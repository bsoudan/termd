package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/urfave/cli/v3"
	"termd/config"
	tlog "termd/frontend/log"
	"termd/transport"
)

var version = "dev"

func main() {
	app := &cli.Command{
		Name:      "termd",
		Usage:     "terminal multiplexer server",
		ArgsUsage: "[listen-spec ...]",
		Description: `LISTEN SPECS:
  unix:/path/to/sock     Unix socket
  tcp://host:port        TCP
  ws://host:port         WebSocket
  ssh://host:port        SSH (requires --ssh-host-key)

  Default: unix:/tmp/termd.sock`,
		Version: version,
		CustomRootCommandHelpTemplate: `NAME:
   {{template "helpNameTemplate" .}}

USAGE:
   {{.FullName}} {{if .VisibleFlags}}[global options]{{end}}{{if .VisibleCommands}} [command [command options]]{{end}} {{.ArgsUsage}}{{if .Version}}{{if not .HideVersion}}

VERSION:
   {{.Version}}{{end}}{{end}}{{if .VisibleCommands}}

COMMANDS:{{template "visibleCommandCategoryTemplate" .}}{{end}}{{if .VisibleFlagCategories}}

GLOBAL OPTIONS:{{template "visibleFlagCategoryTemplate" .}}{{else if .VisibleFlags}}

GLOBAL OPTIONS:{{template "visibleFlagTemplate" .}}{{end}}{{if .Description}}

{{template "descriptionTemplate" .}}{{end}}
`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "config file path (default: ~/.config/termd/server.toml)",
			},
			&cli.StringFlag{
				Name:  "ssh-host-key",
				Usage: "SSH host key file (auto-generated if missing)",
			},
			&cli.StringFlag{
				Name:  "ssh-auth-keys",
				Usage: "SSH authorized_keys file (default: ~/.ssh/authorized_keys)",
			},
			&cli.BoolFlag{
				Name:  "ssh-no-auth",
				Usage: "disable SSH authentication (insecure)",
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "enable debug logging",
				Sources: cli.EnvVars("TERMD_DEBUG"),
			},
		},
		Action: runServer,
		Commands: []*cli.Command{
			{
				Name:      "start",
				Usage:     "install and start termd as a systemd user service",
				ArgsUsage: "[listen-spec ...]",
				Action:    cmdStart,
			},
			{
				Name:   "stop",
				Usage:  "stop and remove the termd systemd user service",
				Action: cmdStop,
			},
			{
				Name:   "restart",
				Usage:  "restart the termd systemd user service",
				Action: cmdRestart,
			},
			{
				Name:   "status",
				Usage:  "show the termd systemd user service status",
				Action: cmdStatus,
			},
			{
				Name:            "tail",
				Usage:           "tail the termd service logs (extra args passed to journalctl)",
				SkipFlagParsing: true,
				Action:          cmdTail,
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// listenSpecs returns the listen addresses from positional args, config, or default.
// Returns an error if any spec is missing a scheme.
func listenSpecs(cmd *cli.Command, cfg config.ServerConfig) ([]string, error) {
	var specs []string
	if cmd.NArg() > 0 {
		specs = cmd.Args().Slice()
	} else if len(cfg.Listen) > 0 {
		specs = cfg.Listen
	} else {
		return []string{"unix:/tmp/termd.sock"}, nil
	}
	for _, s := range specs {
		if !strings.Contains(s, ":") {
			return nil, fmt.Errorf("invalid listen spec %q (missing scheme, e.g. unix:/path or tcp://host:port)", s)
		}
	}
	return specs, nil
}

func runServer(_ context.Context, cmd *cli.Command) error {
	// Load config file (provides defaults for unset flags)
	cfg, err := config.LoadServerConfig(cmd.String("config"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	debug := cmd.Bool("debug") || cfg.Debug
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := tlog.NewHandler(os.Stderr, level, nil)
	slog.SetDefault(slog.New(handler))

	transport.InstallStackDump("termd")

	specs, err := listenSpecs(cmd, cfg)
	if err != nil {
		return err
	}

	sshHostKey := cmd.String("ssh-host-key")
	if sshHostKey == "" {
		sshHostKey = cfg.SSH.HostKey
	}
	sshAuthKeys := cmd.String("ssh-auth-keys")
	if sshAuthKeys == "" {
		sshAuthKeys = cfg.SSH.AuthorizedKeys
	}
	sshNoAuth := cmd.Bool("ssh-no-auth") || cfg.SSH.NoAuth

	listeners := make([]net.Listener, 0, len(specs))
	for _, spec := range specs {
		var ln net.Listener
		var err error
		if strings.HasPrefix(spec, "ssh:") || strings.HasPrefix(spec, "ssh://") {
			addr := strings.TrimPrefix(strings.TrimPrefix(spec, "ssh:"), "//")
			ln, err = transport.ListenSSH(addr, transport.SSHListenerConfig{
				HostKeyPath:        sshHostKey,
				AuthorizedKeysPath: sshAuthKeys,
				NoAuth:             sshNoAuth,
			})
		} else {
			ln, err = transport.Listen(spec)
		}
		if err != nil {
			return fmt.Errorf("listen %s: %w", spec, err)
		}
		listeners = append(listeners, ln)
	}

	srv := NewServer(listeners, version, cfg.Sessions)
	defer srv.Shutdown()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		slog.Info("received shutdown signal")
		srv.Shutdown()
	}()

	srv.Run()
	return nil
}
