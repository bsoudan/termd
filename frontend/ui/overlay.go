package ui

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"termd/frontend/protocol"
)

// Overlay is the interface for all overlay dialogs.
type Overlay interface {
	// Update handles a key press. Returns the updated overlay (nil to close),
	// an optional action to execute on the session, and an optional tea.Cmd.
	// The action returns (response tea.Msg, cmd tea.Cmd) to allow signaling
	// DetachMsg or other responses back through the layer stack.
	Update(tea.KeyPressMsg) (Overlay, func(*SessionLayer) (tea.Msg, tea.Cmd), tea.Cmd)

	// HandleWheel handles scroll wheel events.
	HandleWheel(tea.MouseWheelMsg) (Overlay, tea.Cmd)

	// View renders the overlay composited over the base screen.
	View(base string, width, height int) string

	// Label returns the tab bar label.
	Label() string
}

// ── Scrollable overlay (log viewer, changelog) ──────────────────────────────

type ScrollableOverlay struct {
	label   string
	vp      viewport.Model
	hScroll int
}

func NewScrollableOverlay(label, content string, gotoBottom bool, termWidth, termHeight int) *ScrollableOverlay {
	h := termHeight * 80 / 100
	if h < 5 {
		h = 5
	}
	vp := viewport.New(viewport.WithWidth(10000), viewport.WithHeight(h-3))
	vp.SetContent(content)
	if gotoBottom {
		vp.GotoBottom()
	}
	return &ScrollableOverlay{label: label, vp: vp}
}

func (o *ScrollableOverlay) Label() string { return o.label }

func (o *ScrollableOverlay) Update(msg tea.KeyPressMsg) (Overlay, func(*SessionLayer) (tea.Msg, tea.Cmd), tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return nil, nil, nil
	case "left":
		if o.hScroll > 0 {
			o.hScroll--
		}
		return o, nil, nil
	case "right":
		o.hScroll++
		return o, nil, nil
	case "home":
		o.hScroll = 0
		o.vp.GotoTop()
		return o, nil, nil
	default:
		var cmd tea.Cmd
		o.vp, cmd = o.vp.Update(msg)
		return o, nil, cmd
	}
}

func (o *ScrollableOverlay) HandleWheel(msg tea.MouseWheelMsg) (Overlay, tea.Cmd) {
	var cmd tea.Cmd
	o.vp, cmd = o.vp.Update(msg)
	return o, cmd
}

func (o *ScrollableOverlay) View(base string, width, height int) string {
	return renderScrollableOverlay(o.vp.View(), o.hScroll, base, width, height)
}

// RefreshContent updates the viewport content (for live log updates).
func (o *ScrollableOverlay) RefreshContent(content string) {
	atBottom := o.vp.AtBottom()
	o.vp.SetContent(content)
	if atBottom {
		o.vp.GotoBottom()
	}
}

// ── Status overlay ──────────────────────────────────────────────────────────

type StatusOverlay struct {
	status *protocol.StatusResponse
	caps   StatusCaps
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

func NewStatusOverlay(caps StatusCaps) *StatusOverlay {
	return &StatusOverlay{caps: caps}
}

func (o *StatusOverlay) SetStatus(resp *protocol.StatusResponse) {
	o.status = resp
}

func (o *StatusOverlay) Label() string { return "status" }

func (o *StatusOverlay) Update(msg tea.KeyPressMsg) (Overlay, func(*SessionLayer) (tea.Msg, tea.Cmd), tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "s":
		return nil, nil, nil
	}
	return o, nil, nil
}

func (o *StatusOverlay) HandleWheel(tea.MouseWheelMsg) (Overlay, tea.Cmd) {
	return o, nil
}

func (o *StatusOverlay) View(base string, width, height int) string {
	return renderStatusOverlay(base, o.caps, o.status, width, height)
}

// ── Help overlay ────────────────────────────────────────────────────────────

type HelpOverlay struct {
	cursor int
	items  []helpItem
}

func NewHelpOverlay(items []helpItem) *HelpOverlay {
	return &HelpOverlay{items: items}
}

func (o *HelpOverlay) Label() string { return "help" }

func (o *HelpOverlay) Update(msg tea.KeyPressMsg) (Overlay, func(*SessionLayer) (tea.Msg, tea.Cmd), tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "?":
		return nil, nil, nil
	case "up", "k":
		if o.cursor > 0 {
			o.cursor--
		}
		return o, nil, nil
	case "down", "j":
		if o.cursor < len(o.items)-1 {
			o.cursor++
		}
		return o, nil, nil
	case "enter":
		action := o.items[o.cursor].action
		return nil, action, nil
	default:
		for _, item := range o.items {
			if msg.String() == item.key {
				return nil, item.action, nil
			}
		}
		return o, nil, nil
	}
}

func (o *HelpOverlay) HandleWheel(tea.MouseWheelMsg) (Overlay, tea.Cmd) {
	return o, nil
}

func (o *HelpOverlay) View(base string, width, height int) string {
	return renderHelpOverlay(base, o.cursor, o.items, width, height)
}
