package ui

import (
	"io"
	"os"
	"slices"

	tea "charm.land/bubbletea/v2"
	termlog "termd/frontend/log"
)

// Model is the top-level bubbletea model. It owns the layer stack and
// dispatches messages top-down. All session-specific state lives in
// SessionLayer (the root layer, always layers[0]).
type Model struct {
	layers   []Layer
	Detached bool
}

func NewModel(s *Server, pipeW io.Writer, cmd string, args []string, ring *termlog.LogRingBuffer, endpoint, version, changelog string) Model {
	hostname, _ := os.Hostname()
	session := NewSessionLayer(s, pipeW, cmd, args, ring, endpoint, version, changelog, hostname)
	return Model{
		layers: []Layer{session},
	}
}

func (m Model) Init() tea.Cmd {
	return m.layers[0].(*SessionLayer).Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if push, ok := msg.(PushLayerMsg); ok {
		m.layers = append(m.layers, push.Layer)
		return m, nil
	}

	var cmds []tea.Cmd
	for i := len(m.layers) - 1; i >= 0; i-- {
		resp, cmd, handled := m.layers[i].Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if _, ok := resp.(QuitLayerMsg); ok {
			m.layers = slices.Delete(m.layers, i, i+1)
		}
		if _, ok := resp.(DetachMsg); ok {
			m.Detached = true
		}
		if handled {
			break
		}
	}
	return m, tea.Batch(cmds...)
}

func (m Model) View() tea.View {
	session := m.layers[0].(*SessionLayer)
	v := tea.NewView(session.View(session.termWidth, session.termHeight))
	v.AltScreen = true

	switch session.terminal.MouseMode() {
	case 2:
		v.MouseMode = tea.MouseModeAllMotion
	default:
		v.MouseMode = tea.MouseModeCellMotion
	}

	return v
}
