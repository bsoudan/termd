package ui

import (
	"encoding/base64"
	"strings"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/protocol"
)

// sendRawToServer forwards raw bytes as input to the active region.
func (s *SessionLayer) sendRawToServer(raw []byte) {
	if s.regionID == "" || len(raw) == 0 {
		return
	}
	s.server.Send(InputMsg{
		RegionID: s.regionID,
		Data:     raw,
	})
}

func (s *SessionLayer) handlePrefixCommand(key byte) (tea.Msg, tea.Cmd) {
	switch key {
	case 'd':
		return s.detach()
	case prefixKey: // ctrl+b ctrl+b → send literal ctrl+b
		s.sendRawToServer([]byte{prefixKey})
		return nil, nil
	case 'l':
		layer := NewScrollableLayer("logviewer", s.logRing.String(), true, s.pipeW, s.logRing, s.termWidth, s.termHeight)
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }
	case '?':
		layer := NewHelpLayer(helpItems, s, s.pipeW)
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }
	case 's':
		layer := NewStatusLayer(s.buildStatusCaps(), s.pipeW)
		s.requestFn(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				layer.SetStatus(resp)
			}
		})
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }
	case 'n':
		layer := NewScrollableLayer("release notes", strings.TrimRight(s.changelog, "\n"), false, s.pipeW, nil, s.termWidth, s.termHeight)
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }
	case '[':
		if s.term != nil {
			s.term.EnterScrollback(0)
		}
		return nil, nil
	case 'r':
		if s.term != nil {
			s.term.SetPendingClear()
			s.server.Send(protocol.GetScreenRequest{
				RegionID: s.term.RegionID(),
			})
		}
		return nil, nil
	default:
		return nil, nil
	}
}

type helpItem struct {
	key    string
	label  string
	action func(s *SessionLayer) (tea.Msg, tea.Cmd)
}

var helpItems = []helpItem{
	{"d", "detach", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		return s.detach()
	}},
	{"l", "log viewer", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		layer := NewScrollableLayer("logviewer", s.logRing.String(), true, s.pipeW, s.logRing, s.termWidth, s.termHeight)
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }
	}},
	{"s", "status", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		layer := NewStatusLayer(s.buildStatusCaps(), s.pipeW)
		s.requestFn(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				layer.SetStatus(resp)
			}
		})
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }
	}},
	{"n", "release notes", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		layer := NewScrollableLayer("release notes", strings.TrimRight(s.changelog, "\n"), false, s.pipeW, nil, s.termWidth, s.termHeight)
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }
	}},
	{"r", "refresh screen", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		if s.term != nil {
			s.term.SetPendingClear()
			s.server.Send(protocol.GetScreenRequest{
				RegionID: s.term.RegionID(),
			})
		}
		return nil, nil
	}},
	{"[", "scrollback", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		if s.term != nil {
			s.term.EnterScrollback(0)
		}
		return nil, nil
	}},
	{"b", "send literal ctrl+b", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		if s.regionID != "" {
			data := base64.StdEncoding.EncodeToString([]byte{0x02})
			s.server.Send(protocol.InputMsg{
				RegionID: s.regionID, Data: data,
			})
		}
		return nil, nil
	}},
}
