package ui

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	termlog "termd/frontend/log"
)

// ScrollableLayer is a layer that displays scrollable content in a centered
// dialog. Used for the log viewer (with live logRing updates) and release notes.
type ScrollableLayer struct {
	label   string
	vp      viewport.Model
	hScroll int
	logRing *termlog.LogRingBuffer // non-nil for logviewer (live content)
}

func NewScrollableLayer(label, content string, gotoBottom bool, logRing *termlog.LogRingBuffer, termWidth, termHeight int) *ScrollableLayer {
	h := termHeight * 80 / 100
	if h < 5 {
		h = 5
	}
	vp := viewport.New(viewport.WithWidth(10000), viewport.WithHeight(h-3))
	vp.SetContent(content)
	if gotoBottom {
		vp.GotoBottom()
	}
	return &ScrollableLayer{label: label, vp: vp, logRing: logRing}
}

func (l *ScrollableLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
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

