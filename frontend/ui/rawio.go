package ui

import (
	"bytes"
	"os"

	tea "charm.land/bubbletea/v2"
)

// RawInputMsg carries raw bytes from the terminal input goroutine.
type RawInputMsg []byte

// InputLoop reads raw bytes from stdin and sends them to bubbletea.
// It exits when stdin is closed or returns an error.
func InputLoop(stdin *os.File, p *tea.Program) {
	buf := make([]byte, 4096)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			raw := make([]byte, n)
			copy(raw, buf[:n])
			p.Send(RawInputMsg(raw))
		}
		if err != nil {
			return
		}
	}
}

const prefixKey = 0x02 // ctrl+b

// handleRawInput processes raw bytes from the terminal input goroutine.
// Returns (response, cmd) where response may be DetachMsg if detaching.
func (s *SessionLayer) handleRawInput(chunk []byte) (tea.Msg, tea.Cmd) {
	// Focus mode: write to bubbletea's input pipe for key event parsing.
	if s.overlay != nil || s.scrollback.Active() {
		if bytes.Contains(chunk, sgrMousePrefix) {
			mice, rest := extractSGRMouseSequences(chunk)
			var cmds []tea.Cmd
			if len(rest) > 0 {
				pipeW := s.pipeW
				cmds = append(cmds, func() tea.Msg {
					pipeW.Write(rest)
					return nil
				})
			}
			for _, mouse := range mice {
				saved := mouse
				cmds = append(cmds, func() tea.Msg { return saved })
			}
			return nil, tea.Batch(cmds...)
		}
		pipeW := s.pipeW
		data := make([]byte, len(chunk))
		copy(data, chunk)
		return nil, func() tea.Msg {
			pipeW.Write(data)
			return nil
		}
	}

	// Prefix active: next byte is the command
	if s.prefixMode {
		s.prefixMode = false
		key := chunk[0]
		chunk = chunk[1:]
		resp, cmd := s.handlePrefixKey(key)
		if len(chunk) > 0 {
			s.sendRawToServer(chunk)
		}
		return resp, cmd
	}

	// Scan for prefix key (ctrl+b)
	if idx := bytes.IndexByte(chunk, prefixKey); idx >= 0 {
		if idx > 0 {
			s.sendRawToServer(chunk[:idx])
		}
		s.prefixMode = true
		rest := chunk[idx+1:]
		if len(rest) > 0 {
			s.prefixMode = false
			key := rest[0]
			resp, cmd := s.handlePrefixKey(key)
			if len(rest) > 1 {
				s.sendRawToServer(rest[1:])
			}
			return resp, cmd
		}
		return nil, nil
	}

	// Parse and route mouse sequences
	if bytes.Contains(chunk, sgrMousePrefix) {
		mice, rest := extractSGRMouseSequences(chunk)
		if len(rest) > 0 {
			s.sendRawToServer(rest)
		}
		var cmds []tea.Cmd
		for _, mouse := range mice {
			if s.terminal.ChildWantsMouse() {
				seq := encodeSGRMouse(mouse, mouse.Mouse().X, mouse.Mouse().Y-chromeRows)
				if seq != "" {
					s.server.Send(InputMsg{
						RegionID: s.regionID,
						Data:     []byte(seq),
					})
				}
			} else {
				cmd := s.handleMouse(mouse)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return nil, tea.Batch(cmds...)
	}

	// Regular input — forward to server
	s.sendRawToServer(chunk)
	return nil, nil
}
