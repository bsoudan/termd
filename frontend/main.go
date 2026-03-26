package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"termd/frontend/client"
	termlog "termd/frontend/log"
	"termd/transport"
	"termd/frontend/ui"
)

func main() {
	socketPath := flag.String("socket", "", "socket path (env: TERMD_SOCKET, default: /tmp/termd.sock)")
	flag.StringVar(socketPath, "s", "", "socket path (shorthand)")
	debug := flag.Bool("debug", false, "enable debug logging (env: TERMD_DEBUG=1)")
	flag.BoolVar(debug, "d", false, "enable debug logging (shorthand)")
	logStderr := flag.Bool("log-stderr", false, "also write logs to stderr (corrupts terminal display)")
	command := flag.String("command", "", "command to run (default: $SHELL or bash)")
	flag.StringVar(command, "c", "", "command to run (shorthand)")
	flag.Parse()

	if !*debug && os.Getenv("TERMD_DEBUG") == "1" {
		*debug = true
	}
	if *socketPath == "" {
		if env := os.Getenv("TERMD_SOCKET"); env != "" {
			*socketPath = env
		} else {
			*socketPath = "/tmp/termd.sock"
		}
	}

	level := slog.LevelWarn
	if *debug {
		level = slog.LevelDebug
	}
	logRing := termlog.NewLogRingBuffer(1000)
	var logW io.Writer
	if *logStderr {
		logW = os.Stderr
	}
	logHandler := termlog.NewHandler(logW, level, logRing)
	slog.SetDefault(slog.New(logHandler))

	// Resolve the command to spawn
	var shell string
	var shellArgs []string
	if *command != "" {
		parts := strings.Fields(*command)
		shell = parts[0]
		shellArgs = parts[1:]
	} else {
		shell = os.Getenv("SHELL")
		if shell == "" {
			var err error
			shell, err = exec.LookPath("bash")
			if err != nil {
				slog.Error("cannot find shell", "error", err)
				os.Exit(1)
			}
		}
	}

	transport.InstallStackDump("termd-frontend")

	endpoint := *socketPath
	if !strings.Contains(endpoint, ":") {
		endpoint = "unix:" + endpoint
	}
	conn, err := transport.Dial(endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connect %s: %v\n", endpoint, err)
		os.Exit(1)
	}
	c := client.New(conn, "termd-frontend")
	defer c.Close()

	restore, err := ui.SetupRawTerminal()
	if err != nil {
		slog.Error("raw mode failed", "error", err)
		os.Exit(1)
	}
	defer restore()

	pipeR, pipeW := io.Pipe()

	model := ui.NewModel(c, shell, shellArgs, logRing, endpoint)
	p := tea.NewProgram(model,
		tea.WithInput(pipeR),
		tea.WithColorProfile(colorprofile.TrueColor),
	)

	stdinDupFd, err := syscall.Dup(int(os.Stdin.Fd()))
	if err != nil {
		slog.Error("dup stdin failed", "error", err)
		os.Exit(1)
	}
	stdinDup := os.NewFile(uintptr(stdinDupFd), "stdin-dup")

	logHandler.SetNotifyFn(func() { p.Send(ui.LogEntryMsg{}) })
	go ui.RawInputLoop(stdinDup, c, model.RegionReady, pipeW, p, model.FocusCh)

	finalModel, err := p.Run()
	stdinDup.Close()

	if err != nil {
		slog.Error("program error", "error", err)
		os.Exit(1)
	}

	if m, ok := finalModel.(ui.Model); ok && m.Detached {
		restore()
		restore = func() {}
		os.Stdout.WriteString("detached\n")
	}
}
