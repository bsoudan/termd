//go:build !windows

package tui

import (
	"os"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

func SetupRawTerminal() (restore func(), err error) {
	fd := os.Stdin.Fd()
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	// MakeRaw clears OPOST which disables ONLCR (\n → \r\n translation).
	// Re-enable it so terminal output renders correctly.
	termios, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	if err == nil {
		termios.Oflag |= unix.OPOST | unix.ONLCR
		unix.IoctlSetTermios(int(fd), unix.TCSETS, termios)
	}
	return func() { term.Restore(fd, oldState) }, nil
}
