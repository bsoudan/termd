package tui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	te "nxtermd/pkg/te"
	"nxtermd/internal/protocol"
)

// ScrollbackLayer is pushed onto the main layer stack when scrollback
// mode is active. It renders the combined scrollback + screen buffer
// from the client's local HistoryScreen and handles navigation input.
// Because the HistoryScreen accumulates lines as terminal events are
// replayed, the scrollback stays in sync with new output that arrives
// while the user is viewing scrollback. Protocol messages that
// ScrollbackLayer doesn't handle (TerminalEvents, ScreenUpdate) pass
// through to SessionLayer/TerminalLayer via the stack.
//
// On entry, a GetScrollbackRequest is sent to the server. When the
// response arrives, any lines the server has that the client doesn't
// (from before the client connected) are prepended to the local history.
type ScrollbackLayer struct {
	offset      int            // lines scrolled back from bottom (0 = live)
	term        *TerminalLayer // reference to the terminal for history + screen
	serverTotal int            // total server scrollback lines (0 until first chunk)
	synced      bool           // true once all server chunks have arrived
	syncBuf     [][]te.Cell    // server lines in oldest-first order (built by prepending newest-first chunks)
}

func newScrollbackLayer(term *TerminalLayer, offset int) *ScrollbackLayer {
	return &ScrollbackLayer{
		offset: offset,
		term:   term,
	}
}

func (s *ScrollbackLayer) Activate() tea.Cmd { return nil }
func (s *ScrollbackLayer) Deactivate()       { s.term.inScrollback = false }

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
		if !s.synced {
			s.handleSyncChunk(msg)
		}
		return nil, nil, true
	}
	return nil, nil, false // pass through (terminal events, etc.)
}

// handleSyncChunk accumulates server scrollback chunks. Chunks arrive
// newest-first so the lines closest to the user's view load first.
// Each chunk is prepended to syncBuf so it stays in oldest-first order.
// While chunks are streaming, the user can scroll — unloaded regions
// render as x-markers.
func (s *ScrollbackLayer) handleSyncChunk(msg protocol.GetScrollbackResponse) {
	if msg.Error {
		slog.Debug("scrollback sync error", "message", msg.Message)
		s.synced = true
		return
	}

	s.serverTotal = msg.Total
	slog.Debug("scrollback sync chunk", "lines", len(msg.Lines), "total", msg.Total, "done", msg.Done, "buf_so_far", len(s.syncBuf))

	// Convert protocol cells to te.Cell.
	chunk := make([][]te.Cell, len(msg.Lines))
	for j, row := range msg.Lines {
		cells := make([]te.Cell, len(row))
		for i, c := range row {
			cells[i].Data = c.Char
			cells[i].Attr.Fg = specToColor(c.Fg)
			cells[i].Attr.Bg = specToColor(c.Bg)
			cells[i].Attr.Bold = c.A&1 != 0
			cells[i].Attr.Italics = c.A&2 != 0
			cells[i].Attr.Underline = c.A&4 != 0
			cells[i].Attr.Strikethrough = c.A&8 != 0
			cells[i].Attr.Reverse = c.A&16 != 0
			cells[i].Attr.Blink = c.A&32 != 0
			cells[i].Attr.Conceal = c.A&64 != 0
		}
		chunk[j] = cells
	}
	// Prepend: chunks arrive newest-first, so each new chunk is older
	// and goes in front to maintain oldest-first order in syncBuf.
	combined := make([][]te.Cell, 0, len(chunk)+len(s.syncBuf))
	combined = append(combined, chunk...)
	combined = append(combined, s.syncBuf...)
	s.syncBuf = combined

	if !msg.Done {
		return
	}

	// All chunks received. Reconcile with local hscreen by absolute
	// sequence number rather than buffer length.
	//
	// Two regimes based on whether the client's monotonic row counter
	// has caught up to the server's.
	//
	// Events-in-sync (clientTotal == serverTotal): client has replayed
	// every row the server has added. The response covers server rows
	// in seq range [serverTotal - syncBuf, serverTotal); the client's
	// scrollback covers [clientTotal - Scrollback(), clientTotal).
	// Prepend only rows older than the client's oldest —
	// [responseFirstSeq, clientFirstSeq). When the ranges coincide the
	// prefix is empty, so no duplication from the race.
	//
	// Client-behind (clientTotal < serverTotal): the server delivered
	// output as a screen snapshot (bulk output, mode 2026, or initial
	// subscribe) rather than individual LF events, so the client's
	// TotalAdded counter never advanced. The response IS the
	// authoritative scrollback; prepend it all and adopt the server's
	// counter. Any existing local scrollback (from a prior reconnect
	// preserve) is older-seq than the response and stays on top.
	if s.term.hscreen.TotalAdded() < msg.ScrollbackTotal {
		slog.Debug("scrollback sync: client behind, rebuilding from response",
			"client_total", s.term.hscreen.TotalAdded(),
			"server_total", msg.ScrollbackTotal,
			"sync_buf", len(s.syncBuf))
		s.term.hscreen.SetTotalAdded(msg.ScrollbackTotal)
		if len(s.syncBuf) > 0 {
			s.term.hscreen.PrependHistory(s.syncBuf)
		}
	} else {
		responseFirstSeq := int64(msg.ScrollbackTotal) - int64(len(s.syncBuf))
		clientFirstSeq := int64(s.term.hscreen.TotalAdded()) - int64(s.term.hscreen.Scrollback())
		prependCount := int(clientFirstSeq - responseFirstSeq)
		if prependCount < 0 {
			prependCount = 0
		}
		if prependCount > len(s.syncBuf) {
			prependCount = len(s.syncBuf)
		}
		slog.Debug("scrollback sync complete",
			"server_total", msg.ScrollbackTotal,
			"sync_buf", len(s.syncBuf),
			"response_first_seq", responseFirstSeq,
			"client_total", s.term.hscreen.TotalAdded(),
			"client_scrollback", s.term.hscreen.Scrollback(),
			"client_first_seq", clientFirstSeq,
			"prepend", prependCount)
		if prependCount > 0 {
			s.term.hscreen.PrependHistory(s.syncBuf[:prependCount])
		}
	}
	s.syncBuf = nil
	s.synced = true
}

// scrollbackTotal returns the best-known total scrollback line count.
// While syncing, this uses the server-reported total so the user can
// scroll the full extent. After sync completes, only the local history
// count is used — any difference vs serverTotal is due to capacity
// limits, not data that might still load.
func (s *ScrollbackLayer) scrollbackTotal() int {
	local := len(s.term.ScrollbackLines())
	if !s.synced && s.serverTotal > local {
		return s.serverTotal
	}
	return local
}

func (s *ScrollbackLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	maxOffset := s.scrollbackTotal()
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
		if s.offset <= 0 {
			return QuitLayerMsg{}, nil, true
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
		if maxOffset := s.scrollbackTotal(); s.offset > maxOffset {
			s.offset = maxOffset
		}
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
	// Render only within the terminal viewport so the tab bar (row 0)
	// and the status-bar margin row(s) below it remain visible.
	termY := height - s.term.contentHeight()
	if termY < 0 {
		termY = 0
	}
	height = s.term.contentHeight()

	history := s.term.ScrollbackLines()
	screenCells := s.term.ScreenCells()
	syncBufLen := len(s.syncBuf)

	// Layout: gap (unloaded oldest) | syncBuf (loaded) | local history | screen
	// Chunks arrive newest-first, so syncBuf fills from the bottom up:
	// the most recent server lines (adjacent to local history) load
	// first, and the gap at the top shrinks as older chunks stream in.
	// After sync completes, syncBuf is cleared and all loadable lines
	// are in local history — gap is 0.
	localLen := len(history)
	gapLen := 0
	if !s.synced && s.serverTotal > syncBufLen+localLen {
		gapLen = s.serverTotal - syncBufLen - localLen
	}
	totalLines := gapLen + syncBufLen + localLen + len(screenCells)
	totalScrollback := s.scrollbackTotal()

	offset := s.offset
	if offset > totalScrollback {
		offset = totalScrollback
	}

	startIdx := totalLines - height - offset
	if startIdx < 0 {
		startIdx = 0
	}

	// Region boundaries: gap | sync | local history | screen.
	gapEnd := gapLen
	syncEnd := gapEnd + syncBufLen
	histEnd := syncEnd + localLen

	// Content layer.
	var sb strings.Builder
	for i := range height {
		idx := startIdx + i
		if idx < gapEnd {
			// Unloaded gap: x markers with spacing.
			for col := range width {
				if idx%2 == 0 && col%2 == 0 {
					sb.WriteByte('x')
				} else {
					sb.WriteByte(' ')
				}
			}
		} else if idx < syncEnd {
			// Loaded server scrollback (oldest-first in syncBuf).
			renderCellLine(&sb, s.syncBuf[idx-gapEnd], width, i, -1, -1, false, false, false)
		} else if idx < histEnd {
			// Local history (from terminal events).
			renderCellLine(&sb, history[idx-syncEnd], width, i, -1, -1, false, false, false)
		} else {
			// Screen.
			screenIdx := idx - histEnd
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

	if totalLines == 0 {
		return []*lipgloss.Layer{lipgloss.NewLayer(sb.String()).Y(termY)}
	}

	// Scrollbar layer.
	thumbStart, thumbLen := scrollbarGeometry(height, totalLines, offset)
	thumbEnd := thumbStart + thumbLen
	atTop := offset >= totalScrollback
	atBottom := offset <= 0

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
			// Show dots for loaded regions, spaces for gap.
			contentPos := i * totalLines / height
			if contentPos >= gapEnd {
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
		lipgloss.NewLayer(sb.String()).Y(termY),
		lipgloss.NewLayer(bar.String()).X(width - 1).Y(termY).Z(1),
	}
}

func (s *ScrollbackLayer) WantsKeyboardInput() bool { return true }

func (s *ScrollbackLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	total := s.scrollbackTotal()
	offset := s.offset
	if offset > total {
		offset = total
	}
	return fmt.Sprintf("scrollback [%d/%d]", offset, total), scrollbackStatusStyle
}
