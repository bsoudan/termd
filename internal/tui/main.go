package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/urfave/cli/v3"
	"nxtermd/dist"
	"nxtermd/internal/config"
	"nxtermd/internal/nxlog"
	"nxtermd/internal/transport"
)

var version string

var changelog = dist.Changelog

// Main is the entry point for the nxterm TUI binary.
func Main(v string) {
	version = v
	// On Windows, the ssh:// transport re-executes nxterm.exe inside
	// a ConPTY to disable console echo before launching ssh.exe.
	// Detect this early, before any CLI/TUI setup.
	if len(os.Args) > 1 && os.Args[1] == "--internal-conpty-wrap" {
		args := os.Args[2:]
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		handleConptyWrap(args)
		return
	}

	app := &cli.Command{
		Name:    "nxterm",
		Usage:   "terminal multiplexer TUI client",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "config file path (default: ~/.config/nxterm/config.toml)",
			},
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Value:   defaultSocket,
				Usage:   "server address (unix path or transport spec)",
				Sources: cli.EnvVars("NXTERMD_SOCKET"),
			},
			&cli.StringFlag{
				Name:    "session",
				Aliases: []string{"S"},
				Usage:   "session name (default: server's default, typically 'main')",
				Sources: cli.EnvVars("NXTERMD_SESSION"),
			},
			&cli.BoolFlag{
				Name:    "browse",
				Aliases: []string{"b"},
				Usage:   "open the server connect dialog on startup",
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "enable debug logging",
				Sources: cli.EnvVars("NXTERMD_DEBUG"),
			},
			&cli.StringSliceFlag{
				Name:    "trace",
				Usage:   "enable trace flags (comma-separated, repeatable): wire",
				Sources: cli.EnvVars("NXTERM_TRACE"),
			},
			&cli.BoolFlag{
				Name:  "log-stderr",
				Usage: "also write logs to stderr (corrupts terminal display)",
			},
			&cli.BoolFlag{
				Name:  "show-config",
				Usage: "print the effective configuration with sources and exit",
			},
			&cli.BoolFlag{
				Name:  "show-keybindings",
				Usage: "print all resolved keybindings with sources and exit",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if cmd.Bool("show-config") {
				if err := showFrontendConfig(cmd); err != nil {
					return ctx, err
				}
				os.Exit(0)
			}
			if cmd.Bool("show-keybindings") {
				if err := showKeybindings(); err != nil {
					return ctx, err
				}
				os.Exit(0)
			}
			return ctx, nil
		},
		Action: runFrontend,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runFrontend(_ context.Context, cmd *cli.Command) error {
	// Load config file (provides defaults for unset flags)
	cfg, err := config.LoadFrontendConfig(cmd.String("config"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	kbCfg, err := config.LoadKeybindConfig()
	if err != nil {
		return fmt.Errorf("keybind config: %w", err)
	}
	registry := NewRegistry(kbCfg.Style, kbCfg.Prefix, kbCfg.Overrides())

	// Initialize trace flags from CLI + config + env (env is handled
	// by urfave via the Sources on the flag definition).
	config.SetTraceFlags(cmd.StringSlice("trace")...)
	config.SetTraceFlags(cfg.Trace...)

	debug := cmd.Bool("debug") || cfg.Debug || config.TraceEnabled("wire")
	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}
	logRing := NewLogRingBuffer(1000)
	var logW io.Writer
	if cmd.Bool("log-stderr") {
		logW = os.Stderr
	}
	logHandler := nxlog.NewHandler(logW, level, logRing.Append)
	slog.SetDefault(slog.New(logHandler))

	transport.InstallStackDump("nxterm")

	// CLI --socket > config connect > platform default
	socketVal := cmd.String("socket")
	userSetSocket := cmd.IsSet("socket")
	if !userSetSocket && cfg.Connect != "" {
		socketVal = cfg.Connect
		userSetSocket = true
	}
	endpoint := inferEndpoint(socketVal)
	browse := cmd.Bool("browse")

	// Try to connect. If the user didn't explicitly set an address and
	// the default socket doesn't exist, start in disconnected mode.
	// --browse forces disconnected mode to show the connect dialog.
	//
	// The initial dial happens before SetupRawTerminal, so the
	// terminal is still in cooked mode and the cookedPrompter can
	// read passwords / passphrases / yes-no answers via /dev/tty.
	var conn net.Conn
	if !browse {
		initDial := func() (net.Conn, error) {
			c, err := transport.DialWithPrompter(endpoint, cookedPrompter{})
			if err != nil {
				return nil, err
			}
			return transport.MaybeWrapCompression(transport.WrapTracing(c, "client"), endpoint), nil
		}
		if userSetSocket {
			var err error
			conn, err = initDial()
			if err != nil {
				return fmt.Errorf("connect %s: %w", endpoint, err)
			}
		} else {
			conn, _ = initDial()
		}
	}

	disconnected := conn == nil

	restore, err := SetupRawTerminal()
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer restore()

	pipeR, pipeW := io.Pipe()
	sessionName := cmd.String("session")
	server := NewServer(64, "nxterm")

	var p *tea.Program
	var uiPrompter *UIPrompter
	var wg sync.WaitGroup

	initEndpoint := endpoint
	if disconnected {
		initEndpoint = ""
	}

	// dialFn wraps transport.DialWithPrompter for both the main loop's
	// connectOverlay and the Server's reconnect loop.
	dialFn := func(ep string) (net.Conn, error) {
		c, err := transport.DialWithPrompter(ep, uiPrompter)
		if err != nil {
			return nil, err
		}
		return transport.MaybeWrapCompression(transport.WrapTracing(c, "client"), ep), nil
	}

	// connectFn dials a server and starts a Server.Run goroutine.
	// Used by NxtermModel for "open-session" during an active session.
	connectFn := func(ep, session string) {
		go func() {
			c, err := dialFn(ep)
			if err != nil {
				p.Send(ConnectErrorMsg{Endpoint: ep, Error: err.Error()})
				return
			}
			reconnDialFn := func() (net.Conn, error) {
				return dialFn(ep)
			}
			newSrv := NewServer(64, "nxterm")
			p.Send(ConnectedMsg{Endpoint: ep, Session: session, Server: newSrv})
			newSrv.Run(c, reconnDialFn)
		}()
	}
	main := NewNxtermModel(server, pipeW, registry, AppContext{
		Version:         version,
		Changelog:       changelog,
		Endpoint:        initEndpoint,
		SessionName:     sessionName,
		StatusBarMargin: cfg.GetStatusBarMargin(),
		LogRing:         logRing,
	}, connectFn)

	p = tea.NewProgram(main,
		tea.WithInput(pipeR),
		tea.WithColorProfile(colorprofile.TrueColor),
	)
	uiPrompter = NewUIPrompter(p)

	stdinDup, err := dupStdin()
	if err != nil {
		return fmt.Errorf("dup stdin: %w", err)
	}
	PreUpgradeCleanup = func() { stdinDup.Close() }

	logHandler.SetNotifyFn(func() { p.Send(LogEntryMsg{}) })

	if !disconnected {
		reconnectDialFn := func() (net.Conn, error) {
			return dialFn(endpoint)
		}
		wg.Add(1)
		go func() { defer wg.Done(); server.Run(conn, reconnectDialFn) }()
	}
	rawCh := make(chan RawInputMsg, 16)
	wg.Add(1)
	go func() { defer wg.Done(); InputLoop(stdinDup, rawCh, pipeW) }()

	// Start mDNS browsing when the connect overlay is shown.
	browseCtx, browseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	go browseServers(browseCtx, p)

	err = main.Run(p, rawCh, dialFn, !disconnected)

	browseCancel()

	// Ordered shutdown:
	// 1. Close server (unsubscribe+disconnect already sent by model.quit())
	server.Close()
	// 2. Close stdin dup → unblocks InputLoop
	stdinDup.Close()
	// 3. Close pipe writer → bubbletea's input reader exits
	pipeW.Close()
	// 4. Wait for goroutines (timeout in case stdin close doesn't unblock)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		slog.Debug("shutdown: goroutines did not exit in time")
	}

	if err != nil {
		return fmt.Errorf("program error: %w", err)
	}

	if main.Detached {
		restore()
		os.Stdout.WriteString("detached\n")
	}
	return nil
}
