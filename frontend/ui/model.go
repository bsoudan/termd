package ui

import (
	"bytes"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
	"termd/pkg/tui"
)

// Model is the top-level bubbletea model. It owns the layer stack and
// dispatches messages top-down. Protocol message unwrapping, req_id
// matching, raw input routing, and overlay compositing happen here.
type Model struct {
	stack    *tui.Stack
	registry *Registry
	req      *requestState
	Tasks    *tui.TaskRunner
	Detached bool
}

func NewModel(s *Server, pipeW io.Writer, registry *Registry, ring *termlog.LogRingBuffer, endpoint, version, changelog, sessionName string, connectFn func(string)) Model {
	hostname, _ := os.Hostname()
	req := &requestState{pending: make(map[uint64]ReplyFunc)}
	// currentServer is a mutable pointer so requestFn always uses the
	// latest server even after a reconnect swaps it.
	currentServer := s
	requestFn := func(msg any, reply ReplyFunc) {
		req.nextReqID++
		req.pending[req.nextReqID] = reply
		currentServer.Send(protocol.TaggedWithReqID(msg, req.nextReqID))
	}
	req.requestFn = requestFn
	main := NewMainLayer(s, pipeW, requestFn, registry, ring, endpoint, version, changelog, hostname, sessionName, connectFn)
	main.swapServerFn = func(newSrv *Server) { currentServer = newSrv }
	tasks := tui.NewTaskRunner()
	main.tasks = tasks
	stack := tui.NewStack(main)
	return Model{
		stack:    stack,
		registry: registry,
		req:      req,
		Tasks:    tasks,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.stack.Layers()[0].(*MainLayer).Init(), m.Tasks.ListenCmd())
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

	// Route task messages from task goroutines.
	if tui.IsTaskMsg(msg) {
		cmd := m.Tasks.HandleMsg(msg)
		return m, tea.Batch(cmd, m.Tasks.ListenCmd())
	}

	// Handle task Send messages — request/response from task goroutines.
	// requestFn runs here on the bubbletea goroutine (safe for requestState).
	if tsm, ok := msg.(tui.TaskSendMsg); ok {
		m.req.requestFn(tsm.Payload, func(payload any) {
			m.Tasks.Deliver(tsm.TaskID, payload)
		})
		return m, nil
	}

	// Check task WaitFor filters before layer iteration.
	if m.Tasks.CheckFilters(msg) {
		return m, nil // message consumed by a task
	}

	// DetachMsg signals the app should record detach state.
	if _, ok := msg.(DetachMsg); ok {
		m.Detached = true
		return m, nil
	}

	// RawInputMsg: Model handles command mode, focus mode routing,
	// prefix key detection, and key filter interception before the
	// normal layer iteration.
	//  - Command mode buffers keys after the prefix and matches
	//    against the chord trie (handled synchronously in MainLayer).
	//  - Focus mode feeds one sequence at a time through pipeW so
	//    overlay layers can pop between keystrokes.
	//  - Prefix key detection enters command mode and passes any
	//    remaining bytes to the chord buffer.
	//  - Key filters intercept specific sequences (e.g. PageUp/PageDown)
	//    and route them through bubbletea's key parser while forwarding
	//    the rest to the server.
	if raw, ok := msg.(RawInputMsg); ok {
		main := m.stack.Layers()[0].(*MainLayer)
		if main.commandMode {
			return m, main.handleCommandInput([]byte(raw))
		}
		if needsFocusRouting(m.stack) {
			return m.handleFocusInput(raw, main.pipeW)
		}
		if idx := bytes.IndexByte([]byte(raw), m.registry.PrefixKey); idx >= 0 {
			return m.handlePrefixDetected(raw, idx, main)
		}
		if filters := collectKeyFilters(m.stack); len(filters) > 0 {
			if cmd := m.handleFilteredInput(raw, filters, main); cmd != nil {
				return m, cmd
			}
		}
		// Normal mode — fall through to layer iteration.
		// MainLayer forwards to active session for mouse routing and server forwarding.
	}

	// Dispatch through the layer stack.
	cmd := m.stack.Update(msg)
	return m, cmd
}

// handleFocusInput routes raw input through pipeW one sequence at a time.
// This allows overlay layers (help, scrollback, etc.) to receive
// tea.KeyPressMsg events. Each sequence is written to pipeW individually;
// remaining bytes are re-sent as a new RawInputMsg for the next cycle.
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

// handlePrefixDetected handles a RawInputMsg that contains the prefix key.
// Bytes before the prefix are forwarded to the server. MainLayer enters
// command mode synchronously, and any remaining bytes are passed to the
// chord buffer immediately — no async layer push needed.
func (m Model) handlePrefixDetected(raw RawInputMsg, idx int, main *MainLayer) (tea.Model, tea.Cmd) {
	if idx > 0 {
		main.sendRawToServer(raw[:idx])
	}
	main.enterCommandMode()
	rest := raw[idx+1:]
	if len(rest) > 0 {
		return m, main.handleCommandInput(rest)
	}
	return m, nil
}

// handleFilteredInput scans raw input for sequences matching key filters.
// When a match is found, bytes before it are forwarded to the server,
// the matching sequence is written to pipeW (so bubbletea delivers it
// as a tea.KeyPressMsg), and remaining bytes are re-sent as RawInputMsg.
// Returns nil if no filtered keys were found.
func (m Model) handleFilteredInput(raw RawInputMsg, filters [][]byte, main *MainLayer) tea.Cmd {
	buf := []byte(raw)
	pos := 0
	for pos < len(buf) {
		_, _, n, _ := ansi.DecodeSequence(buf[pos:], ansi.NormalState, nil)
		if n == 0 {
			break
		}
		seq := buf[pos : pos+n]
		for _, f := range filters {
			if bytes.Equal(seq, f) {
				if pos > 0 {
					main.sendRawToServer(buf[:pos])
				}
				filtered := make([]byte, n)
				copy(filtered, seq)
				writeCmd := func() tea.Msg {
					main.pipeW.Write(filtered)
					return nil
				}
				rest := buf[pos+n:]
				if len(rest) > 0 {
					restCopy := make([]byte, len(rest))
					copy(restCopy, rest)
					resendCmd := func() tea.Msg { return RawInputMsg(restCopy) }
					return tea.Sequence(writeCmd, resendCmd)
				}
				return writeCmd
			}
		}
		pos += n
	}
	return nil
}

func (m Model) View() tea.View {
	main := m.stack.Layers()[0].(*MainLayer)

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
	for i, l := range m.stack.Layers() {
		if tl, ok := l.(TermdLayer); ok {
			t, s := tl.Status()
			if t != "" {
				statusText = t
				if i > 0 {
					statusStyle = s.Bold(true).Foreground(lipgloss.Color("6"))
				} else {
					statusStyle = s
				}
			}
			if i > 0 && tl.WantsKeyboardInput() != nil {
				hasOverlay = true
			}
		}
	}

	// Composite all layer views via the stack.
	layers := m.stack.View(width, height)

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
