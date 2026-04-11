package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	te "nxtermd/pkg/te"
	"nxtermd/internal/protocol"
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

// ScrollbackLayer is a layer pushed onto TerminalLayer's inner stack
// when scrollback mode is active. It renders the combined scrollback +
// screen buffer and handles navigation input.
type ScrollbackLayer struct {
	offset int                    // lines scrolled back from bottom (0 = live)
	cells  [][]protocol.ScreenCell // server-side scrollback buffer (accumulated chunks)
	total  int                    // total scrollback lines reported by server
	loaded bool                   // true once all chunks have arrived
	term   *TerminalLayer         // reference to the terminal for screen cells and dimensions
}

func newScrollbackLayer(term *TerminalLayer, offset int) *ScrollbackLayer {
	return &ScrollbackLayer{
		offset: offset,
		term:   term,
	}
}

func (s *ScrollbackLayer) Activate() tea.Cmd { return nil }
func (s *ScrollbackLayer) Deactivate()       {}

func (s *ScrollbackLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	case tea.MouseMsg:
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			return s.handleWheel(wheel.Button)
		}
		return nil, nil, true // absorb other mouse events
	case protocol.GetScrollbackResponse:
		s.cells = append(s.cells, msg.Lines...)
		s.total = msg.Total
		s.loaded = msg.Done
		return nil, nil, true
	}
	return nil, nil, false // pass through (terminal events, etc.)
}

func (s *ScrollbackLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	maxOffset := s.total
	halfPage := s.term.contentHeight() / 2
	if halfPage < 1 {
		halfPage = 1
	}

	switch msg.String() {
	case "q", "esc":
		return QuitLayerMsg{}, nil, true
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
		// Any unrecognized key exits scrollback.
		return QuitLayerMsg{}, nil, true
	}
	return nil, nil, true
}

func (s *ScrollbackLayer) handleWheel(button tea.MouseButton) (tea.Msg, tea.Cmd, bool) {
	switch button {
	case tea.MouseWheelUp:
		s.offset += 3
	case tea.MouseWheelDown:
		s.offset -= 3
		if s.offset <= 0 {
			return QuitLayerMsg{}, nil, true
		}
	}
	return nil, nil, true
}

// Scrollbar characters.
const (
	scrollbarTrack   = "·"
	scrollbarThumb   = "█"
	scrollbarCapTop  = "▲"
	scrollbarCapBot  = "▼"
)

var (
	scrollbarTrackStyle    = lipgloss.NewStyle().Faint(true)
	scrollbarThumbStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightCyan)
	scrollbackStatusStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightCyan)
)

// scrollbarGeometry computes the thumb position and size for a scrollbar.
// Returns thumbStart (first row of thumb) and thumbLen (number of rows).
func scrollbarGeometry(height, totalLines, offset int) (thumbStart, thumbLen int) {
	if totalLines <= height {
		return 0, height
	}
	thumbLen = max(height*height/totalLines, 3) // at least cap + body + cap
	// offset=0 means bottom (viewport at end), offset=max means top.
	maxOffset := totalLines - height
	scrollFrac := 1.0
	if maxOffset > 0 {
		scrollFrac = 1.0 - float64(offset)/float64(maxOffset)
	}
	thumbStart = int(scrollFrac * float64(height-thumbLen))
	thumbStart = max(min(thumbStart, height-thumbLen), 0)
	return thumbStart, thumbLen
}

func (s *ScrollbackLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	screenCells := s.term.ScreenCells()

	offset := s.offset
	if offset > s.total {
		offset = s.total
	}

	// Full content layout: loaded scrollback | gap | screen.
	// Gap rows are not-yet-loaded scrollback lines.
	loadedEnd := len(s.cells)
	gapEnd := s.total
	totalLines := s.total + len(screenCells)
	startIdx := totalLines - height - offset
	if startIdx < 0 {
		startIdx = 0
	}

	// Content layer — full width terminal content.
	var sb strings.Builder
	for i := range height {
		idx := startIdx + i
		if idx < loadedEnd {
			row := protocolCellsToTe(s.cells[idx])
			renderCellLine(&sb, row, width, i, -1, -1, false, false, false)
		} else if idx < gapEnd {
			// Not-yet-loaded: alternating x marks with row/column spacing.
			for col := range width {
				if idx%2 == 0 && col%2 == 0 {
					sb.WriteByte('x')
				} else {
					sb.WriteByte(' ')
				}
			}
		} else {
			screenIdx := idx - gapEnd
			var row []te.Cell
			if screenIdx >= 0 && screenIdx < len(screenCells) {
				row = screenCells[screenIdx]
			}
			renderCellLine(&sb, row, width, i, -1, -1, false, false, false)
		}
		if i < height-1 {
			sb.WriteByte('\n')
		}
	}

	// Don't render the scrollbar until the first chunk arrives.
	if s.total == 0 && len(s.cells) == 0 {
		return []*lipgloss.Layer{lipgloss.NewLayer(sb.String())}
	}

	// Use server-reported total for scrollbar so it reflects the full
	// extent even while chunks are still streaming.
	scrollbarTotal := s.total + len(screenCells)

	// Scrollbar layer — single column overlaid on the right edge.
	// Track uses microdots for loaded content and spaces for the gap
	// where chunks haven't arrived yet.
	thumbStart, thumbLen := scrollbarGeometry(height, scrollbarTotal, offset)
	thumbEnd := thumbStart + thumbLen
	atTop := offset >= s.total
	atBottom := offset <= 0

	// Map scrollbar rows to content regions for track styling.
	// Reuses loadedEnd/gapEnd from above.

	var bar strings.Builder
	for i := range height {
		if i >= thumbStart && i < thumbEnd {
			if i == thumbStart && !atTop {
				bar.WriteString(scrollbarThumbStyle.Render(scrollbarCapTop))
			} else if i == thumbEnd-1 && !atBottom {
				bar.WriteString(scrollbarThumbStyle.Render(scrollbarCapBot))
			} else {
				bar.WriteString(scrollbarThumbStyle.Render(scrollbarThumb))
			}
		} else {
			// Map scrollbar row to content position.
			contentPos := i * scrollbarTotal / height
			if contentPos < loadedEnd || contentPos >= gapEnd {
				bar.WriteString(scrollbarTrackStyle.Render(scrollbarTrack))
			} else {
				bar.WriteByte(' ')
			}
		}
		if i < height-1 {
			bar.WriteByte('\n')
		}
	}

	return []*lipgloss.Layer{
		lipgloss.NewLayer(sb.String()),
		lipgloss.NewLayer(bar.String()).X(width - 1).Z(1),
	}
}

func (s *ScrollbackLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }

func (s *ScrollbackLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	if s.total == 0 && len(s.cells) == 0 {
		return "scrollback [...]", scrollbackStatusStyle
	}
	total := s.total
	offset := s.offset
	if offset > total {
		offset = total
	}
	return fmt.Sprintf("scrollback [%d/%d]", offset, total), scrollbackStatusStyle
}
