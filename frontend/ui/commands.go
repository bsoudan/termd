package ui

import (
	"encoding/base64"
	"strings"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/protocol"
)

// handlePrefixKey handles a single key byte after ctrl+b.
func (m Model) handlePrefixKey(key byte) (tea.Model, tea.Cmd) {
	return m.handlePrefixCommand(key)
}

// sendRawToServer forwards raw bytes as input to the active region.
func (m Model) sendRawToServer(raw []byte) {
	if m.regionID == "" || len(raw) == 0 {
		return
	}
	m.server.Send(InputMsg{
		RegionID: m.regionID,
		Data:     raw,
	})
}

func (m Model) handlePrefixCommand(key byte) (tea.Model, tea.Cmd) {
	m.prefixMode = false
	switch key {
	case 'd':
		m.Detached = true
		return m.quit()
	case prefixKey: // ctrl+b ctrl+b → send literal ctrl+b
		m.sendRawToServer([]byte{prefixKey})
		return m, nil
	case 'l':
		m.overlay = NewScrollableOverlay("logviewer", m.LogRing.String(), true, m.termWidth, m.termHeight)
		return m, nil
	case '?':
		m.overlay = NewHelpOverlay(helpItems)
		return m, nil
	case 's':
		so := NewStatusOverlay(m.buildStatusCaps())
		m.overlay = so
		m.request(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				so.SetStatus(resp)
			}
		})
		return m, nil
	case 'n':
		m.overlay = NewScrollableOverlay("release notes", strings.TrimRight(m.Changelog, "\n"), false, m.termWidth, m.termHeight)
		return m, nil
	case '[':
		if m.regionID != "" {
			m.scrollback = m.scrollback.Enter(0)
			m.server.Send(protocol.GetScrollbackRequest{RegionID: m.regionID})
		}
		return m, nil
	case 'r':
		if m.regionID != "" {
			m.terminal = m.terminal.SetPendingClear()
			m.server.Send(protocol.GetScreenRequest{
				RegionID: m.regionID,
			})
		}
		return m, nil
	default:
		return m, nil
	}
}

type helpItem struct {
	key    string
	label  string
	action func(m Model) (Model, tea.Cmd)
}

var helpItems = []helpItem{
	{"d", "detach", func(m Model) (Model, tea.Cmd) {
		m.Detached = true
		model, cmd := m.quit()
		return model.(Model), cmd
	}},
	{"l", "log viewer", func(m Model) (Model, tea.Cmd) {
		m.overlay = NewScrollableOverlay("logviewer", m.LogRing.String(), true, m.termWidth, m.termHeight)
		return m, nil
	}},
	{"s", "status", func(m Model) (Model, tea.Cmd) {
		so := NewStatusOverlay(m.buildStatusCaps())
		m.overlay = so
		m.request(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				so.SetStatus(resp)
			}
		})
		return m, nil
	}},
	{"n", "release notes", func(m Model) (Model, tea.Cmd) {
		m.overlay = NewScrollableOverlay("release notes", strings.TrimRight(m.Changelog, "\n"), false, m.termWidth, m.termHeight)
		return m, nil
	}},
	{"r", "refresh screen", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			m.terminal = m.terminal.SetPendingClear()
			m.server.Send(protocol.GetScreenRequest{
				RegionID: m.regionID,
			})
			return m, nil
		}
		return m, nil
	}},
	{"[", "scrollback", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			m.scrollback = m.scrollback.Enter(0)
			m.server.Send(protocol.GetScrollbackRequest{RegionID: m.regionID})
		}
		return m, nil
	}},
	{"b", "send literal ctrl+b", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			data := base64.StdEncoding.EncodeToString([]byte{0x02})
			m.server.Send(protocol.InputMsg{
				RegionID: m.regionID, Data: data,
			})
		}
		return m, nil
	}},
}

// handleMouse processes mouse events. If an overlay is active, route to it.
// If the child app has mouse mode enabled, forward to the server.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Overlays get scroll wheel events while they're visible
	if m.overlay != nil {
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			var cmd tea.Cmd
			m.overlay, cmd = m.overlay.HandleWheel(wheel)
			return m, cmd
		}
		return m, nil
	}

	mouse := msg.Mouse()

	if m.terminal.ChildWantsMouse() && m.regionID != "" {
		seq := encodeSGRMouse(msg, mouse.X, mouse.Y-1)
		if seq != "" {
			data := base64.StdEncoding.EncodeToString([]byte(seq))
			m.server.Send(protocol.InputMsg{
				RegionID: m.regionID, Data: data,
			})
		}
		return m, nil
	}

	// Child doesn't want mouse — scroll wheel enters/navigates scrollback
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		if m.scrollback.Active() {
			var exited bool
			m.scrollback, exited = m.scrollback.HandleWheel(wheel.Button)
			if exited {
				m.scrollback = m.scrollback.Exit()
			}
			return m, nil
		}
		if wheel.Button == tea.MouseWheelUp && m.regionID != "" {
			m.scrollback = m.scrollback.Enter(3)
			return m, func() tea.Msg {
				m.server.Send(protocol.GetScrollbackRequest{RegionID: m.regionID})
				return nil
			}
		}
	}
	return m, nil
}
