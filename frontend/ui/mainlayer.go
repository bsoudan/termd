package ui

import (
	"fmt"
	"io"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	termlog "nxtermd/frontend/log"
	"nxtermd/frontend/protocol"
	"nxtermd/pkg/tui"
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

	// Upgrade state
	upgradeServerAvail  bool
	upgradeServerVer    string
	upgradeClientAvail  bool
	upgradeClientVer    string

	tasks *tui.TaskRunner[RenderState]

	termWidth  int
	termHeight int

	// pendingConnect is true when an initial SessionConnectRequest is
	// queued but not yet sent because we don't know the window size yet.
	// Cleared after the first WindowSizeMsg triggers the send.
	pendingConnect bool

	connectFn    func(endpoint, session string) // dials a server and sends ConnectedMsg
	sessionName  string                         // initial session name, used after deferred connect
	swapServerFn func(*Server)                  // updates the requestFn's server reference

	// Number of blank rows between the tab/status bar at row 0 and the
	// terminal content. Configured via FrontendConfig.StatusBarMargin.
	statusBarMargin int

	// Command mode: active after the prefix key is pressed, buffering
	// chord keys until a match or mismatch is found.
	commandMode   bool
	commandBuffer []string
}

func NewMainLayer(
	server *Server, pipeW io.Writer, requestFn RequestFunc, registry *Registry,
	logRing *termlog.LogRingBuffer,
	endpoint, version, changelog, hostname, sessionName string,
	statusBarMargin int,
	connectFn func(endpoint, session string),
) *MainLayer {
	m := &MainLayer{
		server:          server,
		pipeW:           pipeW,
		requestFn:       requestFn,
		registry:        registry,
		logRing:         logRing,
		localHostname:   hostname,
		endpoint:        endpoint,
		version:         version,
		changelog:       changelog,
		connStatus:      "connected",
		connectFn:       connectFn,
		sessionName:     sessionName,
		statusBarMargin: statusBarMargin,
	}
	if endpoint != "" {
		// Connected mode: create initial session immediately.
		session := NewSessionLayer(server, requestFn, registry, logRing, endpoint, version, changelog, hostname, sessionName, statusBarMargin)
		m.sessions = []*SessionLayer{session}
	}
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

// enterCommandMode is called when the prefix key is detected. It sets
// command mode so subsequent raw input is buffered and matched against
// the chord trie instead of being forwarded to the server.
func (m *MainLayer) enterCommandMode() {
	m.commandMode = true
	m.commandBuffer = m.commandBuffer[:0]
}

func (m *MainLayer) exitCommandMode() {
	m.commandMode = false
	m.commandBuffer = m.commandBuffer[:0]
}

// rawSeqToChordKey converts a single raw terminal token to a chord key
// string for trie matching. Printable bytes map to themselves ("d", "S"),
// ctrl+letter maps to "ctrl+x". Escape sequences and other bytes return
// "" (no match, exits command mode).
func rawSeqToChordKey(seq []byte) string {
	if len(seq) == 1 {
		b := seq[0]
		if b >= 0x20 && b <= 0x7e {
			return string(rune(b))
		}
		if b >= 1 && b <= 26 {
			return "ctrl+" + string(rune('a'+b-1))
		}
	}
	return ""
}

// handleCommandInput processes raw bytes while in command mode. It
// iterates one ANSI token at a time, converts each to a chord key,
// and checks the trie. On a full match the command is dispatched;
// on a prefix match we stay in command mode; otherwise we exit.
// Any unprocessed bytes after command mode exits are re-sent as a
// new RawInputMsg for normal processing.
func (m *MainLayer) handleCommandInput(raw []byte) tea.Cmd {
	pos := 0
	for pos < len(raw) {
		_, _, n, _ := ansi.DecodeSequence(raw[pos:], ansi.NormalState, nil)
		if n == 0 {
			break
		}
		seq := raw[pos : pos+n]
		pos += n

		key := rawSeqToChordKey(seq)
		if key == "" {
			m.exitCommandMode()
			return resendRemainder(raw, pos)
		}

		m.commandBuffer = append(m.commandBuffer, key)

		match, isPrefix := m.registry.MatchChord(m.commandBuffer)
		if match != nil && !isPrefix {
			m.exitCommandMode()
			cmd := cmdForBinding(match.command, match.args)
			if resend := resendRemainder(raw, pos); resend != nil {
				return tea.Batch(cmd, resend)
			}
			return cmd
		}
		if !isPrefix && match == nil {
			m.exitCommandMode()
			return resendRemainder(raw, pos)
		}
		// isPrefix → stay in command mode, wait for more keys
	}
	return nil
}

// resendRemainder returns a tea.Cmd that re-sends unprocessed bytes
// as a new RawInputMsg, or nil if there are none.
func resendRemainder(raw []byte, pos int) tea.Cmd {
	if pos >= len(raw) {
		return nil
	}
	rest := make([]byte, len(raw)-pos)
	copy(rest, raw[pos:])
	return func() tea.Msg { return RawInputMsg(rest) }
}

// Init delegates to the first session and starts the hint timer.
// If started in disconnected mode, pushes the connect overlay instead.
func (m *MainLayer) Init() tea.Cmd {
	if len(m.sessions) == 0 {
		recents := LoadRecents()
		return func() tea.Msg {
			return PushLayerMsg{Layer: NewConnectLayer(recents)}
		}
	}
	// Defer the SessionConnectRequest until the first WindowSizeMsg
	// arrives so we can pass the actual viewport size to the server. The
	// server uses these to size the PTYs of newly-spawned regions,
	// avoiding an initial 80x24 → real-size resize round trip.
	m.pendingConnect = true
	m.checkForUpgrades()
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} })
}

// viewportHeight returns the number of rows available for terminal content,
// i.e. the window height minus the tab bar and status-bar margin.
// Mirrors TerminalLayer.contentHeight.
func (m *MainLayer) viewportHeight() int {
	h := m.termHeight - 1 - m.statusBarMargin
	if h < 1 {
		h = 1
	}
	return h
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
	detachCmd := func() tea.Msg { return DetachMsg{} }
	return nil, tea.Batch(detachCmd, tea.Quit)
}

func (m *MainLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	// ── Keybinding commands (MainCmd) ───────────────────────────────────

	case MainCmd:
		return m.handleCmd(msg)

	// ── Connect overlay flow ────────────────────────────────────────────

	case ConnectToServerMsg:
		// Close the old server so its Run goroutine exits cleanly.
		m.server.Close()
		m.Deactivate()
		m.sessions = nil
		m.activeSession = 0
		m.connectFn(msg.Endpoint, msg.Session)
		return nil, nil, true

	case ConnectedMsg:
		m.server = msg.Server
		m.swapServerFn(msg.Server)
		m.endpoint = msg.Endpoint
		m.connStatus = "connected"
		// If the connect overlay specified a session, adopt it so
		// future reconnects target the same session.
		if msg.Session != "" {
			m.sessionName = msg.Session
		}
		session := NewSessionLayer(m.server, m.requestFn, m.registry, m.logRing, m.endpoint, m.version, m.changelog, m.localHostname, m.sessionName, m.statusBarMargin)
		session.termWidth = m.termWidth
		session.termHeight = m.termHeight
		m.sessions = []*SessionLayer{session}
		m.activeSession = 0
		session.server.Send(protocol.SessionConnectRequest{
			Session: session.sessionName,
			Width:   uint16(m.termWidth),
			Height:  uint16(m.viewportHeight()),
		})
		SaveRecent(recentAddress(msg.Endpoint, msg.Session), msg.Endpoint)
		m.checkForUpgrades()
		return nil, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} }), true

	case ConnectErrorMsg:
		return nil, nil, false // let ConnectLayer handle it

	case DiscoveredServerMsg:
		return nil, nil, false // let ConnectLayer handle it

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
		m.checkForUpgrades()
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
		if m.pendingConnect && len(m.sessions) > 0 && m.termWidth > 0 && m.termHeight > 1 {
			m.pendingConnect = false
			s := m.sessions[0]
			s.termWidth = m.termWidth
			s.termHeight = m.termHeight
			s.server.Send(protocol.SessionConnectRequest{
				Session: s.sessionName,
				Width:   uint16(m.termWidth),
				Height:  uint16(m.viewportHeight()),
			})
		}
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
	push := func(layer tui.Layer[RenderState]) (tea.Msg, tea.Cmd, bool) {
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }, true
	}
	switch msg.Name {

	// ── Session management ─────────────────────────────────────────────
	case "open-session":
		recents := LoadRecents()
		return push(NewConnectLayer(recents))
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

	case "upgrade":
		m.tasks.Run(func(h *tui.Handle[RenderState]) {
			t := &TermdHandle{Handle: h}
			// Fresh upgrade check.
			resp, err := t.Request(protocol.UpgradeCheckRequest{
				ClientVersion: m.version,
				OS:            runtime.GOOS,
				Arch:          runtime.GOARCH,
			})
			if err != nil {
				return
			}
			ucr, ok := resp.(protocol.UpgradeCheckResponse)
			if !ok || ucr.Error {
				return
			}
			m.upgradeServerAvail = ucr.ServerAvailable
			m.upgradeServerVer = ucr.ServerVersion
			m.upgradeClientAvail = ucr.ClientAvailable
			m.upgradeClientVer = ucr.ClientVersion

			if !ucr.ServerAvailable && !ucr.ClientAvailable {
				toast := &ToastLayer{id: nextToastID + 1, text: "Already up to date"}
				nextToastID++
				t.PushLayer(toast)
				time.Sleep(3 * time.Second)
				t.PopLayer(toast)
				return
			}
			upgradeTask(t, m.server,
				ucr.ServerAvailable, ucr.ServerVersion,
				ucr.ClientAvailable, ucr.ClientVersion, m.version)
		})
		return nil, nil, true

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
			Hostname:           m.localHostname,
			Endpoint:           m.endpoint,
			Version:            m.version,
			ConnStatus:         m.connStatus,
			ClientUpgradeAvail: m.upgradeClientAvail,
			ClientUpgradeVer:   m.upgradeClientVer,
			ServerUpgradeAvail: m.upgradeServerAvail,
			ServerUpgradeVer:   m.upgradeServerVer,
		}
		if s := m.activeSessionLayer(); s != nil {
			caps.SessionName = s.sessionName
			if t := s.activeTerm(); t != nil {
				caps.KeyboardFlags = t.KeyboardFlags()
				caps.BgDark = t.BgDark()
				caps.TermEnv = t.TermEnv()
				caps.MouseModes = t.MouseModes()
				caps.Modes = t.Modes()
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
func (m *MainLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	if m.err != "" {
		return []*lipgloss.Layer{lipgloss.NewLayer("error: " + m.err + "\n")}
	}
	s := m.activeSessionLayer()
	if s != nil {
		return s.View(width, height, rs)
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
func (m *MainLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	rs.CommandMode = m.commandMode
	if m.commandMode {
		if len(m.commandBuffer) > 0 {
			return "? " + strings.Join(m.commandBuffer, " "), commandModeStyle
		}
		return "?", commandModeStyle
	}

	if m.connStatus == "reconnecting" {
		secs := int(time.Until(m.retryAt).Seconds()) + 1
		return fmt.Sprintf("reconnecting to %s in %ds...", m.endpoint, secs), statusBoldRed
	}

	var text string
	var style lipgloss.Style

	s := m.activeSessionLayer()
	if s == nil {
		text, style = "no session", statusFaint
	} else {
		text, style = s.Status(rs)
	}

	if m.upgradeServerAvail || m.upgradeClientAvail {
		text += " | update available (ctrl+b u)"
	}
	return text, style
}

func (m *MainLayer) WantsKeyboardInput() *KeyboardFilter {
	if t := m.ActiveTerm(); t != nil && t.ScrollbackActive() {
		return allKeysFilter
	}
	return nil
}

// UpgradeAvailable reports whether any upgrade is available.
func (m *MainLayer) UpgradeAvailable() bool {
	return m.upgradeServerAvail || m.upgradeClientAvail
}

func (m *MainLayer) checkForUpgrades() {
	m.requestFn(protocol.UpgradeCheckRequest{
		ClientVersion: m.version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}, func(payload any) {
		if resp, ok := payload.(protocol.UpgradeCheckResponse); ok && !resp.Error {
			m.upgradeServerAvail = resp.ServerAvailable
			m.upgradeServerVer = resp.ServerVersion
			m.upgradeClientAvail = resp.ClientAvailable
			m.upgradeClientVer = resp.ClientVersion
		}
	})
}
