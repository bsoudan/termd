package tui

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nxtermd/internal/protocol"
	"nxtermd/pkg/layer"
)

// SetConnStatusMsg updates the connection status on all sessions.
type SetConnStatusMsg struct{ Status string }

// ReconnectAllMsg triggers a reconnect on all sessions.
type ReconnectAllMsg struct{}

// SetSessionsMsg replaces the session list.
type SetSessionsMsg struct {
	Sessions []*SessionLayer
}

// SessionManagerLayer is the base layer on the stack. It owns the
// session list, routes messages to the active session, and renders
// the tab bar and terminal content.
type SessionManagerLayer struct {
	server   *Server
	registry *Registry

	sessions      []*SessionLayer
	activeSession int

	localHostname   string
	endpoint        string
	version         string
	sessionName     string
	statusBarMargin int

	connStatus string
	retryAt    time.Time
	err        string

	treeStore *TreeStore
	tasks     *layer.TaskRunner[RenderState]

	// program is set once by NxtermModel.Run after bubbletea starts.
	// Task goroutines use program.Send to post state updates back to the
	// main (bubbletea) goroutine instead of mutating fields directly.
	program *tea.Program

	upgradeServerAvail bool
	upgradeServerVer   string
	upgradeClientAvail bool
	upgradeClientVer   string

	termWidth  int
	termHeight int
}

// upgradeInfoMsg is posted by upgrade-check task goroutines. The main
// (bubbletea) goroutine handles it in Update and writes the fields, so
// Status() can read them without a mutex.
type upgradeInfoMsg struct {
	ServerAvailable bool
	ServerVersion   string
	ClientAvailable bool
	ClientVersion   string
}

// SetProgram wires the bubbletea program reference so task goroutines
// can post messages back to the main event loop. Called once, from
// NxtermModel.Run.
func (sm *SessionManagerLayer) SetProgram(p *tea.Program) {
	sm.program = p
}

func NewSessionManagerLayer(
	server *Server, registry *Registry, treeStore *TreeStore,
	tasks *layer.TaskRunner[RenderState],
	endpoint, version, hostname, sessionName string,
	statusBarMargin int,
) *SessionManagerLayer {
	sm := &SessionManagerLayer{
		server:          server,
		registry:        registry,
		treeStore:       treeStore,
		tasks:           tasks,
		localHostname:   hostname,
		endpoint:        endpoint,
		version:         version,
		sessionName:     sessionName,
		statusBarMargin: statusBarMargin,
		connStatus:      "connected",
	}
	if endpoint != "" {
		session := NewSessionLayer(server, registry, treeStore, endpoint, sessionName, statusBarMargin)
		sm.sessions = []*SessionLayer{session}
	}
	return sm
}

func (sm *SessionManagerLayer) activeSessionLayer() *SessionLayer {
	if len(sm.sessions) == 0 {
		return nil
	}
	return sm.sessions[sm.activeSession]
}

// ActiveTerm returns the active session's active terminal.
func (sm *SessionManagerLayer) ActiveTerm() *TerminalLayer {
	if s := sm.activeSessionLayer(); s != nil {
		return s.activeTerm()
	}
	return nil
}

func (sm *SessionManagerLayer) Activate() tea.Cmd {
	if s := sm.activeSessionLayer(); s != nil {
		return s.Activate()
	}
	return nil
}

func (sm *SessionManagerLayer) Deactivate() {
	if s := sm.activeSessionLayer(); s != nil {
		s.Deactivate()
	}
}

func (sm *SessionManagerLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case SessionManagerCmd:
		return sm.handleCmd(msg)

	case SetConnStatusMsg:
		sm.connStatus = msg.Status
		for _, s := range sm.sessions {
			s.connStatus = msg.Status
		}
		return nil, nil, true

	case ReconnectAllMsg:
		sm.connStatus = "connected"
		for _, s := range sm.sessions {
			s.connStatus = "connected"
		}
		for _, s := range sm.sessions {
			s.Reconnect()
		}
		if s := sm.activeSessionLayer(); s != nil {
			s.Activate()
		}
		return nil, nil, true

	case SetSessionsMsg:
		sm.sessions = msg.Sessions
		sm.activeSession = 0
		return nil, nil, true

	case upgradeInfoMsg:
		sm.upgradeServerAvail = msg.ServerAvailable
		sm.upgradeServerVer = msg.ServerVersion
		sm.upgradeClientAvail = msg.ClientAvailable
		sm.upgradeClientVer = msg.ClientVersion
		return nil, nil, true

	case ConnectToServerMsg:
		sm.server.Close()
		sm.Deactivate()
		sm.sessions = nil
		sm.activeSession = 0
		return nil, nil, false // let NxtermModel handle the connect

	case ConnectedMsg:
		sm.server = msg.Server
		sm.endpoint = msg.Endpoint
		sm.connStatus = "connected"
		if msg.Session != "" {
			sm.sessionName = msg.Session
		}
		session := NewSessionLayer(sm.server, sm.registry, sm.treeStore, sm.endpoint, sm.sessionName, sm.statusBarMargin)
		session.termWidth = sm.termWidth
		session.termHeight = sm.termHeight
		sm.sessions = []*SessionLayer{session}
		sm.activeSession = 0
		session.server.Send(protocol.SessionConnectRequest{
			Session: session.sessionName,
			Width:   uint16(sm.termWidth),
			Height:  uint16(sm.viewportHeight()),
		})
		SaveRecent(recentAddress(msg.Endpoint, msg.Session), msg.Endpoint)
		return nil, nil, true

	case tea.WindowSizeMsg:
		sm.termWidth = msg.Width
		sm.termHeight = msg.Height
		return sm.forwardToActiveSession(msg)

	case RawInputMsg:
		return sm.handleRawInput(msg)

	default:
		return sm.forwardToActiveSession(msg)
	}
}

func (sm *SessionManagerLayer) handleRawInput(raw RawInputMsg) (tea.Msg, tea.Cmd, bool) {
	s := sm.activeSessionLayer()
	if s == nil {
		return nil, nil, true
	}
	_, cmd := s.handleRawInput([]byte(raw))
	if cmd != nil {
		return nil, cmd, true
	}
	return nil, nil, true
}

func (sm *SessionManagerLayer) forwardToActiveSession(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	s := sm.activeSessionLayer()
	if s == nil {
		return nil, nil, true
	}
	resp, cmd, _ := s.Update(msg)

	// If the active session lost all its regions, remove it.
	if len(s.tabs) == 0 && s.status == "no regions" {
		s.Deactivate()
		sm.sessions = slices.Delete(sm.sessions, sm.activeSession, sm.activeSession+1)
		if len(sm.sessions) == 0 {
			sm.activeSession = 0
			return resp, cmd, true
		}
		if sm.activeSession >= len(sm.sessions) {
			sm.activeSession = len(sm.sessions) - 1
		}
		sm.activeSessionLayer().Reconnect()
	}

	return resp, cmd, true
}

func (sm *SessionManagerLayer) handleCmd(msg SessionManagerCmd) (tea.Msg, tea.Cmd, bool) {
	push := func(l layer.Layer[RenderState]) (tea.Msg, tea.Cmd, bool) {
		return nil, func() tea.Msg { return PushLayerMsg{Layer: l} }, true
	}
	switch msg.Name {
	case "open-session":
		recents := LoadRecents()
		return push(NewConnectLayer(recents))
	case "close-session":
		return sm.killSession()
	case "next-session":
		sm.nextSession()
		return nil, nil, true
	case "prev-session":
		sm.prevSession()
		return nil, nil, true
	case "switch-session":
		if msg.Args == "" {
			return nil, sm.openSessionPicker(), true
		}
		idx, err := strconv.Atoi(msg.Args)
		if err == nil && idx > 0 {
			sm.switchSession(idx - 1)
		}
		return nil, nil, true
	case "show-status":
		caps := StatusCaps{
			Hostname:           sm.localHostname,
			Endpoint:           sm.endpoint,
			Version:            sm.version,
			ConnStatus:         sm.connStatus,
			ClientUpgradeAvail: sm.upgradeClientAvail,
			ClientUpgradeVer:   sm.upgradeClientVer,
			ServerUpgradeAvail: sm.upgradeServerAvail,
			ServerUpgradeVer:   sm.upgradeServerVer,
		}
		if s := sm.activeSessionLayer(); s != nil {
			caps.SessionName = s.sessionName
			if t := s.activeTerm(); t != nil {
				caps.KeyboardFlags = t.KeyboardFlags()
				caps.BgDark = t.BgDark()
				caps.TermEnv = t.TermEnv()
				caps.MouseModes = t.MouseModes()
				caps.Modes = t.Modes()
			}
		}
		var tree *protocol.Tree
		if sm.treeStore.Valid() {
			t := sm.treeStore.Tree()
			tree = &t
		}
		sl := NewStatusLayer(caps, tree)
		return push(sl)
	default:
		return nil, nil, true
	}
}

func (sm *SessionManagerLayer) killSession() (tea.Msg, tea.Cmd, bool) {
	s := sm.activeSessionLayer()
	if s == nil {
		return nil, nil, true
	}
	s.KillAllRegions()
	s.Deactivate()
	sm.sessions = slices.Delete(sm.sessions, sm.activeSession, sm.activeSession+1)
	if len(sm.sessions) == 0 {
		sm.activeSession = 0
		return nil, nil, true
	}
	if sm.activeSession >= len(sm.sessions) {
		sm.activeSession = len(sm.sessions) - 1
	}
	sm.activeSessionLayer().Reconnect()
	return nil, nil, true
}

func (sm *SessionManagerLayer) switchSession(idx int) {
	if idx < 0 || idx >= len(sm.sessions) || idx == sm.activeSession {
		return
	}
	sm.Deactivate()
	sm.activeSession = idx
	sm.activeSessionLayer().Reconnect()
}

func (sm *SessionManagerLayer) nextSession() {
	if len(sm.sessions) <= 1 {
		return
	}
	sm.switchSession((sm.activeSession + 1) % len(sm.sessions))
}

func (sm *SessionManagerLayer) prevSession() {
	if len(sm.sessions) <= 1 {
		return
	}
	sm.switchSession((sm.activeSession - 1 + len(sm.sessions)) % len(sm.sessions))
}

func (sm *SessionManagerLayer) openSessionPicker() tea.Cmd {
	var sessions []sessionInfo
	for i, s := range sm.sessions {
		sessions = append(sessions, sessionInfo{
			name:        s.sessionName,
			regionCount: len(s.tabs),
			active:      i == sm.activeSession,
		})
	}
	picker := NewSessionPickerLayer(sessions)
	return func() tea.Msg { return PushLayerMsg{Layer: picker} }
}

func (sm *SessionManagerLayer) viewportHeight() int {
	h := sm.termHeight - 1 - sm.statusBarMargin
	if h < 1 {
		h = 1
	}
	return h
}

// View renders the tab bar and terminal content, or a no-session pattern.
func (sm *SessionManagerLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	if sm.err != "" {
		return []*lipgloss.Layer{lipgloss.NewLayer("error: " + sm.err + "\n")}
	}
	s := sm.activeSessionLayer()
	if s != nil {
		return s.View(width, height, rs)
	}
	return sm.viewNoSession(width, height)
}

func (sm *SessionManagerLayer) viewNoSession(width, height int) []*lipgloss.Layer {
	width = max(width, 80)
	height = max(height, 24)

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

func (sm *SessionManagerLayer) WantsKeyboardInput() bool { return false }

func (sm *SessionManagerLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	if sm.connStatus == "reconnecting" {
		secs := int(time.Until(sm.retryAt).Seconds()) + 1
		return fmt.Sprintf("reconnecting to %s in %ds...", sm.endpoint, secs), statusBoldRed
	}

	var text string
	var style lipgloss.Style

	s := sm.activeSessionLayer()
	if s == nil {
		text, style = "no session", statusFaint
	} else {
		text, style = s.Status(rs)
	}

	if sm.upgradeServerAvail || sm.upgradeClientAvail {
		text += " | update available (ctrl+b u)"
	}
	return text, style
}

// SetRetryAt updates the reconnect retry time for status display.
func (sm *SessionManagerLayer) SetRetryAt(t time.Time) {
	sm.retryAt = t
}

// CheckForUpgrades sends an upgrade check request via the task system.
func (sm *SessionManagerLayer) CheckForUpgrades() {
	sm.tasks.Run(func(h *layer.Handle[RenderState]) {
		t := &TermdHandle{Handle: h}
		resp, err := t.Request(protocol.UpgradeCheckRequest{
			ClientVersion: sm.version,
			OS:            runtime.GOOS,
			Arch:          runtime.GOARCH,
		})
		if err != nil {
			return
		}
		if ucr, ok := resp.(protocol.UpgradeCheckResponse); ok && !ucr.Error {
			sm.program.Send(upgradeInfoMsg{
				ServerAvailable: ucr.ServerAvailable,
				ServerVersion:   ucr.ServerVersion,
				ClientAvailable: ucr.ClientAvailable,
				ClientVersion:   ucr.ClientVersion,
			})
		}
	})
}

// Sessions returns the session list (used by lifecycle methods).
func (sm *SessionManagerLayer) Sessions() []*SessionLayer {
	return sm.sessions
}

// SetServer updates the server reference (used after reconnect/connect).
func (sm *SessionManagerLayer) SetServer(s *Server) {
	sm.server = s
}
