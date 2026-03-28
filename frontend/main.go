package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
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
				Name:    "command",
				Aliases: []string{"c"},
				Usage:   "command to run (default: $SHELL or bash)",
				Sources: cli.EnvVars("TERMD_COMMAND"),
			},
			&cli.StringFlag{
				Name:    "session",
				Aliases: []string{"S"},
				Usage:   "session name (default: server's default, typically 'main')",
				Sources: cli.EnvVars("TERMD_SESSION"),
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

	// Resolve the command to spawn
	var shell string
	var shellArgs []string
	if c := cmd.String("command"); c != "" {
		parts := strings.Fields(c)
		shell = parts[0]
		shellArgs = parts[1:]
	} else if cfg.Command != "" {
		parts := strings.Fields(cfg.Command)
		shell = parts[0]
		shellArgs = parts[1:]
	} else {
		shell, shellArgs = defaultShell()
	}

	transport.InstallStackDump("termd-tui")

	// CLI --socket > config connect > platform default
	socketVal := cmd.String("socket")
	if socketVal == defaultSocket && cfg.Connect != "" {
		socketVal = cfg.Connect
	}
	endpoint := inferEndpoint(socketVal)

	dialFn := func() (net.Conn, error) { return transport.Dial(endpoint) }
	conn, err := dialFn()
	if err != nil {
		return fmt.Errorf("connect %s: %w", endpoint, err)
	}

	restore, err := ui.SetupRawTerminal()
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer restore()

	pipeR, pipeW := io.Pipe()

	sessionName := cmd.String("session")

	server := ui.NewServer(64, "termd-tui")
	model := ui.NewModel(server, pipeW, shell, shellArgs, logRing, endpoint, version, changelog, sessionName)
	p := tea.NewProgram(model,
		tea.WithInput(pipeR),
		tea.WithColorProfile(colorprofile.TrueColor),
	)

	stdinDup, err := dupStdin()
	if err != nil {
		return fmt.Errorf("dup stdin: %w", err)
	}

	logHandler.SetNotifyFn(func() { p.Send(ui.LogEntryMsg{}) })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); server.Run(conn, dialFn, p) }()
	go func() { defer wg.Done(); ui.InputLoop(stdinDup, p) }()

	finalModel, err := p.Run()

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
