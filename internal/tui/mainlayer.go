package tui

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
	"nxtermd/pkg/layer"
)

// MainLayer sits at index 0 in the layer stack and manages multiple
// SessionLayers. It intercepts session management commands and global
// messages, forwarding everything else to the active session.
//
// MainLayer owns all TUI state: the layer stack, request state, task
// runner, and connection lifecycle. The mainLoop drives it; bubbletea
// sees it through a thin teaModel adapter for Init/Update/View.
type MainLayer struct {
	server   *Server
	pipeW    io.Writer
	registry *Registry

	sessions      []*SessionLayer
	activeSession int

	logRing       *LogRingBuffer
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

	treeStore *TreeStore
	tasks     *layer.TaskRunner[RenderState]

	termWidth  int
	termHeight int

	connectFn   func(endpoint, session string) // dials a server and sends ConnectedMsg
	sessionName string                         // initial session name, used after deferred connect

	// Number of blank rows between the tab/status bar at row 0 and the
	// terminal content. Configured via FrontendConfig.StatusBarMargin.
	statusBarMargin int

	// Command mode: active after the prefix key is pressed, buffering
	// chord keys until a match or mismatch is found.
	commandMode   bool
	commandBuffer []string

	// stack is the layer stack that MainLayer sits at the bottom of.
	stack *layer.Stack[RenderState]

	// nextReqID and pendingReplies track protocol request/response
	// matching for task goroutines. When a task calls Handle.Send(),
	// the message is tagged with a req_id and the task_id is stored
	// in pendingReplies. When the server response arrives, the task
	// is delivered the payload.
	nextReqID      uint64
	pendingReplies map[uint64]uint64 // reqID → taskID

	// Detached is set by the detach command to signal the main loop
	// to print "detached" after shutdown.
	Detached bool

	// program and rawCh are set by Run and used by the event loop.
	program       *tea.Program
	rawCh         <-chan RawInputMsg

	// focusBuf holds raw input buffered for one-at-a-time sequence
	// processing when a focus-routing layer is active. Between each
	// sequence the main loop runs a priority drain so server and
	// bubbletea messages are never starved by a burst of keystrokes.
	focusBuf []byte
}

func NewMainLayer(
	server *Server, pipeW io.Writer, registry *Registry,
	logRing *LogRingBuffer,
	endpoint, version, changelog, sessionName string,
	statusBarMargin int,
	connectFn func(endpoint, session string),
) *MainLayer {
	hostname, _ := os.Hostname()

	m := &MainLayer{
		server:          server,
		pipeW:           pipeW,
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
		pendingReplies:  make(map[uint64]uint64),
	}

	m.treeStore = &TreeStore{}
	m.tasks = layer.NewTaskRunner[RenderState]()
	m.stack = layer.NewStack[RenderState](m)

	if endpoint != "" {
		session := NewSessionLayer(server, registry, m.treeStore, logRing, endpoint, version, changelog, hostname, sessionName, statusBarMargin)
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
	return nil, tea.Quit
}

func (m *MainLayer) detach() (tea.Msg, tea.Cmd) {
	m.Deactivate()
	m.Detached = true
	// Don't send protocol.Disconnect here — it races with the quit
	// by triggering a reconnect loop. The shutdown path in main.go
	// calls server.Close() which handles the disconnect cleanly.
	return nil, tea.Quit
}

func (m *MainLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	// ── Keybinding commands (MainCmd) ───────────────────────────────────

	case MainCmd:
		return m.handleCmd(msg)

	case SessionManagerCmd:
		return m.handleSessionManagerCmd(msg)

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
		m.endpoint = msg.Endpoint
		m.connStatus = "connected"
		// If the connect overlay specified a session, adopt it so
		// future reconnects target the same session.
		if msg.Session != "" {
			m.sessionName = msg.Session
		}
		session := NewSessionLayer(m.server, m.registry, m.treeStore, m.logRing, m.endpoint, m.version, m.changelog, m.localHostname, m.sessionName, m.statusBarMargin)
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
	push := func(layer layer.Layer[RenderState]) (tea.Msg, tea.Cmd, bool) {
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }, true
	}
	switch msg.Name {
	case "detach":
		resp, cmd := m.detach()
		return resp, cmd, true
	case "run-command":
		return push(NewCommandPaletteLayer(m.registry))
	case "show-help":
		return push(NewHelpLayer(m.registry))
	case "show-log":
		return push(NewScrollableLayer("logviewer", m.logRing.String(), true, m.logRing, m.termWidth, m.termHeight))
	case "show-release-notes":
		return push(NewScrollableLayer("release notes", strings.TrimRight(m.changelog, "\n"), false, nil, m.termWidth, m.termHeight))
	case "upgrade":
		m.tasks.Run(func(h *layer.Handle[RenderState]) {
			t := &TermdHandle{Handle: h}
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
	default:
		return nil, nil, true
	}
}

func (m *MainLayer) handleSessionManagerCmd(msg SessionManagerCmd) (tea.Msg, tea.Cmd, bool) {
	push := func(layer layer.Layer[RenderState]) (tea.Msg, tea.Cmd, bool) {
		return nil, func() tea.Msg { return PushLayerMsg{Layer: layer} }, true
	}
	switch msg.Name {
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
		m.tasks.Run(func(h *layer.Handle[RenderState]) {
			t := &TermdHandle{Handle: h}
			resp, err := t.Request(protocol.StatusRequest{})
			if err != nil {
				return
			}
			if sr, ok := resp.(protocol.StatusResponse); ok {
				sl.SetStatus(&sr)
			}
		})
		return push(sl)
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

func (m *MainLayer) WantsKeyboardInput() bool {
	return false
}

// UpgradeAvailable reports whether any upgrade is available.
func (m *MainLayer) UpgradeAvailable() bool {
	return m.upgradeServerAvail || m.upgradeClientAvail
}

func (m *MainLayer) checkForUpgrades() {
	m.tasks.Run(func(h *layer.Handle[RenderState]) {
		t := &TermdHandle{Handle: h}
		resp, err := t.Request(protocol.UpgradeCheckRequest{
			ClientVersion: m.version,
			OS:            runtime.GOOS,
			Arch:          runtime.GOARCH,
		})
		if err != nil {
			return
		}
		if ucr, ok := resp.(protocol.UpgradeCheckResponse); ok && !ucr.Error {
			m.upgradeServerAvail = ucr.ServerAvailable
			m.upgradeServerVer = ucr.ServerVersion
			m.upgradeClientAvail = ucr.ClientAvailable
			m.upgradeClientVer = ucr.ClientVersion
		}
	})
}

// ── Event loop ──────────────────────────────────────────────────────────

// Run is the top-level event loop. It replaces bubbletea's internal
// eventLoop with an explicit select over all message sources: bubbletea
// terminal events, raw stdin input, server protocol messages, and server
// lifecycle events. Everything runs on one goroutine.
func (m *MainLayer) Run(p *tea.Program, rawCh <-chan RawInputMsg,
	dialFn func(string) (net.Conn, error), connected bool) error {

	if err := p.Start(); err != nil {
		return err
	}

	m.program = p
	m.rawCh = rawCh

	if connected {
		m.initialSetup()
	} else {
		m.connectOverlay(dialFn)
	}
	m.checkForUpgrades()

	for {
		srv := m.server

		// Priority phase: handle bubbletea, server, and lifecycle
		// messages before touching raw input. Non-blocking — falls
		// through to the blocking select when nothing is ready.
		select {
		case msg := <-p.Msgs():
			if _, err := p.Handle(msg); err != nil {
				p.Stop(nil)
				return nil
			}
			continue
		case msg := <-srv.Inbound:
			m.processServerMsg(msg)
			p.Render()
			continue
		case msg := <-srv.Lifecycle:
			switch msg := msg.(type) {
			case DisconnectedMsg:
				m.reconnectLoop(msg)
			}
			continue
		case <-p.Context().Done():
			p.Stop(nil)
			return nil
		default:
		}

		// Process one buffered focus-mode sequence before blocking
		// for new input, so priority channels get checked between
		// every keystroke.
		if len(m.focusBuf) > 0 {
			if err := m.stepFocusSequence(); err != nil {
				p.Stop(nil)
				return nil
			}
			p.Render()
			continue
		}

		// Nothing pending — block on all channels including raw input.
		select {
		case msg := <-p.Msgs():
			if _, err := p.Handle(msg); err != nil {
				p.Stop(nil)
				return nil
			}

		case raw := <-rawCh:
			if err := m.processRawInput(raw); err != nil {
				p.Stop(nil)
				return nil
			}
			p.Render()

		case msg := <-srv.Inbound:
			m.processServerMsg(msg)
			p.Render()

		case msg := <-srv.Lifecycle:
			switch msg := msg.(type) {
			case DisconnectedMsg:
				m.reconnectLoop(msg)
			}

		case <-p.Context().Done():
			p.Stop(nil)
			return nil
		}
	}
}

// stepFocusSequence consumes one ANSI sequence from focusBuf and
// sends it through pipeW. If focus routing is no longer active (a
// layer was popped by a previous keystroke), the remaining buffer is
// re-processed as normal raw input. Returns an error if p.Handle
// fails (e.g. QuitMsg → ErrProgramQuit).
func (m *MainLayer) stepFocusSequence() error {
	if !needsFocusRouting(m.stack) {
		rest := m.focusBuf
		m.focusBuf = nil
		if len(rest) > 0 {
			return m.processRawInput(RawInputMsg(rest))
		}
		return nil
	}

	_, _, n, _ := ansi.DecodeSequence(m.focusBuf, ansi.NormalState, nil)
	if n <= 0 {
		n = len(m.focusBuf)
	}
	seq := make([]byte, n)
	copy(seq, m.focusBuf[:n])
	m.focusBuf = m.focusBuf[n:]

	// pipeW is a synchronous io.Pipe — the goroutine write blocks
	// until bubbletea reads from pipeR and queues a message.
	// We drain p.Msgs() to consume the key event and any follow-on
	// side effects. A short timeout is needed because p.msgs is
	// shared with timers and other async sources — the first message
	// we read may not be the key event from this write.
	go m.pipeW.Write(seq)
	msg := <-m.program.Msgs()
	if _, err := m.program.Handle(msg); err != nil {
		return err
	}
	for {
		select {
		case msg := <-m.program.Msgs():
			if _, err := m.program.Handle(msg); err != nil {
				return err
			}
		case <-time.After(time.Millisecond):
			return nil
		}
	}
}

// processRawInput handles raw bytes from stdin. Returns an error if
// a command produces a QuitMsg (the caller should exit).
func (m *MainLayer) processRawInput(raw RawInputMsg) error {
	if m.commandMode {
		cmd := m.handleCommandInput([]byte(raw))
		return m.execCmdSync(cmd)
	}
	if needsFocusRouting(m.stack) {
		m.focusBuf = append(m.focusBuf, raw...)
		return nil
	}
	if idx := bytes.IndexByte([]byte(raw), m.registry.PrefixKey); idx >= 0 {
		if idx > 0 {
			m.sendRawToServer(raw[:idx])
		}
		m.enterCommandMode()
		if rest := raw[idx+1:]; len(rest) > 0 {
			return m.execCmdSync(m.handleCommandInput(rest))
		}
		return nil
	}
	if s := m.activeSessionLayer(); s != nil {
		_, cmd := s.handleRawInput([]byte(raw))
		if cmd != nil {
			return m.execCmdSync(cmd)
		}
	}
	return nil
}

// execCmdSync executes a tea.Cmd synchronously. Returns an error if
// a QuitMsg is produced (the caller should exit the event loop).
func (m *MainLayer) execCmdSync(cmd tea.Cmd) error {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	// Handle batch commands (from handleCommandInput when there's
	// remaining input after the matched chord).
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if err := m.execCmdSync(c); err != nil {
				return err
			}
		}
		return nil
	}
	if raw, ok := msg.(RawInputMsg); ok {
		m.processRawInput(raw)
		return nil
	}
	if _, ok := msg.(tea.QuitMsg); ok {
		return tea.ErrProgramQuit
	}
	// Dispatch through the layer stack. Use stack.Update so
	// PushLayerMsg/popLayerMsg are handled at the stack level.
	nextCmd := m.stack.Update(msg)
	if nextCmd != nil {
		return m.execCmdSync(nextCmd)
	}
	return nil
}

// processServerMsg handles a protocol message from the server.
func (m *MainLayer) processServerMsg(msg protocol.Message) {
	if msg.ReqID > 0 {
		if taskID, ok := m.pendingReplies[msg.ReqID]; ok {
			delete(m.pendingReplies, msg.ReqID)
			m.tasks.Deliver(taskID, msg.Payload)
			return
		}
	}

	// Tree sync
	switch tmsg := msg.Payload.(type) {
	case protocol.TreeSnapshot:
		m.treeStore.HandleSnapshot(tmsg)
		m.stack.Update(TreeChangedMsg{Tree: m.treeStore.Tree()})
		return
	case protocol.TreeEvents:
		if !m.treeStore.HandleEvents(tmsg) {
			m.server.Send(protocol.Tagged(protocol.TreeResyncRequest{}))
			return
		}
		m.stack.Update(TreeChangedMsg{Tree: m.treeStore.Tree()})
		return
	}

	m.stack.Update(msg.Payload)
}

// drainUntil reads from all channels, processing each message through
// its handler, and returns when a message from any source matches the
// filter. Returns (nil, err) if p.Handle fails (e.g. QuitMsg) or the
// context is cancelled.
func (m *MainLayer) drainUntil(match func(source string, msg any) bool) (any, error) {
	for {
		// Process buffered focus-mode input before blocking, just like
		// the main event loop does. Without this, overlays that use
		// focus routing (ConnectLayer, etc.) would never receive input.
		if len(m.focusBuf) > 0 {
			if err := m.stepFocusSequence(); err != nil {
				return nil, err
			}
			m.program.Render()
			continue
		}

		select {
		case msg := <-m.program.Msgs():
			processed, err := m.program.Handle(msg)
			if err != nil {
				return nil, err
			}
			if processed != nil && match("tea", processed) {
				return processed, nil
			}

		case raw := <-m.rawCh:
			if err := m.processRawInput(raw); err != nil {
				return nil, err
			}
			m.program.Render()
			if match("raw", raw) {
				return raw, nil
			}

		case msg := <-m.server.Inbound:
			m.processServerMsg(msg)
			m.program.Render()
			if match("server", msg) {
				return msg, nil
			}

		case msg := <-m.server.Lifecycle:
			if match("lifecycle", msg) {
				return msg, nil
			}

		case <-m.program.Context().Done():
			return nil, m.program.Context().Err()
		}
	}
}

// initialSetup waits for the window size then sends SessionConnectRequest.
func (m *MainLayer) initialSetup() {
	if _, err := m.drainUntil(func(source string, msg any) bool {
		_, ok := msg.(tea.WindowSizeMsg)
		return source == "tea" && ok
	}); err != nil {
		return
	}

	if len(m.sessions) == 0 {
		return
	}
	sess := m.sessions[0]
	sess.server.Send(protocol.SessionConnectRequest{
		Session: sess.sessionName,
		Width:   uint16(m.termWidth),
		Height:  uint16(m.viewportHeight()),
	})
}

// reconnectLoop handles the reconnection lifecycle.
func (m *MainLayer) reconnectLoop(initial DisconnectedMsg) {
	m.connStatus = "reconnecting"
	m.retryAt = initial.RetryAt
	for _, s := range m.sessions {
		s.connStatus = "reconnecting"
	}
	m.program.Render()

	tickDone := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.program.Send(reconnectTickMsg{})
			case <-tickDone:
				return
			case <-m.program.Context().Done():
				return
			}
		}
	}()

	for {
		msg, err := m.drainUntil(func(source string, msg any) bool {
			if source != "lifecycle" {
				return false
			}
			switch msg.(type) {
			case DisconnectedMsg, ReconnectedMsg:
				return true
			}
			return false
		})

		if err != nil {
			close(tickDone)
			return
		}
		switch msg := msg.(type) {
		case DisconnectedMsg:
			m.retryAt = msg.RetryAt
		case ReconnectedMsg:
			close(tickDone)
			m.connStatus = "connected"
			m.retryAt = time.Time{}
			for _, s := range m.sessions {
				s.connStatus = "connected"
			}
			for _, s := range m.sessions {
				s.Reconnect()
			}
			if s := m.activeSessionLayer(); s != nil {
				s.Activate()
			}
			m.checkForUpgrades()
			return
		}
	}
}

// connectOverlay handles the initial disconnected-mode connect flow.
func (m *MainLayer) connectOverlay(dialFn func(string) (net.Conn, error)) {
	for {
		msg, err := m.drainUntil(func(source string, msg any) bool {
			if source != "tea" {
				return false
			}
			_, ok := msg.(ConnectToServerMsg)
			return ok
		})

		if err != nil {
			return
		}

		connectMsg := msg.(ConnectToServerMsg)

		conn, err := dialFn(connectMsg.Endpoint)
		if err != nil {
			m.program.Send(ConnectErrorMsg{
				Endpoint: connectMsg.Endpoint,
				Error:    err.Error(),
			})
			continue
		}
		conn = transport.WrapTracing(conn, "client")

		newSrv := NewServer(64, "nxterm")
		reconnDialFn := func() (net.Conn, error) {
			c, err := dialFn(connectMsg.Endpoint)
			if err != nil {
				return nil, err
			}
			return transport.WrapTracing(c, "client"), nil
		}
		go newSrv.Run(conn, reconnDialFn)

		m.server.Close()
		m.server = newSrv
		m.endpoint = connectMsg.Endpoint
		m.connStatus = "connected"

		if connectMsg.Session != "" {
			m.sessionName = connectMsg.Session
		}

		session := NewSessionLayer(newSrv, m.registry,
			m.treeStore, m.logRing, m.endpoint, m.version, m.changelog,
			m.localHostname, m.sessionName, m.statusBarMargin)
		session.termWidth = m.termWidth
		session.termHeight = m.termHeight
		m.sessions = []*SessionLayer{session}
		m.activeSession = 0
		session.server.Send(protocol.SessionConnectRequest{
			Session: session.sessionName,
			Width:   uint16(m.termWidth),
			Height:  uint16(m.viewportHeight()),
		})

		SaveRecent(recentAddress(connectMsg.Endpoint, connectMsg.Session), connectMsg.Endpoint)
		return
	}
}
