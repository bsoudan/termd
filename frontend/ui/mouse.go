package ui

import (
	"bytes"
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// sgrMousePrefix is the byte sequence that starts an SGR mouse event.
var sgrMousePrefix = []byte{0x1b, '[', '<'}

// chromeRows is the number of rows used by termd-tui's chrome (tab bar)
// above the content area.
const chromeRows = 1

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

	x := col - 1
	y := row - 1
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	if btn == 64 {
		return tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseWheelUp})
	}
	if btn == 65 {
		return tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseWheelDown})
	}

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

// encodeSGRMouse encodes a mouse event as an SGR mouse escape sequence.
// Format: ESC [ < button ; col ; row M (press) or m (release)
func encodeSGRMouse(msg tea.MouseMsg, col, row int) string {
	if row < 0 {
		row = 0
	}
	col++
	row++

	var button int
	var suffix byte

	switch e := msg.(type) {
	case tea.MouseClickMsg:
		suffix = 'M'
		button = mouseButtonSGR(e.Button)
	case tea.MouseReleaseMsg:
		suffix = 'm'
		button = mouseButtonSGR(e.Button)
	case tea.MouseWheelMsg:
		suffix = 'M'
		switch e.Button {
		case tea.MouseWheelUp:
			button = 64
		case tea.MouseWheelDown:
			button = 65
		default:
			return ""
		}
	case tea.MouseMotionMsg:
		suffix = 'M'
		button = mouseButtonSGR(e.Button) + 32
	default:
		return ""
	}

	return fmt.Sprintf("%c[<%d;%d;%d%c", ansi.ESC, button, col, row, suffix)
}

func mouseButtonSGR(b tea.MouseButton) int {
	switch b {
	case tea.MouseLeft:
		return 0
	case tea.MouseMiddle:
		return 1
	case tea.MouseRight:
		return 2
	default:
		return 0
	}
}

// extractSGRMouseSequences splits a byte buffer into mouse sequences and
// non-mouse chunks. Returns parsed mouse messages and remaining non-mouse bytes.
func extractSGRMouseSequences(buf []byte) (mice []tea.MouseMsg, rest []byte) {
	for len(buf) > 0 {
		idx := bytes.Index(buf, sgrMousePrefix)
		if idx < 0 {
			rest = append(rest, buf...)
			return
		}
		if idx > 0 {
			rest = append(rest, buf[:idx]...)
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
			rest = append(rest, buf...)
			return
		}

		if msg := parseSGRMouse(buf[:end+1]); msg != nil {
			mice = append(mice, msg)
		}
		buf = buf[end+1:]
	}
	return
}

// handleMouse processes mouse events.
// Overlay mouse handling is done by overlay layers above session.
func (s *SessionLayer) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if s.term == nil {
		return nil
	}

	if s.term.ChildWantsMouse() {
		s.term.ForwardMouse(msg)
		return nil
	}

	// Child doesn't want mouse — scroll wheel enters/navigates scrollback
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		if s.term.ScrollbackActive() {
			s.term.HandleScrollbackWheel(wheel.Button)
			return nil
		}
		if wheel.Button == tea.MouseWheelUp {
			s.term.EnterScrollback(3)
			return nil
		}
	}
	return nil
}
