package main

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"termd/frontend/client"
	"termd/frontend/ui"
)

func main() {
	debug := flag.Bool("debug", false, "enable debug logging to stderr")
	flag.Parse()

	if !*debug && os.Getenv("TERMD_DEBUG") == "1" {
		*debug = true
	}

	if *debug {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
			&slog.HandlerOptions{Level: slog.LevelDebug})))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
			&slog.HandlerOptions{Level: slog.LevelWarn})))
	}

	socketPath := "/tmp/termd.sock"
	if flag.NArg() > 0 {
		socketPath = flag.Arg(0)
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		var err error
		shell, err = exec.LookPath("bash")
		if err != nil {
			slog.Error("cannot find shell", "error", err)
			os.Exit(1)
		}
	}

	c, err := client.New(socketPath)
	if err != nil {
		slog.Error("connect failed", "error", err)
		os.Exit(1)
	}
	defer c.Close()

	// Put stdin in raw mode so we can read raw bytes.
	restore, err := ui.SetupRawTerminal()
	if err != nil {
		slog.Error("raw mode failed", "error", err)
		os.Exit(1)
	}
	defer restore()

	// Create a pipe for bubbletea's input. Bubbletea reads from pipeR
	// (which blocks forever — no parsed key messages). We read raw stdin
	// ourselves in a separate goroutine.
	pipeR, pipeW := io.Pipe()

	model := ui.NewModel(c, shell, []string{})
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(pipeR))

	// Start the raw input goroutine. On detach, it tells bubbletea to quit.
	exitReason := make(chan ui.ExitReason, 1)
	go func() {
		reason := ui.RawInputLoop(os.Stdin, c, model.RegionReady, pipeW)
		exitReason <- reason
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		slog.Error("program error", "error", err)
		os.Exit(1)
	}

	select {
	case reason := <-exitReason:
		if reason == ui.ExitDetach {
			restore()
			restore = func() {}
			os.Stdout.WriteString("detached\n")
		}
	default:
	}
}
