package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/urfave/cli/v3"
	"nxtermd/config"
	tlog "nxtermd/frontend/log"
	"nxtermd/transport"
)

var version = "dev"

func main() {
	app := &cli.Command{
		Name:      "nxtermd",
		Usage:     "terminal multiplexer server",
		ArgsUsage: "[listen-spec ...]",
		Description: `LISTEN SPECS:
  unix:/path/to/sock     Unix socket
  tcp://host:port        TCP
  ws://host:port         WebSocket
  ssh://host:port        SSH (requires --ssh-host-key)

  Default: unix:/tmp/nxtermd.sock`,
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
				Usage: "config file path (default: ~/.config/nxtermd/server.toml)",
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
				Sources: cli.EnvVars("NXTERMD_DEBUG"),
			},
			&cli.StringFlag{
				Name:  "pprof",
				Usage: "enable pprof HTTP server (default: localhost:6060, or specify host:port)",
			},
			&cli.IntFlag{
				Name:   "upgrade-fd",
				Usage:  "internal: FD for live upgrade handoff",
				Hidden: true,
			},
			&cli.BoolFlag{
				Name:  "show-config",
				Usage: "print the effective configuration with sources and exit",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if cmd.Bool("show-config") {
				if err := showServerConfig(cmd); err != nil {
					return ctx, err
				}
				os.Exit(0)
			}
			return ctx, nil
		},
		Action: runServer,
		Commands: []*cli.Command{
			{
				Name:      "start",
				Usage:     "install and start nxtermd as a systemd user service",
				ArgsUsage: "[listen-spec ...]",
				Action:    cmdStart,
			},
			{
				Name:   "stop",
				Usage:  "stop and remove the nxtermd systemd user service",
				Action: cmdStop,
			},
			{
				Name:   "restart",
				Usage:  "restart the nxtermd systemd user service",
				Action: cmdRestart,
			},
			{
				Name:   "status",
				Usage:  "show the nxtermd systemd user service status",
				Action: cmdStatus,
			},
			{
				Name:            "tail",
				Usage:           "tail the nxtermd service logs (extra args passed to journalctl)",
				SkipFlagParsing: true,
				Action:          cmdTail,
			},
			{
				Name:  "live-upgrade",
				Usage: "upgrade the running nxtermd server without dropping sessions",
				Action: cmdLiveUpgrade,
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
		return []string{"unix:/tmp/nxtermd.sock"}, nil
	}
	for _, s := range specs {
		if !strings.Contains(s, ":") {
			return nil, fmt.Errorf("invalid listen spec %q (missing scheme, e.g. unix:/path or tcp://host:port)", s)
		}
	}
	return specs, nil
}

func sshConfig(cmd *cli.Command, cfg config.ServerConfig) transport.SSHListenerConfig {
	sshHostKey := cmd.String("ssh-host-key")
	if sshHostKey == "" {
		sshHostKey = cfg.SSH.HostKey
	}
	sshAuthKeys := cmd.String("ssh-auth-keys")
	if sshAuthKeys == "" {
		sshAuthKeys = cfg.SSH.AuthorizedKeys
	}
	return transport.SSHListenerConfig{
		HostKeyPath:        sshHostKey,
		AuthorizedKeysPath: sshAuthKeys,
		NoAuth:             cmd.Bool("ssh-no-auth") || cfg.SSH.NoAuth,
	}
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

	transport.InstallStackDump("nxtermd")

	pprofAddr := cmd.String("pprof")
	if pprofAddr == "" {
		pprofAddr = cfg.Pprof
	}
	if pprofAddr != "" {
		startPprof(pprofAddr)
	}

	sshCfg := sshConfig(cmd, cfg)

	// Live upgrade receiver mode.
	if upgradeFD := cmd.Int("upgrade-fd"); upgradeFD > 0 {
		return runUpgradeReceiver(int(upgradeFD), sshCfg)
	}

	specs, err := listenSpecs(cmd, cfg)
	if err != nil {
		return err
	}

	listeners := make([]net.Listener, 0, len(specs))
	for _, spec := range specs {
		var ln net.Listener
		var err error
		if strings.HasPrefix(spec, "ssh:") || strings.HasPrefix(spec, "ssh://") {
			addr := strings.TrimPrefix(strings.TrimPrefix(spec, "ssh:"), "//")
			ln, err = transport.ListenSSH(addr, sshCfg)
		} else {
			ln, err = transport.Listen(spec)
		}
		if err != nil {
			return fmt.Errorf("listen %s: %w", spec, err)
		}
		listeners = append(listeners, ln)
	}

	srv := NewServer(listeners, version, cfg)
	defer srv.Shutdown()

	disc, err := startDiscovery(cfg.Discovery, specs, listeners, version)
	if err != nil {
		slog.Warn("discovery: mDNS registration failed", "err", err)
	}
	defer disc.shutdown()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR2)

	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGUSR2:
				slog.Info("received upgrade signal (SIGUSR2)")
				if err := srv.HandleUpgrade(specs, sshCfg); err != nil {
					slog.Error("live upgrade failed", "err", err)
					continue // server keeps running
				}
				// Upgrade succeeded — stop the server. Don't remove
				// Unix socket files; the new process owns them now.
				srv.SetUnlinkOnClose(false)
				srv.Shutdown()
				return
			default:
				slog.Info("received shutdown signal")
				srv.Shutdown()
				return
			}
		}
	}()

	// Notify systemd that the server is ready.
	sdNotify("READY=1")

	srv.Run()
	return nil
}

func cmdLiveUpgrade(_ context.Context, cmd *cli.Command) error {
	cfg, err := config.LoadServerConfig(cmd.String("config"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	specs, err := listenSpecs(cmd, cfg)
	if err != nil {
		return err
	}

	// Connect to the server using the first listen spec.
	conn, err := transport.Dial(specs[0])
	if err != nil {
		return fmt.Errorf("connect to %s: %w", specs[0], err)
	}
	defer conn.Close()

	// Read the Identify message to get the server PID.
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read identify: %w", err)
	}
	var ident struct {
		Pid int `json:"pid"`
	}
	if err := json.Unmarshal(buf[:n], &ident); err != nil {
		return fmt.Errorf("parse identify: %w", err)
	}
	if ident.Pid == 0 {
		return fmt.Errorf("server did not report its PID")
	}

	fmt.Fprintf(os.Stderr, "sending SIGUSR2 to nxtermd (pid %d)...\n", ident.Pid)
	if err := syscall.Kill(ident.Pid, syscall.SIGUSR2); err != nil {
		return fmt.Errorf("kill -USR2 %d: %w", ident.Pid, err)
	}
	fmt.Fprintf(os.Stderr, "upgrade signal sent\n")
	return nil
}

func runUpgradeReceiver(fd int, sshCfg transport.SSHListenerConfig) error {
	slog.Info("starting in upgrade receiver mode", "fd", fd)
	srv, _, specs, err := RecvUpgrade(fd, sshCfg, version)
	if err != nil {
		return fmt.Errorf("upgrade recv: %w", err)
	}

	// Tell systemd we're the new main process and we're ready.
	sdNotify(fmt.Sprintf("MAINPID=%d", os.Getpid()))
	sdNotify("READY=1")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR2)

	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGUSR2:
				slog.Info("received upgrade signal (SIGUSR2)")
				if err := srv.HandleUpgrade(specs, sshCfg); err != nil {
					slog.Error("live upgrade failed", "err", err)
					continue
				}
				srv.SetUnlinkOnClose(false)
				srv.Shutdown()
				return
			default:
				slog.Info("received shutdown signal")
				srv.Shutdown()
				return
			}
		}
	}()

	srv.Run()
	return nil
}
