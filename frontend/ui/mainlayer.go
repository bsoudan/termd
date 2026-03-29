package ui

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// MainLayer sits at index 0 in the layer stack and manages multiple
// SessionLayers. It intercepts session management commands and global
// messages, forwarding everything else to the active session.
type MainLayer struct {
	server    *Server
	pipeW     io.Writer
	requestFn RequestFunc
	registry  *Registry

	sessions      []*SessionLayer
	activeSession int

	logRing       *termlog.LogRingBuffer
	localHostname string
	endpoint      string
	version       string
	changelog     string

	connStatus string
	retryAt    time.Time
	err        string

	termWidth  int
	termHeight int
}

func NewMainLayer(
	server *Server, pipeW io.Writer, requestFn RequestFunc, registry *Registry,
	logRing *termlog.LogRingBuffer,
	endpoint, version, changelog, hostname, sessionName string,
) *MainLayer {
	m := &MainLayer{
		server:        server,
		pipeW:         pipeW,
		requestFn:     requestFn,
		registry:      registry,
		logRing:       logRing,
		localHostname: hostname,
		endpoint:      endpoint,
		version:       version,
		changelog:     changelog,
		connStatus:    "connected",
	}
	session := NewSessionLayer(server, requestFn, registry, logRing, endpoint, version, changelog, hostname, sessionName)
	m.sessions = []*SessionLayer{session}
	return m
}

func (m *MainLayer) activeSessionLayer() *SessionLayer {
	if len(m.sessions) == 0 {
		return nil
	}
	return m.sessions[m.activeSession]
}

// ActiveTerm returns the active session's active TerminalLayer.
func (m *MainLayer) ActiveTerm() *TerminalLayer {
	if s := m.activeSessionLayer(); s != nil {
		return s.ActiveTerm()
	}
	return nil
}

func (m *MainLayer) sendRawToServer(raw []byte) {
	if s := m.activeSessionLayer(); s != nil {
		s.sendRawToServer(raw)
	}
}

// Init delegates to the first session and starts the hint timer.
func (m *MainLayer) Init() tea.Cmd {
	s := m.sessions[0]
	s.server.Send(protocol.SessionConnectRequest{Session: s.sessionName})
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} })
}

func (m *MainLayer) Activate() tea.Cmd {
	if s := m.activeSessionLayer(); s != nil {
		return s.Activate()
	}
	return nil
}

func (m *MainLayer) Deactivate() {
	if s := m.activeSessionLayer(); s != nil {
		s.Deactivate()
	}
}

func (m *MainLayer) quit() (tea.Msg, tea.Cmd) {
	m.Deactivate()
	m.server.Send(protocol.Disconnect{})
	return nil, tea.Quit
}

func (m *MainLayer) detach() (tea.Msg, tea.Cmd) {
	m.Deactivate()
	m.server.Send(protocol.Disconnect{})
	return DetachMsg{}, tea.Quit
}

func (m *MainLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	// ── Keybinding commands (MainCmd) ───────────────────────────────────

	case MainCmd:
		return m.handleCmd(msg)

	// ── System messages ─────────────────────────────────────────────────

	case DisconnectedMsg:
		m.connStatus = "reconnecting"
		m.retryAt = msg.RetryAt
		for _, s := range m.sessions {
			s.connStatus = "reconnecting"
		}
		return nil, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} }), true

	case ReconnectedMsg:
		m.connStatus = "connected"
		m.retryAt = time.Time{}
		for _, s := range m.sessions {
			s.connStatus = "connected"
		}
		for _, s := range m.sessions {
			s.Reconnect()
		}
		return nil, nil, true

	case reconnectTickMsg:
		if m.connStatus == "reconnecting" {
			return nil, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} }), true
		}
		return nil, nil, true

	case ServerErrorMsg:
		m.err = msg.Context + ": " + msg.Message
		resp, cmd := m.quit()
		return resp, cmd, true

	case protocol.Identify:
		if msg.Hostname != m.localHostname {
			m.endpoint = m.localHostname + " -> " + m.endpoint
		}
		return nil, nil, true

	case LogEntryMsg:
		return nil, nil, true

	case showHintMsg:
		pushCmd := func() tea.Msg { return PushLayerMsg{Layer: &HintLayer{registry: m.registry}} }
		hideCmd := tea.Tick(3*time.Second, func(time.Time) tea.Msg { return hideHintMsg{} })
		return nil, tea.Batch(pushCmd, hideCmd), true

	// ── Dimension tracking ──────────────────────────────────────────────

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m.forwardToActiveSession(msg)

	// ── Everything else → active session ────────────────────────────────

	default:
		return m.forwardToActiveSession(msg)
	}
}

func (m *MainLayer) forwardToActiveSession(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	s := m.activeSessionLayer()
	if s == nil {
		return nil, nil, true
	}
	resp, cmd, _ := s.Update(msg)

	// If the active session lost all its regions, handle it.
	if len(s.tabs) == 0 && s.status == "no regions" {
		s.Deactivate()
		m.sessions = slices.Delete(m.sessions, m.activeSession, m.activeSession+1)
		if len(m.sessions) == 0 {
			m.activeSession = 0
			return resp, cmd, true
		}
		if m.activeSession >= len(m.sessions) {
			m.activeSession = len(m.sessions) - 1
		}
		m.activeSessionLayer().Reconnect()
	}

	return resp, cmd, true
}

func (m *MainLayer) handleCmd(msg MainCmd) (tea.Msg, tea.Cmd, bool) {
	push := func(layer Layer) (tea.Msg, tea.Cmd, bool) {
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }, true
	}
	switch msg.Name {

	// ── Session management ─────────────────────────────────────────────
	case "open-session":
		if msg.Args != "" {
			return nil, m.createSession(msg.Args), true
		}
		return push(&SessionNameLayer{})
	case "close-session":
		return m.killSession()
	case "next-session":
		m.nextSession()
		return nil, nil, true
	case "prev-session":
		m.prevSession()
		return nil, nil, true
	case "switch-session":
		if msg.Args == "" {
			return nil, m.openSessionPicker(), true
		}
		idx, err := strconv.Atoi(msg.Args)
		if err == nil && idx > 0 {
			m.switchSession(idx - 1)
		}
		return nil, nil, true
	case "detach":
		resp, cmd := m.detach()
		return resp, cmd, true

	// ── Overlays ───────────────────────────────────────────────────────
	case "run-command":
		return push(NewCommandPaletteLayer(m.registry))
	case "show-help":
		return push(NewHelpLayer(m.registry))
	case "show-log":
		return push(NewScrollableLayer("logviewer", m.logRing.String(), true, m.logRing, m.termWidth, m.termHeight))
	case "show-release-notes":
		return push(NewScrollableLayer("release notes", strings.TrimRight(m.changelog, "\n"), false, nil, m.termWidth, m.termHeight))
	case "show-status":
		caps := StatusCaps{
			Hostname:    m.localHostname,
			Endpoint:    m.endpoint,
			Version:     m.version,
			ConnStatus:  m.connStatus,
		}
		if s := m.activeSessionLayer(); s != nil {
			caps.SessionName = s.sessionName
			if t := s.activeTerm(); t != nil {
				caps.KeyboardFlags = t.KeyboardFlags()
				caps.BgDark = t.BgDark()
				caps.TermEnv = t.TermEnv()
				caps.MouseModes = t.MouseModes()
			}
		}
		sl := NewStatusLayer(caps)
		m.requestFn(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(protocol.StatusResponse); ok {
				sl.SetStatus(&resp)
			}
		})
		return push(sl)

	// ── Commands that require an active session ────────────────────────
	case "send-prefix":
		if s := m.activeSessionLayer(); s != nil {
			s.sendRawToServer([]byte{m.registry.PrefixKey})
		}
		return nil, nil, true
	case "enter-scrollback":
		if s := m.activeSessionLayer(); s != nil {
			if t := s.activeTerm(); t != nil {
				t.EnterScrollback(0)
			}
		}
		return nil, nil, true
	case "refresh-screen":
		if s := m.activeSessionLayer(); s != nil {
			if t := s.activeTerm(); t != nil {
				t.SetPendingClear()
				m.server.Send(protocol.GetScreenRequest{RegionID: t.RegionID()})
			}
		}
		return nil, nil, true

	default:
		return nil, nil, true
	}
}

func (m *MainLayer) createSession(name string) tea.Cmd {
	// Deactivate current session.
	m.Deactivate()

	session := NewSessionLayer(m.server, m.requestFn, m.registry, m.logRing, m.endpoint, m.version, m.changelog, m.localHostname, name)
	session.connStatus = m.connStatus
	session.termWidth = m.termWidth
	session.termHeight = m.termHeight
	m.sessions = append(m.sessions, session)
	m.activeSession = len(m.sessions) - 1

	// Connect to the new session.
	session.server.Send(protocol.SessionConnectRequest{Session: name})
	return nil
}

func (m *MainLayer) killSession() (tea.Msg, tea.Cmd, bool) {
	s := m.activeSessionLayer()
	if s == nil {
		return nil, nil, true
	}

	s.KillAllRegions()
	s.Deactivate()
	m.sessions = slices.Delete(m.sessions, m.activeSession, m.activeSession+1)

	if len(m.sessions) == 0 {
		m.activeSession = 0
		return nil, nil, true
	}

	if m.activeSession >= len(m.sessions) {
		m.activeSession = len(m.sessions) - 1
	}

	// Activate the new current session — reconnect to refresh its state.
	newSession := m.activeSessionLayer()
	newSession.Reconnect()
	return nil, nil, true
}

func (m *MainLayer) switchSession(idx int) {
	if idx < 0 || idx >= len(m.sessions) || idx == m.activeSession {
		return
	}
	m.Deactivate()
	m.activeSession = idx
	// Reconnect to refresh the session's region list and subscribe.
	m.activeSessionLayer().Reconnect()
}

func (m *MainLayer) nextSession() {
	if len(m.sessions) <= 1 {
		return
	}
	m.switchSession((m.activeSession + 1) % len(m.sessions))
}

func (m *MainLayer) prevSession() {
	if len(m.sessions) <= 1 {
		return
	}
	m.switchSession((m.activeSession - 1 + len(m.sessions)) % len(m.sessions))
}

func (m *MainLayer) openSessionPicker() tea.Cmd {
	var sessions []sessionInfo
	for i, s := range m.sessions {
		sessions = append(sessions, sessionInfo{
			name:        s.sessionName,
			regionCount: len(s.tabs),
			active:      i == m.activeSession,
		})
	}
	picker := NewSessionPickerLayer(sessions)
	return func() tea.Msg { return PushLayerMsg{Layer: picker} }
}

// View delegates to the active session, or renders the no-session pattern.
func (m *MainLayer) View(width, height int, active bool) []*lipgloss.Layer {
	if m.err != "" {
		return []*lipgloss.Layer{lipgloss.NewLayer("error: " + m.err + "\n")}
	}
	s := m.activeSessionLayer()
	if s != nil {
		return s.View(width, height, active)
	}
	return m.viewNoSession(width, height)
}

// viewNoSession renders a sparse microdot grid filling the content area.
// Dots appear on every other row with a space between each dot.
func (m *MainLayer) viewNoSession(width, height int) []*lipgloss.Layer {
	width = max(width, 80)
	height = max(height, 24)

	// Tab bar: faint dots across the full width.
	var tabBar strings.Builder
	tabBar.WriteString(statusFaint.Render("•"))
	fillCount := max(width-2, 1)
	for range fillCount {
		tabBar.WriteString("·")
	}
	tabBar.WriteString("•")
	tabBarStr := statusFaint.Render(tabBar.String())

	contentHeight := max(height-1, 1)
	var sb strings.Builder
	for row := range contentHeight {
		if row%2 == 1 {
			for col := range width {
				if col%2 == 0 {
					sb.WriteRune('·')
				} else {
					sb.WriteByte(' ')
				}
			}
		} else {
			for range width {
				sb.WriteByte(' ')
			}
		}
		if row < contentHeight-1 {
			sb.WriteByte('\n')
		}
	}

	return []*lipgloss.Layer{
		lipgloss.NewLayer(tabBarStr),
		lipgloss.NewLayer(statusFaint.Render(sb.String())).Y(1),
	}
}

// Status returns the active session's status, overlaid with reconnecting
// info when the connection is down.
func (m *MainLayer) Status() (string, lipgloss.Style) {
	if m.connStatus == "reconnecting" {
		secs := int(time.Until(m.retryAt).Seconds()) + 1
		return fmt.Sprintf("reconnecting to %s in %ds...", m.endpoint, secs), statusBoldRed
	}
	s := m.activeSessionLayer()
	if s == nil {
		return "no session", statusFaint
	}
	return s.Status()
}
