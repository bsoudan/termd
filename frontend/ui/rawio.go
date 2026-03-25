package ui

import (
	"encoding/base64"
	"log/slog"
	"os"

	"termd/frontend/client"
	"termd/frontend/protocol"

	"github.com/charmbracelet/x/term"
)

// SetupRawTerminal puts stdin into raw mode and returns a restore function.
func SetupRawTerminal() (restore func(), err error) {
	fd := os.Stdin.Fd()
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { term.Restore(fd, oldState) }, nil
}

// RawInputLoop reads raw bytes from stdin and forwards them to the server.
// It blocks on regionReady until the handshake completes, then reads stdin
// in a loop. Closes pipeW on exit so bubbletea's input reader unblocks.
func RawInputLoop(stdin *os.File, c *client.Client, regionReady <-chan string, pipeW interface{ Close() error }) {
	defer pipeW.Close()

	// Wait for the region to be ready before forwarding input.
	regionID, ok := <-regionReady
	if !ok {
		return
	}
	slog.Debug("raw input loop started", "region_id", regionID)

	buf := make([]byte, 4096)
	for {
		n, err := stdin.Read(buf)
		if err != nil {
			slog.Debug("raw input read error", "error", err)
			return
		}
		if n == 0 {
			continue
		}

		data := base64.StdEncoding.EncodeToString(buf[:n])
		if err := c.Send(protocol.InputMsg{
			Type:     "input",
			RegionID: regionID,
			Data:     data,
		}); err != nil {
			slog.Debug("raw input send error", "error", err)
			return
		}
	}
}
