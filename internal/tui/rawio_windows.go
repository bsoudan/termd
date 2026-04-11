//go:build windows

package tui

import (
	"os"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/windows"
)

func SetupRawTerminal() (restore func(), err error) {
	fd := os.Stdin.Fd()
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}

	// Enable VT input mode so ReadFile returns VT escape sequences
	// (same byte format as a Unix terminal in raw mode).
	handle := windows.Handle(fd)
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err == nil {
		mode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
		windows.SetConsoleMode(handle, mode)
	}

	// Enable VT processing on stdout so ANSI escape sequences render.
	outHandle := windows.Handle(os.Stdout.Fd())
	var outMode uint32
	if err := windows.GetConsoleMode(outHandle, &outMode); err == nil {
		outMode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.DISABLE_NEWLINE_AUTO_RETURN
		windows.SetConsoleMode(outHandle, outMode)
	}

	return func() { term.Restore(fd, oldState) }, nil
}
