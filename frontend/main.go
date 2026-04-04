package main

import (
	"context"
	_ "embed"
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
	"termd/config"
	termlog "termd/frontend/log"
	"termd/frontend/ui"
	"termd/transport"
)

var version = "dev"

//go:embed changelog.txt
var changelog string

func main() {
	app := &cli.Command{
		Name:    "termd-tui",
		Usage:   "terminal multiplexer TUI client",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "config file path (default: ~/.config/termd-tui/config.toml)",
			},
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Value:   defaultSocket,
				Usage:   "server address (unix path or transport spec)",
				Sources: cli.EnvVars("TERMD_SOCKET"),
			},
			&cli.StringFlag{
				Name:    "session",
				Aliases: []string{"S"},
				Usage:   "session name (default: server's default, typically 'main')",
				Sources: cli.EnvVars("TERMD_SESSION"),
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
				Sources: cli.EnvVars("TERMD_DEBUG"),
			},
			&cli.BoolFlag{
				Name:  "log-stderr",
				Usage: "also write logs to stderr (corrupts terminal display)",
			},
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
	registry := ui.NewRegistry(kbCfg.Style, kbCfg.Prefix, kbCfg.Overrides())

	debug := cmd.Bool("debug") || cfg.Debug
	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}
	logRing := termlog.NewLogRingBuffer(1000)
	var logW io.Writer
	if cmd.Bool("log-stderr") {
		logW = os.Stderr
	}
	logHandler := termlog.NewHandler(logW, level, logRing)
	slog.SetDefault(slog.New(logHandler))

	transport.InstallStackDump("termd-tui")

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
	var conn net.Conn
	if !browse {
		dialFn := func() (net.Conn, error) { return transport.Dial(endpoint) }
		if userSetSocket {
			var err error
			conn, err = dialFn()
			if err != nil {
				return fmt.Errorf("connect %s: %w", endpoint, err)
			}
		} else {
			conn, _ = dialFn()
		}
	}

	disconnected := conn == nil

	restore, err := ui.SetupRawTerminal()
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer restore()

	pipeR, pipeW := io.Pipe()
	sessionName := cmd.String("session")
	server := ui.NewServer(64, "termd-tui")

	// p is declared here so the connectFn closure can capture it.
	// It is assigned after NewModel below.
	var p *tea.Program
	var wg sync.WaitGroup

	// connectFn dials a server and starts a Server.Run goroutine.
	// It creates a fresh Server each time so it works for both the
	// initial connect and subsequent reconnects from the connect overlay.
	connectFn := func(ep string) {
		go func() {
			c, err := transport.Dial(ep)
			if err != nil {
				p.Send(ui.ConnectErrorMsg{Endpoint: ep, Error: err.Error()})
				return
			}
			df := func() (net.Conn, error) { return transport.Dial(ep) }
			newSrv := ui.NewServer(64, "termd-tui")
			p.Send(ui.ConnectedMsg{Endpoint: ep, Server: newSrv})
			newSrv.Run(c, df, p)
		}()
	}

	initEndpoint := endpoint
	if disconnected {
		initEndpoint = ""
	}

	model := ui.NewModel(server, pipeW, registry, logRing, initEndpoint, version, changelog, sessionName, connectFn)
	p = tea.NewProgram(model,
		tea.WithInput(pipeR),
		tea.WithColorProfile(colorprofile.TrueColor),
	)

	stdinDup, err := dupStdin()
	if err != nil {
		return fmt.Errorf("dup stdin: %w", err)
	}
	ui.PreUpgradeCleanup = func() { stdinDup.Close() }

	logHandler.SetNotifyFn(func() { p.Send(ui.LogEntryMsg{}) })

	if !disconnected {
		dialFn := func() (net.Conn, error) { return transport.Dial(endpoint) }
		wg.Add(1)
		go func() { defer wg.Done(); server.Run(conn, dialFn, p) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); ui.InputLoop(stdinDup, p, pipeW, model.InitDone()) }()

	// Start mDNS browsing when the connect overlay is shown.
	browseCtx, browseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	go browseServers(browseCtx, p)

	finalModel, err := p.Run()

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

	if m, ok := finalModel.(ui.Model); ok && m.Detached {
		restore()
		os.Stdout.WriteString("detached\n")
	}
	return nil
}
