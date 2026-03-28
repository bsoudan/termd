package ui

import (
	"io"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// ── Scrollable layer (log viewer, release notes) ────────────────────────────

// ScrollableLayer is a layer that displays scrollable content in a centered
// dialog. Used for the log viewer (with live logRing updates) and release notes.
type ScrollableLayer struct {
	label   string
	vp      viewport.Model
	hScroll int
	pipeW   io.Writer
	logRing *termlog.LogRingBuffer // non-nil for logviewer (live content)
}

func NewScrollableLayer(label, content string, gotoBottom bool, pipeW io.Writer, logRing *termlog.LogRingBuffer, termWidth, termHeight int) *ScrollableLayer {
	h := termHeight * 80 / 100
	if h < 5 {
		h = 5
	}
	vp := viewport.New(viewport.WithWidth(10000), viewport.WithHeight(h-3))
	vp.SetContent(content)
	if gotoBottom {
		vp.GotoBottom()
	}
	return &ScrollableLayer{label: label, vp: vp, pipeW: pipeW, logRing: logRing}
}

func (l *ScrollableLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RawInputMsg:
		return nil, handleFocusModeInput([]byte(msg), l.pipeW), true
	case tea.KeyPressMsg:
		return l.handleKey(msg)
	case tea.MouseMsg:
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			var cmd tea.Cmd
			l.vp, cmd = l.vp.Update(wheel)
			return nil, cmd, true
		}
		return nil, nil, true
	}
	return nil, nil, false // pass through
}

func (l *ScrollableLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "esc":
		return QuitLayerMsg{}, nil, true
	case "left":
		if l.hScroll > 0 {
			l.hScroll--
		}
		return nil, nil, true
	case "right":
		l.hScroll++
		return nil, nil, true
	case "home":
		l.hScroll = 0
		l.vp.GotoTop()
		return nil, nil, true
	default:
		var cmd tea.Cmd
		l.vp, cmd = l.vp.Update(msg)
		return nil, cmd, true
	}
}

func (l *ScrollableLayer) View(width, height int) string { return "" }

func (l *ScrollableLayer) ViewOverlay(base string, width, height int) string {
	// For logviewer, refresh content from logRing on each render
	if l.logRing != nil {
		atBottom := l.vp.AtBottom()
		l.vp.SetContent(l.logRing.String())
		if atBottom {
			l.vp.GotoBottom()
		}
	}
	return renderScrollableOverlay(l.vp.View(), l.hScroll, base, width, height)
}

func (l *ScrollableLayer) Status() (string, bool, bool) { return l.label, true, false }

// ── Status layer ────────────────────────────────────────────────────────────

// StatusLayer displays server and terminal status in a centered dialog.
type StatusLayer struct {
	status *protocol.StatusResponse
	caps   StatusCaps
	pipeW  io.Writer
}

// StatusCaps captures terminal capability data at open time.
type StatusCaps struct {
	Hostname      string
	Endpoint      string
	Version       string
	ConnStatus    string
	KeyboardFlags int
	BgDark        *bool
	TermEnv       map[string]string
	MouseModes    string
}

func NewStatusLayer(caps StatusCaps, pipeW io.Writer) *StatusLayer {
	return &StatusLayer{caps: caps, pipeW: pipeW}
}

func (s *StatusLayer) SetStatus(resp *protocol.StatusResponse) {
	s.status = resp
}

func (s *StatusLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RawInputMsg:
		return nil, handleFocusModeInput([]byte(msg), s.pipeW), true
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "s":
			return QuitLayerMsg{}, nil, true
		}
		return nil, nil, true
	case tea.MouseMsg:
		return nil, nil, true // absorb mouse events
	}
	return nil, nil, false
}

func (s *StatusLayer) View(width, height int) string { return "" }

func (s *StatusLayer) ViewOverlay(base string, width, height int) string {
	return renderStatusOverlay(base, s.caps, s.status, width, height)
}

func (s *StatusLayer) Status() (string, bool, bool) { return "status", true, false }

// ── Help layer ──────────────────────────────────────────────────────────────

// HelpLayer shows available ctrl+b commands and dispatches selections.
type HelpLayer struct {
	cursor  int
	items   []helpItem
	session *SessionLayer
	pipeW   io.Writer
}

func NewHelpLayer(items []helpItem, session *SessionLayer, pipeW io.Writer) *HelpLayer {
	return &HelpLayer{items: items, session: session, pipeW: pipeW}
}

func (h *HelpLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RawInputMsg:
		return nil, handleFocusModeInput([]byte(msg), h.pipeW), true
	case tea.KeyPressMsg:
		return h.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true // absorb mouse events
	}
	return nil, nil, false
}

func (h *HelpLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "esc", "?":
		return QuitLayerMsg{}, nil, true
	case "up", "k":
		if h.cursor > 0 {
			h.cursor--
		}
		return nil, nil, true
	case "down", "j":
		if h.cursor < len(h.items)-1 {
			h.cursor++
		}
		return nil, nil, true
	case "enter":
		action := h.items[h.cursor].action
		resp, cmd := action(h.session)
		if resp == nil {
			resp = QuitLayerMsg{}
		}
		return resp, cmd, true
	default:
		for _, item := range h.items {
			if msg.String() == item.key {
				resp, cmd := item.action(h.session)
				if resp == nil {
					resp = QuitLayerMsg{}
				}
				return resp, cmd, true
			}
		}
		return nil, nil, true
	}
}

func (h *HelpLayer) View(width, height int) string { return "" }

func (h *HelpLayer) ViewOverlay(base string, width, height int) string {
	return renderHelpOverlay(base, h.cursor, h.items, width, height)
}

func (h *HelpLayer) Status() (string, bool, bool) { return "help", true, false }
