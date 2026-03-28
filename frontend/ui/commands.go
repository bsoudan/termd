package ui

import (
	"encoding/base64"
	"strings"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/protocol"
)

// handlePrefixKey handles a single key byte after ctrl+b.
func (s *SessionLayer) handlePrefixKey(key byte) (tea.Msg, tea.Cmd) {
	return s.handlePrefixCommand(key)
}

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
	s.prefixMode = false
	switch key {
	case 'd':
		return s.detach()
	case prefixKey: // ctrl+b ctrl+b → send literal ctrl+b
		s.sendRawToServer([]byte{prefixKey})
		return nil, nil
	case 'l':
		s.overlay = NewScrollableOverlay("logviewer", s.logRing.String(), true, s.termWidth, s.termHeight)
		return nil, nil
	case '?':
		s.overlay = NewHelpOverlay(helpItems)
		return nil, nil
	case 's':
		so := NewStatusOverlay(s.buildStatusCaps())
		s.overlay = so
		s.request(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				so.SetStatus(resp)
			}
		})
		return nil, nil
	case 'n':
		s.overlay = NewScrollableOverlay("release notes", strings.TrimRight(s.changelog, "\n"), false, s.termWidth, s.termHeight)
		return nil, nil
	case '[':
		if s.regionID != "" {
			s.scrollback = s.scrollback.Enter(0)
			s.server.Send(protocol.GetScrollbackRequest{RegionID: s.regionID})
		}
		return nil, nil
	case 'r':
		if s.regionID != "" {
			s.terminal = s.terminal.SetPendingClear()
			s.server.Send(protocol.GetScreenRequest{
				RegionID: s.regionID,
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
		s.overlay = NewScrollableOverlay("logviewer", s.logRing.String(), true, s.termWidth, s.termHeight)
		return nil, nil
	}},
	{"s", "status", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		so := NewStatusOverlay(s.buildStatusCaps())
		s.overlay = so
		s.request(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				so.SetStatus(resp)
			}
		})
		return nil, nil
	}},
	{"n", "release notes", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		s.overlay = NewScrollableOverlay("release notes", strings.TrimRight(s.changelog, "\n"), false, s.termWidth, s.termHeight)
		return nil, nil
	}},
	{"r", "refresh screen", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		if s.regionID != "" {
			s.terminal = s.terminal.SetPendingClear()
			s.server.Send(protocol.GetScreenRequest{
				RegionID: s.regionID,
			})
		}
		return nil, nil
	}},
	{"[", "scrollback", func(s *SessionLayer) (tea.Msg, tea.Cmd) {
		if s.regionID != "" {
			s.scrollback = s.scrollback.Enter(0)
			s.server.Send(protocol.GetScrollbackRequest{RegionID: s.regionID})
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
