package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	te "github.com/rcarmo/go-te/pkg/te"
	"termd/frontend/protocol"
)

// protocolCellsToTe converts protocol ScreenCell data to te.Cell for rendering.
func protocolCellsToTe(cells []protocol.ScreenCell) []te.Cell {
	row := make([]te.Cell, len(cells))
	for i, c := range cells {
		row[i].Data = c.Char
		row[i].Attr.Fg = specToColor(c.Fg)
		row[i].Attr.Bg = specToColor(c.Bg)
		row[i].Attr.Bold = c.A&1 != 0
		row[i].Attr.Italics = c.A&2 != 0
		row[i].Attr.Underline = c.A&4 != 0
		row[i].Attr.Strikethrough = c.A&8 != 0
		row[i].Attr.Reverse = c.A&16 != 0
		row[i].Attr.Blink = c.A&32 != 0
		row[i].Attr.Conceal = c.A&64 != 0
	}
	return row
}

// Scrollback manages the scrollback navigation state.
type Scrollback struct {
	active bool
	offset int                    // lines scrolled back from bottom (0 = live)
	cells  [][]protocol.ScreenCell // server-side scrollback buffer
}

// Active returns whether scrollback mode is active.
func (s Scrollback) Active() bool { return s.active }

// Enter activates scrollback mode with an initial offset.
func (s Scrollback) Enter(offset int) Scrollback {
	s.active = true
	s.offset = offset
	return s
}

// Exit deactivates scrollback mode and clears data.
func (s Scrollback) Exit() Scrollback {
	return Scrollback{}
}

// SetData stores the scrollback cell data from the server.
func (s Scrollback) SetData(cells [][]protocol.ScreenCell) Scrollback {
	s.cells = cells
	return s
}

// StatusText returns the tab bar status string.
func (s Scrollback) StatusText() string {
	offset := s.offset
	total := len(s.cells)
	if offset > total {
		offset = total
	}
	return fmt.Sprintf("scrollback [%d/%d]", offset, total)
}

// Update handles keyboard navigation in scrollback mode.
func (s Scrollback) Update(msg tea.KeyPressMsg, contentHeight int) (Scrollback, bool) {
	maxOffset := len(s.cells)
	halfPage := contentHeight / 2
	if halfPage < 1 {
		halfPage = 1
	}

	switch msg.String() {
	case "q", "esc":
		return s.Exit(), true
	case "up", "k":
		if s.offset < maxOffset {
			s.offset++
		}
	case "down", "j":
		if s.offset > 0 {
			s.offset--
		}
	case "pgup", "ctrl+u":
		s.offset += halfPage
		if s.offset > maxOffset {
			s.offset = maxOffset
		}
	case "pgdown", "ctrl+d":
		s.offset -= halfPage
		if s.offset < 0 {
			s.offset = 0
		}
	case "home", "g":
		s.offset = maxOffset
	case "end", "G":
		s.offset = 0
	default:
		return s.Exit(), true
	}
	return s, false
}

// HandleWheel adjusts the offset for scroll wheel events.
// Returns the updated scrollback and whether it should exit.
func (s Scrollback) HandleWheel(button tea.MouseButton) (Scrollback, bool) {
	switch button {
	case tea.MouseWheelUp:
		s.offset += 3
	case tea.MouseWheelDown:
		s.offset -= 3
		if s.offset <= 0 {
			return s.Exit(), true
		}
	}
	return s, false
}

// View renders the combined scrollback + screen buffer.
func (s Scrollback) View(sb *strings.Builder, screenCells [][]te.Cell, width, height int) {
	// Clamp offset to available scrollback
	offset := s.offset
	if offset > len(s.cells) {
		offset = len(s.cells)
	}

	totalLines := len(s.cells) + len(screenCells)
	startIdx := totalLines - height - offset
	if startIdx < 0 {
		startIdx = 0
	}

	for i := range height {
		idx := startIdx + i
		var row []te.Cell
		if idx < len(s.cells) {
			row = protocolCellsToTe(s.cells[idx])
		} else {
			screenIdx := idx - len(s.cells)
			if screenIdx >= 0 && screenIdx < len(screenCells) {
				row = screenCells[screenIdx]
			}
		}
		renderCellLine(sb, row, width, i, -1, -1, false, false)
		if i < height-1 {
			sb.WriteByte('\n')
		}
	}
}
