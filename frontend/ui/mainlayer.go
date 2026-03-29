package ui

import (
	"fmt"
	"io"
	"slices"
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
	server *Server, pipeW io.Writer, requestFn RequestFunc,
	logRing *termlog.LogRingBuffer,
	endpoint, version, changelog, hostname, sessionName string,
) *MainLayer {
	m := &MainLayer{
		server:        server,
		pipeW:         pipeW,
		requestFn:     requestFn,
		logRing:       logRing,
		localHostname: hostname,
		endpoint:      endpoint,
		version:       version,
		changelog:     changelog,
		connStatus:    "connected",
	}
	session := NewSessionLayer(server, requestFn, logRing, endpoint, version, changelog, hostname, sessionName)
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
	// ── Session management commands ─────────────────────────────────────

	case NewSessionMsg:
		layer := &SessionNameLayer{}
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }, true

	case CreateSessionMsg:
		return nil, m.createSession(msg.Name), true

	case KillSessionMsg:
		return m.killSession()

	case SwitchSessionMsg:
		m.switchSession(msg.Index)
		return nil, nil, true

	// ── Global messages ─────────────────────────────────────────────────

	case DetachRequestMsg:
		resp, cmd := m.detach()
		return resp, cmd, true

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
		// Re-connect all sessions to refresh their region lists.
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
		pushCmd := func() tea.Msg { return PushLayerMsg{Layer: &HintLayer{}} }
		hideCmd := tea.Tick(3*time.Second, func(time.Time) tea.Msg { return hideHintMsg{} })
		return nil, tea.Batch(pushCmd, hideCmd), true

	// ── Overlay routing ─────────────────────────────────────────────────

	case OpenOverlayMsg:
		if msg.Name == "sessions" {
			return nil, m.openSessionPicker(), true
		}
		// Forward other overlays to active session.
		return m.forwardToActiveSession(msg)

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
		if len(m.sessions) <= 1 {
			// Last session has no regions — quit.
			qResp, qCmd := m.quit()
			return qResp, tea.Batch(cmd, qCmd), true
		}
		// Other sessions available — kill this one and switch.
		m.sessions = slices.Delete(m.sessions, m.activeSession, m.activeSession+1)
		if m.activeSession >= len(m.sessions) {
			m.activeSession = len(m.sessions) - 1
		}
		m.activeSessionLayer().Reconnect()
	}

	return resp, cmd, true
}

func (m *MainLayer) createSession(name string) tea.Cmd {
	// Deactivate current session.
	m.Deactivate()

	session := NewSessionLayer(m.server, m.requestFn, m.logRing, m.endpoint, m.version, m.changelog, m.localHostname, name)
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
		resp, cmd := m.quit()
		return resp, cmd, true
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

// View delegates to the active session.
func (m *MainLayer) View(width, height int, active bool) []*lipgloss.Layer {
	if m.err != "" {
		return []*lipgloss.Layer{lipgloss.NewLayer("error: " + m.err + "\n")}
	}
	s := m.activeSessionLayer()
	if s == nil {
		return nil
	}
	return s.View(width, height, active)
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
		return m.endpoint, statusFaint
	}
	return s.Status()
}
