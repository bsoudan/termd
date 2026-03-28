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
func (m Model) handleRawInput(chunk []byte) (tea.Model, tea.Cmd) {
	// Focus mode: write to bubbletea's input pipe for key event parsing.
	if m.overlay != nil || m.scrollback.Active() {
		if bytes.Contains(chunk, sgrMousePrefix) {
			mice, rest := extractSGRMouseSequences(chunk)
			var cmds []tea.Cmd
			if len(rest) > 0 {
				pipeW := m.pipeW
				cmds = append(cmds, func() tea.Msg {
					pipeW.Write(rest)
					return nil
				})
			}
			for _, mouse := range mice {
				saved := mouse
				cmds = append(cmds, func() tea.Msg { return saved })
			}
			return m, tea.Batch(cmds...)
		}
		pipeW := m.pipeW
		data := make([]byte, len(chunk))
		copy(data, chunk)
		return m, func() tea.Msg {
			pipeW.Write(data)
			return nil
		}
	}

	// Prefix active: next byte is the command
	if m.prefixMode {
		m.prefixMode = false
		key := chunk[0]
		chunk = chunk[1:]
		model, cmd := m.handlePrefixKey(key)
		if len(chunk) > 0 {
			m2 := model.(Model)
			m2.sendRawToServer(chunk)
			return m2, cmd
		}
		return model, cmd
	}

	// Scan for prefix key (ctrl+b)
	if idx := bytes.IndexByte(chunk, prefixKey); idx >= 0 {
		if idx > 0 {
			m.sendRawToServer(chunk[:idx])
		}
		m.prefixMode = true
		rest := chunk[idx+1:]
		if len(rest) > 0 {
			m.prefixMode = false
			key := rest[0]
			model, cmd := m.handlePrefixKey(key)
			if len(rest) > 1 {
				m2 := model.(Model)
				m2.sendRawToServer(rest[1:])
				return m2, cmd
			}
			return model, cmd
		}
		return m, nil
	}

	// Parse and route mouse sequences
	if bytes.Contains(chunk, sgrMousePrefix) {
		mice, rest := extractSGRMouseSequences(chunk)
		if len(rest) > 0 {
			m.sendRawToServer(rest)
		}
		var cmds []tea.Cmd
		for _, mouse := range mice {
			if m.terminal.ChildWantsMouse() {
				seq := encodeSGRMouse(mouse, mouse.Mouse().X, mouse.Mouse().Y-chromeRows)
				if seq != "" {
					m.server.Send(InputMsg{
						RegionID: m.regionID,
						Data:     []byte(seq),
					})
				}
			} else {
				m2, cmd := m.handleMouse(mouse)
				m = m2.(Model)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)
	}

	// Regular input — forward to server
	m.sendRawToServer(chunk)
	return m, nil
}
