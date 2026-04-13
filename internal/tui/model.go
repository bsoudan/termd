package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
	"nxtermd/pkg/layer"
)

// teaModel adapts MainLayer to bubbletea's Model interface.
// It has no state of its own — everything lives on MainLayer.
type teaModel struct{ *MainLayer }

func (m teaModel) Init() tea.Cmd {
	return tea.Batch(m.MainLayer.Init(), m.tasks.ListenCmd())
}

func (m teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle tree sync messages before anything else.
	switch tmsg := msg.(type) {
	case protocol.TreeSnapshot:
		m.treeStore.HandleSnapshot(tmsg)
		cmd := m.stack.Update(TreeChangedMsg{Tree: m.treeStore.Tree()})
		return m, cmd
	case protocol.TreeEvents:
		if !m.treeStore.HandleEvents(tmsg) {
			m.server.Send(protocol.Tagged(protocol.TreeResyncRequest{}))
			return m, nil
		}
		cmd := m.stack.Update(TreeChangedMsg{Tree: m.treeStore.Tree()})
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

	// Dispatch through the layer stack.
	cmd := m.stack.Update(msg)
	return m, cmd
}

func (m teaModel) View() tea.View {
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
	for i, l := range m.stack.Layers() {
		if tl, ok := l.(TermdLayer); ok {
			t, s := tl.Status(&rs)
			if t != "" {
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
	v.WindowTitle = windowTitle(m.MainLayer)

	if activeTerm := m.ActiveTerm(); activeTerm != nil {
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

// windowTitle composes the outer-terminal window title for the active
// session+region.
func windowTitle(main *MainLayer) string {
	session := main.activeSessionLayer()
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

	if t := main.ActiveTerm(); t != nil {
		if title := t.Title(); title != "" {
			return title + " " + suffix
		}
	}
	return suffix
}

// renderStatusBar renders the right side of the tab bar for compositing
// on top of the session view at row 0.
func renderStatusBar(status, version string, style lipgloss.Style, showVersion bool) (string, int) {
	result := style.Render("• " + status + " •")
	displayWidth := len([]rune("• " + status + " •"))

	suffix := "nxterm"
	if version != "" && showVersion {
		suffix = "nxterm " + version
	}
	result += statusFaint.Render(" ") + statusBold.Render(suffix) + statusFaint.Render(" •")
	displayWidth += 1 + len([]rune(suffix)) + 2

	return result, displayWidth
}
