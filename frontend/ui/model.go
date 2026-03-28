package ui

import (
	"io"
	"os"
	"slices"

	tea "charm.land/bubbletea/v2"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// Model is the top-level bubbletea model. It owns the layer stack and
// dispatches messages top-down. Protocol message unwrapping and req_id
// matching happen here so all layers see unwrapped payloads.
type Model struct {
	layers   []Layer
	req      *requestState
	Detached bool
}

func NewModel(s *Server, pipeW io.Writer, cmd string, args []string, ring *termlog.LogRingBuffer, endpoint, version, changelog string) Model {
	hostname, _ := os.Hostname()
	req := &requestState{pending: make(map[uint64]ReplyFunc)}
	requestFn := func(msg any, reply ReplyFunc) {
		req.nextReqID++
		req.pending[req.nextReqID] = reply
		s.Send(protocol.TaggedWithReqID(msg, req.nextReqID))
	}
	session := NewSessionLayer(s, pipeW, requestFn, cmd, args, ring, endpoint, version, changelog, hostname)
	return Model{
		layers: []Layer{session},
		req:    req,
	}
}

func (m Model) Init() tea.Cmd {
	return m.layers[0].(*SessionLayer).Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Unwrap protocol.Message: match req_id, then dispatch payload.
	if pmsg, ok := msg.(protocol.Message); ok {
		if pmsg.ReqID > 0 {
			if reply, ok := m.req.pending[pmsg.ReqID]; ok {
				delete(m.req.pending, pmsg.ReqID)
				reply(pmsg.Payload)
				return m, nil
			}
		}
		msg = pmsg.Payload
	}

	if push, ok := msg.(PushLayerMsg); ok {
		m.layers = append(m.layers, push.Layer)
		return m, nil
	}

	// Tell session whether focus mode is needed (overlay layer active).
	// CommandLayer and HintLayer are transparent — they don't need focus mode.
	session := m.layers[0].(*SessionLayer)
	session.focusMode = false
	for i := 1; i < len(m.layers); i++ {
		if _, ok := m.layers[i].(OverlayViewer); ok {
			session.focusMode = true
			break
		}
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

	// Collect topmost non-empty Status from layers above session.
	layerStatus, layerBold, layerRed := "", false, false
	hasOverlay := false
	for i := len(m.layers) - 1; i > 0; i-- {
		if _, ok := m.layers[i].(OverlayViewer); ok {
			hasOverlay = true
		}
		t, b, r := m.layers[i].Status()
		if t != "" && layerStatus == "" {
			layerStatus, layerBold, layerRed = t, b, r
		}
	}

	base := session.ViewWithStatus(layerStatus, layerBold, layerRed, hasOverlay)

	// Composite overlay layers on top of the base view.
	width, height := session.termWidth, session.termHeight
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	for i := 1; i < len(m.layers); i++ {
		if ov, ok := m.layers[i].(OverlayViewer); ok {
			base = ov.ViewOverlay(base, width, height)
		}
	}

	v := tea.NewView(base)
	v.AltScreen = true

	if session.term != nil {
		switch session.term.MouseMode() {
		case 2:
			v.MouseMode = tea.MouseModeAllMotion
		default:
			v.MouseMode = tea.MouseModeCellMotion
		}
	}

	return v
}
