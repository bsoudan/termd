package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
	"nxtermd/pkg/layer"
)

// NxtermModel implements tea.Model via Init/Update/View defined here.
// The event loop (Run), command mode, and connection lifecycle are in
// mainlayer.go.

func (m *NxtermModel) Init() tea.Cmd {
	initCmd := m.init()
	return tea.Batch(initCmd, m.tasks.ListenCmd(), tea.RequestTerminalVersion)
}

func (m *NxtermModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Strip stdin-side sync markers out of RawInputMsg before layers
	// see it, so layers never dispatch the OSC as a real keystroke.
	// Queue the ack to be flushed by the main loop after the next
	// render; emitting inline would lose ordering against the frame.
	if ri, ok := msg.(RawInputMsg); ok {
		remaining, ids := ExtractSyncMarkers([]byte(ri))
		if len(ids) > 0 {
			m.pendingSyncAcks = append(m.pendingSyncAcks, ids...)
			if len(remaining) == 0 {
				return m, nil
			}
			msg = RawInputMsg(remaining)
		}
	}

	// Handle tree sync messages before anything else.
	switch tmsg := msg.(type) {
	case protocol.TreeSnapshot:
		m.treeStore.HandleSnapshot(tmsg)
		changed := TreeChangedMsg{Tree: m.treeStore.Tree()}
		m.tasks.CheckFilters(changed)
		cmd := m.stack.Update(changed)
		return m, cmd
	case protocol.TreeEvents:
		if !m.treeStore.HandleEvents(tmsg) {
			m.server.Send(protocol.Tagged(protocol.TreeResyncRequest{}))
			return m, nil
		}
		changed := TreeChangedMsg{Tree: m.treeStore.Tree()}
		m.tasks.CheckFilters(changed)
		cmd := m.stack.Update(changed)
		return m, cmd
	}

	// Route task messages from task goroutines.
	if layer.IsTaskMsg(msg) {
		cmd := m.tasks.HandleMsg(msg)
		return m, tea.Batch(cmd, m.tasks.ListenCmd())
	}

	// Handle task Send messages — request/response from task goroutines.
	if tsm, ok := msg.(layer.TaskSendMsg); ok {
		m.nextReqID++
		m.pendingReplies[m.nextReqID] = tsm.TaskID
		m.server.Send(protocol.TaggedWithReqID(tsm.Payload, m.nextReqID))
		return m, nil
	}

	// Check task WaitFor filters before layer iteration.
	if m.tasks.CheckFilters(msg) {
		return m, nil
	}

	// DetachMsg signals the app should record detach state.
	if _, ok := msg.(DetachMsg); ok {
		m.Detached = true
		return m, nil
	}

	// MainCmd is handled directly — NxtermModel is not on the stack.
	if mc, ok := msg.(MainCmd); ok {
		_, cmd, _ := m.handleCmd(mc)
		return m, cmd
	}

	// System messages.
	switch msg := msg.(type) {
	case reconnectTickMsg:
		if m.sm.connStatus == "reconnecting" {
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} })
		}
		return m, nil
	case ConnectToServerMsg:
		// Let SessionManagerLayer tear down its current session first,
		// then dial the new endpoint via connectFn.
		m.stack.Update(msg)
		if m.connectFn != nil {
			m.connectFn(msg.Endpoint, msg.Session)
		}
		return m, nil
	case ConnectedMsg:
		// Update the top-level server reference so the main event loop
		// reads from the new connection's channels; then let the stack
		// process the rest (SessionManagerLayer creates the new session).
		m.server = msg.Server
		cmd := m.stack.Update(msg)
		return m, cmd
	case ServerErrorMsg:
		m.sm.err = msg.Context + ": " + msg.Message
		_, cmd := m.quit()
		return m, cmd
	case protocol.Identify:
		if msg.Hostname != m.sm.localHostname {
			m.sm.endpoint = m.sm.localHostname + " -> " + m.sm.endpoint
		}
		return m, nil
	case LogEntryMsg:
		return m, nil
	case showHintMsg:
		pushCmd := func() tea.Msg { return PushLayerMsg{Layer: &HintLayer{registry: m.registry}} }
		hideCmd := tea.Tick(3*time.Second, func(time.Time) tea.Msg { return hideHintMsg{} })
		return m, tea.Batch(pushCmd, hideCmd)
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		// Fall through to stack for SM to track dimensions too.
	}

	// Dispatch through the layer stack.
	cmd := m.stack.Update(msg)
	return m, cmd
}

func (m *NxtermModel) View() tea.View {
	width, height := m.termWidth, m.termHeight
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	// Pass 1: collect status and build render state.
	statusText := ""
	statusStyle := lipgloss.Style{}
	rs := RenderState{}

	// Command mode status comes from NxtermModel (not on the stack).
	rs.CommandMode = m.commandMode
	rs.SessionPaused = m.sessionPaused
	if m.commandMode {
		if len(m.commandBuffer) > 0 {
			statusText = "? " + strings.Join(m.commandBuffer, " ")
		} else {
			statusText = "?"
		}
		statusStyle = commandModeStyle
	}

	for i, l := range m.stack.Layers() {
		if tl, ok := l.(TermdLayer); ok {
			t, s := tl.Status(&rs)
			if t != "" && !m.commandMode {
				statusText = t
				if i > 0 {
					statusStyle = s.Bold(true).Foreground(lipgloss.Color("6"))
				} else {
					statusStyle = s
				}
			}
			if i > 0 && tl.WantsKeyboardInput() {
				rs.HasOverlay = true
			}
		}
	}
	rs.Active = !rs.HasOverlay

	// Pass 2: composite all layer views with the render state.
	layers := m.stack.View(width, height, &rs)

	// Status bar (right side of tab bar) as the topmost layer.
	statusContent, statusWidth := renderStatusBar(statusText, m.version, statusStyle, rs.HasHint)
	statusX := max(width-statusWidth, 0)
	layers = append(layers, lipgloss.NewLayer(statusContent).X(statusX).Z(2))

	content := lipgloss.NewCompositor(layers...).Render()

	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = windowTitle(m.sm)

	if activeTerm := m.sm.ActiveTerm(); activeTerm != nil {
		switch activeTerm.MouseMode() {
		case 2:
			v.MouseMode = tea.MouseModeAllMotion
		default:
			v.MouseMode = tea.MouseModeCellMotion
		}
	}

	return v
}

// serverFromEndpoint returns the host:port (or path) portion of a dial
// spec, used for the window title prefix.
func serverFromEndpoint(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	_, addr := transport.ParseSpec(endpoint)
	if at := strings.Index(addr, "@"); at >= 0 {
		return addr[at+1:]
	}
	return addr
}

// windowTitle composes the outer-terminal window title.
func windowTitle(sm *SessionManagerLayer) string {
	session := sm.activeSessionLayer()
	if session == nil {
		return "nxterm"
	}
	server := serverFromEndpoint(session.endpoint)
	if server == "" {
		return "nxterm"
	}
	suffix := "• nx "
	if session.sessionName != "" && session.sessionName != "main" {
		suffix += session.sessionName + "@"
	}
	suffix += server

	if t := sm.ActiveTerm(); t != nil {
		if title := t.Title(); title != "" {
			return title + " " + suffix
		}
	}
	return suffix
}

// renderStatusBar renders the right side of the tab bar for compositing
// on top of the session view at row 0. The status-text wrappers use the
// large dot (●) because the style passed in by the caller is typically
// bright cyan (command mode, active scrollback, etc.); the trailing dot
// after the nxterm label stays mid-size since it renders faint.
func renderStatusBar(status, version string, style lipgloss.Style, showVersion bool) (string, int) {
	result := style.Render("● " + status + " ●")
	displayWidth := len([]rune("● " + status + " ●"))

	suffix := "nxterm"
	if version != "" && showVersion {
		suffix = "nxterm " + version
	}
	result += statusFaint.Render(" ") + statusBold.Render(suffix) + statusFaint.Render(" •")
	displayWidth += 1 + len([]rune(suffix)) + 2

	return result, displayWidth
}
