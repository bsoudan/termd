package ui

import (
	"bytes"
	"io"
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

// handleFocusModeInput processes raw input during focus mode (overlay or
// scrollback active). Mouse sequences are extracted and re-sent as MouseMsg;
// remaining bytes go to pipeW for bubbletea key event parsing.
func handleFocusModeInput(chunk []byte, pipeW io.Writer) tea.Cmd {
	if bytes.Contains(chunk, sgrMousePrefix) {
		mice, rest := extractSGRMouseSequences(chunk)
		var cmds []tea.Cmd
		if len(rest) > 0 {
			cmds = append(cmds, func() tea.Msg {
				pipeW.Write(rest)
				return nil
			})
		}
		for _, mouse := range mice {
			saved := mouse
			cmds = append(cmds, func() tea.Msg { return saved })
		}
		return tea.Batch(cmds...)
	}
	data := make([]byte, len(chunk))
	copy(data, chunk)
	return func() tea.Msg {
		pipeW.Write(data)
		return nil
	}
}

// handleRawInput processes raw bytes from the terminal input goroutine.
// Returns (response, cmd) where response may be DetachMsg if detaching.
func (s *SessionLayer) handleRawInput(chunk []byte) (tea.Msg, tea.Cmd) {
	// Focus mode: overlay or scrollback is active — write to pipeW so
	// bubbletea parses key events. Layers above handle the resulting
	// KeyPressMsg and MouseMsg.
	if s.focusMode || (s.term != nil && s.term.ScrollbackActive()) {
		return nil, handleFocusModeInput(chunk, s.pipeW)
	}

	// Scan for prefix key (ctrl+b)
	if idx := bytes.IndexByte(chunk, prefixKey); idx >= 0 {
		if idx > 0 {
			s.sendRawToServer(chunk[:idx])
		}
		rest := chunk[idx+1:]
		pushCmd := func() tea.Msg { return PushLayerMsg{Layer: NewCommandLayer(s)} }
		if len(rest) > 0 {
			// Command byte in same chunk — push CommandLayer then re-send
			// remaining bytes. Sequence guarantees push happens first.
			restCopy := make([]byte, len(rest))
			copy(restCopy, rest)
			resendCmd := func() tea.Msg { return RawInputMsg(restCopy) }
			return nil, tea.Sequence(pushCmd, resendCmd)
		}
		return nil, pushCmd
	}

	// Parse and route mouse sequences
	if bytes.Contains(chunk, sgrMousePrefix) {
		mice, rest := extractSGRMouseSequences(chunk)
		if len(rest) > 0 {
			s.sendRawToServer(rest)
		}
		var cmds []tea.Cmd
		for _, mouse := range mice {
			if s.term != nil && s.term.ChildWantsMouse() {
				seq := encodeSGRMouse(mouse, mouse.Mouse().X, mouse.Mouse().Y-chromeRows)
				if seq != "" {
					s.server.Send(InputMsg{
						RegionID: s.term.RegionID(),
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
