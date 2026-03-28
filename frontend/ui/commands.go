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
