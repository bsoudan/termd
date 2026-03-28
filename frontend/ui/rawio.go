package ui

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

const prefixKey = 0x02 // ctrl+b

// RawInputLoop reads raw bytes from stdin and forwards them to the server.
// When ctrl+b is pressed, one byte is diverted to bubbletea for prefix
// command handling. If bubbletea requests extended input focus (e.g., for
// the log viewer), it sends a done channel on focusCh; the raw loop stays
// in focus mode until that channel is closed.
func RawInputLoop(stdin *os.File, c *client.Client, regionReady <-chan string, pipeW io.WriteCloser, program *tea.Program, focusCh <-chan chan struct{}, childWantsMouse *atomic.Bool) {
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
				// Mouse sequences must be parsed and sent via program.Send
				// because bubbletea can't reliably parse SGR mouse from a pipe.
				if bytes.Contains(chunk, sgrMousePrefix) {
					sendSplitMouseInput(c, regionID, chunk, program)
				} else {
					pipeW.Write(chunk)
				}
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

		// When the child has mouse mode enabled, forward mouse events
		// directly to the server (with row adjustment) for lowest latency.
		// Otherwise, parse them and send to bubbletea for scrollback.
		if bytes.Contains(chunk, sgrMousePrefix) {
			if childWantsMouse.Load() {
				sendInput(c, regionID, adjustMouseRow(chunk, chromeRows))
			} else {
				sendSplitMouseInput(c, regionID, chunk, program)
			}
		} else {
			sendInput(c, regionID, chunk)
		}
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

// sendSplitMouseInput separates SGR mouse sequences from other bytes.
// Mouse sequences are parsed and sent to bubbletea as MouseMsg via program.Send().
// Non-mouse bytes are forwarded to the server as regular input.
func sendSplitMouseInput(c *client.Client, regionID string, buf []byte, program *tea.Program) {
	for len(buf) > 0 {
		idx := bytes.Index(buf, sgrMousePrefix)
		if idx < 0 {
			sendInput(c, regionID, buf)
			return
		}
		if idx > 0 {
			sendInput(c, regionID, buf[:idx])
		}
		buf = buf[idx:]

		end := -1
		for i := 3; i < len(buf); i++ {
			if buf[i] == 'M' || buf[i] == 'm' {
				end = i
				break
			}
			if buf[i] != ';' && (buf[i] < '0' || buf[i] > '9') {
				break
			}
		}
		if end < 0 {
			sendInput(c, regionID, buf)
			return
		}

		if msg := parseSGRMouse(buf[:end+1]); msg != nil {
			program.Send(msg)
		}
		buf = buf[end+1:]
	}
}

// parseSGRMouse parses an SGR mouse sequence and returns the corresponding
// bubbletea MouseMsg. Returns nil if parsing fails.
// Format: ESC [ < btn ; col ; row M/m (1-based coordinates)
func parseSGRMouse(seq []byte) tea.MouseMsg {
	if len(seq) < 7 || seq[0] != 0x1b || seq[1] != '[' || seq[2] != '<' {
		return nil
	}
	terminator := seq[len(seq)-1]
	params := string(seq[3 : len(seq)-1])
	parts := bytes.Split([]byte(params), []byte{';'})
	if len(parts) != 3 {
		return nil
	}
	btn, err := strconv.Atoi(string(parts[0]))
	if err != nil {
		return nil
	}
	col, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return nil
	}
	row, err := strconv.Atoi(string(parts[2]))
	if err != nil {
		return nil
	}

	// Convert from 1-based SGR to 0-based bubbletea coordinates
	x := col - 1
	y := row - 1
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	// Wheel events
	if btn == 64 {
		return tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseWheelUp})
	}
	if btn == 65 {
		return tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseWheelDown})
	}

	// Motion events (bit 5 set)
	if btn&32 != 0 {
		button := sgrToTeaButton(btn & 3)
		return tea.MouseMotionMsg(tea.Mouse{X: x, Y: y, Button: button})
	}

	button := sgrToTeaButton(btn & 3)
	if terminator == 'm' {
		return tea.MouseReleaseMsg(tea.Mouse{X: x, Y: y, Button: button})
	}
	return tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: button})
}

func sgrToTeaButton(btn int) tea.MouseButton {
	switch btn {
	case 0:
		return tea.MouseLeft
	case 1:
		return tea.MouseMiddle
	case 2:
		return tea.MouseRight
	default:
		return tea.MouseLeft
	}
}

// sgrMousePrefix is the byte sequence that starts an SGR mouse event.
var sgrMousePrefix = []byte{0x1b, '[', '<'}

// chromeRows is the number of rows used by termd-tui's chrome (tab bar)
// above the content area. Mouse coordinates must be adjusted by this offset.
const chromeRows = 1

// adjustMouseRow rewrites SGR mouse sequences in buf to subtract rowOffset
// from the row coordinate. SGR format: ESC [ < btn ; col ; row M/m
func adjustMouseRow(buf []byte, rowOffset int) []byte {
	result := make([]byte, 0, len(buf))
	for len(buf) > 0 {
		idx := bytes.Index(buf, sgrMousePrefix)
		if idx < 0 {
			result = append(result, buf...)
			break
		}
		result = append(result, buf[:idx]...)
		buf = buf[idx:]

		end := -1
		for i := 3; i < len(buf); i++ {
			if buf[i] == 'M' || buf[i] == 'm' {
				end = i
				break
			}
			if buf[i] != ';' && (buf[i] < '0' || buf[i] > '9') {
				break
			}
		}
		if end < 0 {
			result = append(result, buf...)
			break
		}

		params := string(buf[3:end])
		terminator := buf[end]
		parts := bytes.Split([]byte(params), []byte{';'})
		if len(parts) == 3 {
			row, err := strconv.Atoi(string(parts[2]))
			if err == nil {
				row -= rowOffset
				if row < 1 {
					row = 1
				}
				result = append(result, 0x1b, '[', '<')
				result = append(result, parts[0]...)
				result = append(result, ';')
				result = append(result, parts[1]...)
				result = append(result, ';')
				result = append(result, []byte(strconv.Itoa(row))...)
				result = append(result, terminator)
				buf = buf[end+1:]
				continue
			}
		}
		result = append(result, buf[:end+1]...)
		buf = buf[end+1:]
	}
	return result
}
