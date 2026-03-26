package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	tlog "termd/frontend/log"
)

func printUsage() {
	fmt.Fprint(os.Stderr, `Usage: termd [options]

Options:
  -s, --socket <path>  Socket path (env: TERMD_SOCKET, default: /tmp/termd.sock)
  -d, --debug          Enable debug logging (env: TERMD_DEBUG=1)
  -h, --help           Show this help
`)
}

func main() {
	socketPath := "/tmp/termd.sock"
	socketFlagSet := false
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
				printUsage()
				os.Exit(1)
			}
			socketPath = args[i]
			socketFlagSet = true
		default:
			fmt.Fprintf(os.Stderr, "error: unknown option: %s\n", args[i])
			printUsage()
			os.Exit(1)
		}
	}

	if !debug {
		if os.Getenv("TERMD_DEBUG") == "1" {
			debug = true
		}
	}
	if !socketFlagSet {
		if v := os.Getenv("TERMD_SOCKET"); v != "" {
			socketPath = v
		}
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := tlog.NewHandler(os.Stderr, level, nil)
	slog.SetDefault(slog.New(handler))

	srv, err := NewServer(socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
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
