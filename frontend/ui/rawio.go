package ui

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

type prefixStartedMsg struct{}

const prefixKey = 0x02 // ctrl+b

func SetupRawTerminal() (restore func(), err error) {
	fd := os.Stdin.Fd()
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { term.Restore(fd, oldState) }, nil
}

// RawInputLoop reads raw bytes from stdin and forwards them to the server.
// When ctrl+b is pressed, one byte is diverted to bubbletea for prefix
// command handling. If bubbletea requests extended input focus (e.g., for
// the log viewer), it sends a done channel on focusCh; the raw loop stays
// in focus mode until that channel is closed.
func RawInputLoop(stdin *os.File, c *client.Client, regionReady <-chan string, pipeW io.WriteCloser, program *tea.Program, focusCh <-chan chan struct{}) {
	defer pipeW.Close()

	regionID, ok := <-regionReady
	if !ok {
		return
	}
	slog.Debug("raw input loop started", "region_id", regionID)

	var focusDone chan struct{}
	prefixActive := false
	buf := make([]byte, 4096)

	for {
		select {
		case done := <-focusCh:
			focusDone = done
		default:
		}

		n, err := stdin.Read(buf)

		// Re-check after read — bubbletea may have requested focus while
		// we were blocked on stdin.
		select {
		case done := <-focusCh:
			focusDone = done
		default:
		}
		if err != nil {
			slog.Debug("raw input read error", "error", err)
			return
		}
		if n == 0 {
			continue
		}

		chunk := buf[:n]

		// Focus mode: all input goes to bubbletea.
		if focusDone != nil {
			select {
			case <-focusDone:
				focusDone = nil
			default:
				pipeW.Write(chunk)
				continue
			}
		}

		// Prefix active: divert one byte to bubbletea for the command.
		if prefixActive {
			pipeW.Write(chunk[:1])
			prefixActive = false
			chunk = chunk[1:]
			if len(chunk) == 0 {
				continue
			}
		}

		// Scan for prefix key.
		if idx := bytes.IndexByte(chunk, prefixKey); idx >= 0 {
			if idx > 0 {
				sendInput(c, regionID, chunk[:idx])
			}
			program.Send(prefixStartedMsg{})
			rest := chunk[idx+1:]
			if len(rest) > 0 {
				pipeW.Write(rest[:1])
				if len(rest) > 1 {
					sendInput(c, regionID, rest[1:])
				}
			} else {
				prefixActive = true
			}
			continue
		}

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
