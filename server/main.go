package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tlog "termd/frontend/log"
	"termd/transport"
)

func printUsage() {
	fmt.Fprint(os.Stderr, `Usage: termd [options]

Options:
  -l, --listen <spec>       Listen address (repeatable; default: unix:/tmp/termd.sock)
                             Schemes: unix:<path>, tcp:<host:port>, ws:<host:port>, ssh:<host:port>
  -s, --socket <path>       Shorthand for --listen unix:<path>
      --ssh-host-key <path>  SSH host key file (auto-generated if missing)
      --ssh-auth-keys <path> SSH authorized_keys file (default: ~/.ssh/authorized_keys)
      --ssh-no-auth          Disable SSH authentication (insecure)
  -d, --debug               Enable debug logging (env: TERMD_DEBUG=1)
  -h, --help                Show this help
`)
}

func main() {
	var listenSpecs []string
	var sshHostKey, sshAuthKeys string
	sshNoAuth := false
	debug := false

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printUsage()
			return
		case "-d", "--debug":
			debug = true
		case "-s", "--socket":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --socket requires a path argument")
				os.Exit(1)
			}
			listenSpecs = append(listenSpecs, "unix:"+args[i])
		case "-l", "--listen":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --listen requires an address argument")
				os.Exit(1)
			}
			listenSpecs = append(listenSpecs, args[i])
		case "--ssh-host-key":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --ssh-host-key requires a path argument")
				os.Exit(1)
			}
			sshHostKey = args[i]
		case "--ssh-auth-keys":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --ssh-auth-keys requires a path argument")
				os.Exit(1)
			}
			sshAuthKeys = args[i]
		case "--ssh-no-auth":
			sshNoAuth = true
		default:
			fmt.Fprintf(os.Stderr, "error: unknown option: %s\n", args[i])
			printUsage()
			os.Exit(1)
		}
	}

	if !debug && os.Getenv("TERMD_DEBUG") == "1" {
		debug = true
	}
	if len(listenSpecs) == 0 {
		if v := os.Getenv("TERMD_SOCKET"); v != "" {
			listenSpecs = append(listenSpecs, "unix:"+v)
		} else {
			listenSpecs = append(listenSpecs, "unix:/tmp/termd.sock")
		}
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := tlog.NewHandler(os.Stderr, level, nil)
	slog.SetDefault(slog.New(handler))

	transport.InstallStackDump("termd")

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
			fmt.Fprintf(os.Stderr, "error: listen %s: %v\n", spec, err)
			os.Exit(1)
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
}
