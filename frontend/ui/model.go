package ui

import (
	"bytes"
	"io"
	"os"
	"slices"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// Model is the top-level bubbletea model. It owns the layer stack and
// dispatches messages top-down. Protocol message unwrapping, req_id
// matching, raw input routing, and overlay compositing happen here.
type Model struct {
	layers   []Layer
	req      *requestState
	Detached bool
	initDone chan struct{}
}

func NewModel(s *Server, pipeW io.Writer, ring *termlog.LogRingBuffer, endpoint, version, changelog, sessionName string) Model {
	hostname, _ := os.Hostname()
	req := &requestState{pending: make(map[uint64]ReplyFunc)}
	requestFn := func(msg any, reply ReplyFunc) {
		req.nextReqID++
		req.pending[req.nextReqID] = reply
		s.Send(protocol.TaggedWithReqID(msg, req.nextReqID))
	}
	main := NewMainLayer(s, pipeW, requestFn, ring, endpoint, version, changelog, hostname, sessionName)
	return Model{
		layers:   []Layer{main},
		req:      req,
		initDone: make(chan struct{}),
	}
}

func (m Model) Init() tea.Cmd {
	close(m.initDone)
	return m.layers[0].(*MainLayer).Init()
}

// InitDone returns a channel that is closed when Init completes.
// InputLoop uses this to know when to stop forwarding input to bubbletea
// and start sending RawInputMsg instead.
func (m Model) InitDone() <-chan struct{} {
	return m.initDone
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

	// RawInputMsg: Model handles focus mode routing and ctrl+b detection.
	// This must happen before the normal layer iteration because:
	//  - Focus mode needs to feed one sequence at a time through pipeW
	//    so overlay/command layers can pop between keystrokes.
	//  - ctrl+b detection pushes CommandLayer before delivering the
	//    remaining bytes, ensuring proper sequencing.
	if raw, ok := msg.(RawInputMsg); ok {
		main := m.layers[0].(*MainLayer)
		if m.hasFocusLayer(main) {
			return m.handleFocusInput(raw, main.pipeW)
		}
		if idx := bytes.IndexByte([]byte(raw), prefixKey); idx >= 0 {
			return m.handlePrefixDetected(raw, idx, main)
		}
		// Normal mode — fall through to layer iteration.
		// MainLayer forwards to active session for mouse routing and server forwarding.
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

// hasFocusLayer returns true if any overlay or scrollback mode is active,
// meaning raw input should be routed through pipeW for key event parsing
// rather than forwarded to the server.
func (m Model) hasFocusLayer(main *MainLayer) bool {
	for i := 1; i < len(m.layers); i++ {
		switch m.layers[i].(type) {
		case *CommandLayer, *ScrollableLayer, *StatusLayer, *HelpLayer, *ProgramPickerLayer, *SessionPickerLayer, *SessionNameLayer:
			return true
		}
	}
	t := main.ActiveTerm()
	return t != nil && t.ScrollbackActive()
}

// handleFocusInput routes raw input through pipeW one sequence at a time.
// This allows layers to pop between keystrokes — for example, CommandLayer
// handles one key and pops, then remaining bytes arrive as a new RawInputMsg
// in the next Update cycle where CommandLayer is no longer on the stack.
func (m Model) handleFocusInput(raw RawInputMsg, pipeW io.Writer) (tea.Model, tea.Cmd) {
	_, _, n, _ := ansi.DecodeSequence([]byte(raw), ansi.NormalState, nil)
	if n <= 0 {
		n = len(raw)
	}

	first := make([]byte, n)
	copy(first, raw[:n])
	writeCmd := func() tea.Msg {
		pipeW.Write(first)
		return nil
	}

	if n < len(raw) {
		rest := make([]byte, len(raw)-n)
		copy(rest, raw[n:])
		resendCmd := func() tea.Msg { return RawInputMsg(rest) }
		return m, tea.Sequence(writeCmd, resendCmd)
	}
	return m, writeCmd
}

// handlePrefixDetected handles a RawInputMsg that contains ctrl+b.
// Bytes before ctrl+b are forwarded to the server. A CommandLayer is
// pushed, then any bytes after ctrl+b are re-sent as a new RawInputMsg
// for CommandLayer to process. tea.Sequence guarantees the push happens
// before the re-send.
func (m Model) handlePrefixDetected(raw RawInputMsg, idx int, main *MainLayer) (tea.Model, tea.Cmd) {
	if idx > 0 {
		main.sendRawToServer(raw[:idx])
	}

	pushCmd := func() tea.Msg { return PushLayerMsg{Layer: &CommandLayer{}} }
	rest := raw[idx+1:]
	if len(rest) > 0 {
		restCopy := make([]byte, len(rest))
		copy(restCopy, rest)
		resendCmd := func() tea.Msg { return RawInputMsg(restCopy) }
		return m, tea.Sequence(pushCmd, resendCmd)
	}
	return m, pushCmd
}

func (m Model) View() tea.View {
	main := m.layers[0].(*MainLayer)

	width, height := main.termWidth, main.termHeight
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	// Collect topmost Status and detect overlay presence.
	// Status is collected bottom-up; topmost non-empty text wins.
	// Model applies bold to status from layers above main.
	statusText := ""
	statusStyle := lipgloss.Style{}
	hasOverlay := false
	for i := 0; i < len(m.layers); i++ {
		t, s := m.layers[i].Status()
		if t != "" {
			statusText = t
			if i > 0 {
				statusStyle = s.Bold(true).Foreground(lipgloss.Color("6"))
			} else {
				statusStyle = s
			}
		}
		switch m.layers[i].(type) {
		case *ScrollableLayer, *StatusLayer, *HelpLayer, *ProgramPickerLayer, *SessionPickerLayer, *SessionNameLayer:
			hasOverlay = true
		}
	}

	// Collect all View layers. Each layer returns a slice; flatten them.
	// The first layer (main) is active when no overlay is above it.
	var layers []*lipgloss.Layer
	layers = append(layers, main.View(width, height, !hasOverlay)...)
	for i := 1; i < len(m.layers); i++ {
		layers = append(layers, m.layers[i].View(width, height, false)...)
	}

	// Status bar (right side of tab bar) as the topmost layer.
	statusContent, statusWidth := renderStatusBar(statusText, main.version, statusStyle, hasOverlay)
	statusX := max(width-statusWidth, 0)
	layers = append(layers, lipgloss.NewLayer(statusContent).X(statusX).Z(2))

	content := lipgloss.NewCompositor(layers...).Render()

	v := tea.NewView(content)
	v.AltScreen = true

	if activeTerm := main.ActiveTerm(); activeTerm != nil {
		switch activeTerm.MouseMode() {
		case 2:
			v.MouseMode = tea.MouseModeAllMotion
		default:
			v.MouseMode = tea.MouseModeCellMotion
		}
	}

	return v
}

// renderStatusBar renders the right side of the tab bar for compositing
// on top of the session view at row 0.
func renderStatusBar(status, version string, style lipgloss.Style, showVersion bool) (string, int) {
	result := style.Render("• " + status + " •")
	displayWidth := len([]rune("• " + status + " •"))

	suffix := "termd-tui"
	if version != "" && showVersion {
		suffix = "termd-tui " + version
	}
	result += statusFaint.Render(" ") + statusBold.Render(suffix) + statusFaint.Render(" •")
	displayWidth += 1 + len([]rune(suffix)) + 2

	return result, displayWidth
}
