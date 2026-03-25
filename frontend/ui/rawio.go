package ui

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"os"

	"termd/frontend/client"
	"termd/frontend/protocol"

	"github.com/charmbracelet/x/term"
)

const prefixKey = 0x02 // ctrl+b

// SetupRawTerminal puts stdin into raw mode and returns a restore function.
func SetupRawTerminal() (restore func(), err error) {
	fd := os.Stdin.Fd()
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { term.Restore(fd, oldState) }, nil
}

// ExitReason indicates why the raw input loop exited.
type ExitReason int

const (
	ExitError   ExitReason = iota // stdin error or send error
	ExitDetach                    // user pressed ctrl+b d
	ExitClosed                    // regionReady channel closed
)

// RawInputLoop reads raw bytes from stdin and forwards them to the server.
// It blocks on regionReady until the handshake completes, then reads stdin
// in a loop. Handles the prefix key (ctrl+b) for frontend commands.
// Returns the reason for exiting.
func RawInputLoop(stdin *os.File, c *client.Client, regionReady <-chan string, pipeW interface{ Close() error }) ExitReason {
	defer pipeW.Close()

	regionID, ok := <-regionReady
	if !ok {
		return ExitClosed
	}
	slog.Debug("raw input loop started", "region_id", regionID)

	prefixActive := false
	buf := make([]byte, 4096)
	for {
		n, err := stdin.Read(buf)
		if err != nil {
			slog.Debug("raw input read error", "error", err)
			return ExitError
		}
		if n == 0 {
			continue
		}

		chunk := buf[:n]

		if prefixActive {
			// First byte after prefix key determines the command.
			switch chunk[0] {
			case 'd':
				slog.Debug("prefix: detach")
				return ExitDetach
			case prefixKey:
				// Send a literal ctrl+b to the program.
				sendInput(c, regionID, []byte{prefixKey})
			default:
				// Unknown prefix command — discard the byte.
				slog.Debug("prefix: unknown command", "byte", chunk[0])
			}
			prefixActive = false
			// Process remaining bytes in the chunk (if any).
			chunk = chunk[1:]
			if len(chunk) == 0 {
				continue
			}
		}

		// Scan for prefix key in the chunk.
		if idx := bytes.IndexByte(chunk, prefixKey); idx >= 0 {
			// Send everything before the prefix key.
			if idx > 0 {
				sendInput(c, regionID, chunk[:idx])
			}
			// Enter prefix mode. Remaining bytes after the prefix key
			// will be processed on the next iteration (or if there are
			// more bytes in this chunk, handle them now).
			rest := chunk[idx+1:]
			if len(rest) > 0 {
				// We have bytes after ctrl+b in the same read.
				switch rest[0] {
				case 'd':
					slog.Debug("prefix: detach")
					return ExitDetach
				case prefixKey:
					sendInput(c, regionID, []byte{prefixKey})
				default:
					slog.Debug("prefix: unknown command", "byte", rest[0])
				}
				// Send any remaining bytes after the prefix command.
				if len(rest) > 1 {
					sendInput(c, regionID, rest[1:])
				}
			} else {
				// ctrl+b was the last byte — wait for next read.
				prefixActive = true
			}
			continue
		}

		// No prefix key — send the entire chunk.
		sendInput(c, regionID, chunk)
	}
}

func sendInput(c *client.Client, regionID string, raw []byte) {
	if len(raw) == 0 {
		return
	}
	data := base64.StdEncoding.EncodeToString(raw)
	if err := c.Send(protocol.InputMsg{
		Type:     "input",
		RegionID: regionID,
		Data:     data,
	}); err != nil {
		slog.Debug("raw input send error", "error", err)
	}
}
