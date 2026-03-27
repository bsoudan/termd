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
	tlog "termd/frontend/log"
	"termd/transport"
)

var version = "dev"

func main() {
	app := &cli.Command{
		Name:    "termd",
		Usage:   "terminal multiplexer server",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:    "listen",
				Aliases: []string{"l"},
				Usage:   "listen address (repeatable; schemes: unix, tcp, ws, ssh)",
				Sources: cli.EnvVars("TERMD_LISTEN"),
			},
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Usage:   "shorthand for --listen unix:<path>",
				Sources: cli.EnvVars("TERMD_SOCKET"),
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
				Name:   "start",
				Usage:  "install and start termd as a systemd user service",
				Action: cmdStart,
			},
			{
				Name:   "stop",
				Usage:  "stop and remove the termd systemd user service",
				Action: cmdStop,
			},
			{
				Name:   "status",
				Usage:  "show the termd systemd user service status",
				Action: cmdStatus,
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runServer(_ context.Context, cmd *cli.Command) error {
	level := slog.LevelInfo
	if cmd.Bool("debug") {
		level = slog.LevelDebug
	}
	handler := tlog.NewHandler(os.Stderr, level, nil)
	slog.SetDefault(slog.New(handler))

	transport.InstallStackDump("termd")

	// Build listen specs: --socket prepends to --listen, default if neither given
	listenSpecs := cmd.StringSlice("listen")
	if sock := cmd.String("socket"); sock != "" {
		listenSpecs = append([]string{"unix:" + sock}, listenSpecs...)
	}
	if len(listenSpecs) == 0 {
		listenSpecs = []string{"unix:/tmp/termd.sock"}
	}

	sshHostKey := cmd.String("ssh-host-key")
	sshAuthKeys := cmd.String("ssh-auth-keys")
	sshNoAuth := cmd.Bool("ssh-no-auth")

	listeners := make([]net.Listener, 0, len(listenSpecs))
	for _, spec := range listenSpecs {
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

	srv := NewServer(listeners)
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
