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

	// anchorTotal is the hscreen.TotalAdded() value at which the current
	// offset was last set (entry, navigation, or previous anchor-advance).
	// Live output arriving during scrollback pushes screen rows into
	// history and advances TotalAdded; offset is interpreted as
	// lines-from-bottom of the virtual buffer, so without compensation
	// the same offset would point at different content frame-to-frame.
	// advanceAnchor bumps offset by the TotalAdded delta on every
	// render, keeping the visible content pinned to what the user
	// scrolled to. Zero until the first View.
	anchorTotal    uint64
	anchorInitted  bool
}

func newScrollbackLayer(term *TerminalLayer, offset int) *ScrollbackLayer {
	return &ScrollbackLayer{
		offset: offset,
		term:   term,
	}
}

func (s *ScrollbackLayer) Activate() tea.Cmd {
	if s.term.hscreen != nil {
		s.anchorTotal = s.term.hscreen.TotalAdded()
		s.anchorInitted = true
	}
	return nil
}
func (s *ScrollbackLayer) Deactivate() { s.term.inScrollback = false }

// advanceAnchor compensates offset for any rows pushed into history
// since the anchor was last captured. Called before any read of offset
// (View + key/wheel handling) so the viewport stays pinned to the
// content the user scrolled to while live output continues. offset==0
// is left alone: at the bottom the user is tracking the live view, not
// a fixed anchor. Returns the current maxOffset for callers that clamp.
func (s *ScrollbackLayer) advanceAnchor() int {
	maxOffset := s.scrollbackTotal()
	if s.term.hscreen == nil {
		return maxOffset
	}
	cur := s.term.hscreen.TotalAdded()
	if !s.anchorInitted {
		s.anchorTotal = cur
		s.anchorInitted = true
		return maxOffset
	}
	if cur > s.anchorTotal && s.offset > 0 {
		delta := cur - s.anchorTotal
		s.offset += int(delta)
		if s.offset > maxOffset {
			s.offset = maxOffset
		}
	}
	s.anchorTotal = cur
	return maxOffset
}

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

	chunk := make([][]te.Cell, len(msg.Lines))
	for j, row := range msg.Lines {
		chunk[j] = protocolCellsToTe(row)
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
	// sequence number.
	//
	// Response covers server rows in seq range
	// [serverTotal-syncBufLen, serverTotal). The client's retained
	// scrollback covers [FirstSeq(), FirstSeq()+Scrollback()).
	//
	// If the client missed events or received them as a snapshot
	// (mode 2026, initial subscribe), its TotalAdded has fallen behind
	// ScrollbackTotal. In that case we discard the event-derived subset
	// — it overlaps the response's range and prepending on top of it
	// would duplicate rows — and adopt the response as the authority.
	// After the reset, the retained range is empty; the subsequent
	// prepend fills it entirely from the response.
	//
	// When events are in sync, only fill the front gap: prepend the
	// portion of the response older than the client's current FirstSeq.
	// Either way, a single PrependHistory call completes the merge.
	responseFirstSeq := int64(msg.ScrollbackTotal) - int64(len(s.syncBuf))
	if s.term.hscreen.TotalAdded() < msg.ScrollbackTotal {
		slog.Debug("scrollback sync: client behind, rebuilding from response",
			"client_total", s.term.hscreen.TotalAdded(),
			"server_total", msg.ScrollbackTotal,
			"client_scrollback", s.term.hscreen.Scrollback(),
			"sync_buf", len(s.syncBuf))
		s.term.hscreen.ResetHistory()
		s.term.hscreen.SetTotalAdded(msg.ScrollbackTotal)
	}
	clientFirstSeq := int64(s.term.hscreen.FirstSeq())
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
		"client_first_seq", clientFirstSeq,
		"client_scrollback", s.term.hscreen.Scrollback(),
		"prepend", prependCount)
	if prependCount > 0 {
		s.term.hscreen.PrependHistory(s.syncBuf[:prependCount])
	}
	s.syncBuf = nil
	s.synced = true
	// Sync reconciliation can jump TotalAdded (client-behind rebuild)
	// without adding rows at the viewport's bottom. Re-anchor so the
	// next advanceAnchor tick doesn't mistake the jump for live output
	// and shift the user's view.
	s.anchorTotal = s.term.hscreen.TotalAdded()
	s.anchorInitted = true
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
	maxOffset := s.advanceAnchor()
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
	maxOffset := s.advanceAnchor()
	switch button {
	case tea.MouseWheelUp:
		s.offset += 3
		if s.offset > maxOffset {
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
	// Keep the viewport pinned to the content the user scrolled to
	// across frames, even as live output pushes rows into history.
	s.advanceAnchor()

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
