package ui

import tea "charm.land/bubbletea/v2"

// CommandLayer is a temporary layer pushed when ctrl+b is detected.
// It captures the next RawInputMsg byte, dispatches the command via
// session, and pops itself.
type CommandLayer struct {
	session *SessionLayer
}

func NewCommandLayer(session *SessionLayer) *CommandLayer {
	return &CommandLayer{session: session}
}

func (c *CommandLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	raw, ok := msg.(RawInputMsg)
	if !ok {
		return nil, nil, false // pass through to layers below
	}

	key := raw[0]
	rest := raw[1:]
	resp, cmd := c.session.handlePrefixCommand(key)
	if len(rest) > 0 {
		c.session.sendRawToServer(rest)
	}

	// Pop self unless detaching (DetachMsg + tea.Quit handles exit)
	if resp == nil {
		resp = QuitLayerMsg{}
	}
	return resp, cmd, true
}

func (c *CommandLayer) View(width, height int) string { return "" }
func (c *CommandLayer) Status() (string, bool, bool)  { return "?", true, false }

// HintLayer is a temporary layer pushed after startup to show
// "ctrl+b ? for help" in the status bar. It pops itself on hideHintMsg.
type HintLayer struct{}

func (h *HintLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	if _, ok := msg.(hideHintMsg); ok {
		return QuitLayerMsg{}, nil, true
	}
	return nil, nil, false // pass through
}

func (h *HintLayer) View(width, height int) string { return "" }
func (h *HintLayer) Status() (string, bool, bool)  { return "ctrl+b ? for help", true, false }
