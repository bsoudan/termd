package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"

	"termd/config"
	"termd/frontend/client"
	termlog "termd/frontend/log"
	"termd/transport"

	"github.com/urfave/cli/v3"
)

var version = "dev"

//go:embed changelog.txt
var changelog string

func main() {
	app := &cli.Command{
		Name:    "termd-gui",
		Usage:   "terminal multiplexer GUI client",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "config file path (default: ~/.config/termd-tui/config.toml)",
			},
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Value:   "/tmp/termd.sock",
				Usage:   "server address (unix path or transport spec)",
				Sources: cli.EnvVars("TERMD_SOCKET"),
			},
			&cli.StringFlag{
				Name:    "command",
				Aliases: []string{"c"},
				Usage:   "command to run (default: $SHELL or bash)",
				Sources: cli.EnvVars("TERMD_COMMAND"),
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "enable debug logging",
				Sources: cli.EnvVars("TERMD_DEBUG"),
			},
		},
		Action: runGUI,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runGUI(_ context.Context, cmd *cli.Command) error {
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
	var logW io.Writer = os.Stderr
	logHandler := termlog.NewHandler(logW, level, logRing)
	slog.SetDefault(slog.New(logHandler))

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
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell, err = exec.LookPath("bash")
			if err != nil {
				shell = "sh"
			}
		}
	}

	transport.InstallStackDump("termd-gui")

	socketVal := cmd.String("socket")
	if socketVal == "/tmp/termd.sock" && cfg.Connect != "" {
		socketVal = cfg.Connect
	}
	endpoint := socketVal

	dialFn := func() (net.Conn, error) { return transport.Dial(endpoint) }
	conn, err := dialFn()
	if err != nil {
		return fmt.Errorf("connect %s: %w", endpoint, err)
	}
	c := client.New(conn, dialFn, "termd-gui")
	defer c.Close()

	app := newApp(c, shell, shellArgs, logRing, endpoint, version, changelog)
	app.run()
	return nil
}
