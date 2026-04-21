package tui

import (
	"bytes"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
	"nxtermd/pkg/layer"
)

// NxtermModel owns the event loop, command mode, connection lifecycle,
// and global commands. It is NOT a layer — it sits above the stack
// and dispatches messages into it. SessionManagerLayer at the base
// of the stack handles session management and terminal routing.
type NxtermModel struct {
	server   *Server
	pipeW    io.Writer
	registry *Registry

	logRing   *LogRingBuffer
	version   string
	changelog string

	treeStore *TreeStore
	tasks     *layer.TaskRunner[RenderState]

	termWidth  int
	termHeight int

	connectFn func(endpoint, session string) // dials a server and sends ConnectedMsg

	// Command mode: active after the prefix key is pressed, buffering
	// chord keys until a match or mismatch is found.
	commandMode   bool
	commandBuffer []string

	// stack is the layer stack. SessionManagerLayer is at index 0.
	stack *layer.Stack[RenderState]

	// sm is the session manager at the base of the stack.
	sm *SessionManagerLayer

	// nextReqID and pendingReplies track protocol request/response
	// matching for task goroutines.
	nextReqID      uint64
	pendingReplies map[uint64]uint64 // reqID → taskID

	// Detached is set by the detach command to signal the main loop
	// to print "detached" after shutdown.
	Detached bool

	// program and rawCh are set by Run and used by the event loop.
	program *tea.Program
	rawCh   <-chan RawInputMsg

	// focusBuf holds raw input buffered for one-at-a-time sequence
	// processing when a focus-routing layer is active.
	focusBuf []byte

	// pendingSyncAcks holds sync marker ids stripped from input that
	// need their ack emitted after the next render. Emitting to stderr
	// before render causes the test harness to see the ack before the
	// rendered frame, yielding stale ScreenLines() on assertion.
	pendingSyncAcks []string

	// sessionPaused mirrors the server goroutine's paused state, updated
	// from PausedMsg/ResumedMsg on srv.Lifecycle. Read from View() via
	// RenderState.SessionPaused to drive the UI indicators.
	sessionPaused bool
}

// AppContext holds application-level metadata and services shared
// across the layer stack. These are set once at startup and never change.
type AppContext struct {
	Version         string
	Changelog       string
	Endpoint        string
	SessionName     string
	StatusBarMargin int
	LogRing         *LogRingBuffer
}

// NewNxtermModel creates the top-level application model.
func NewNxtermModel(
	server *Server, pipeW io.Writer, registry *Registry,
	app AppContext,
	connectFn func(endpoint, session string),
) *NxtermModel {
	hostname, _ := os.Hostname()
	treeStore := &TreeStore{}
	tasks := layer.NewTaskRunner[RenderState]()

	sm := NewSessionManagerLayer(server, registry, treeStore, tasks, app.Endpoint, app.Version, hostname, app.SessionName, app.StatusBarMargin)

	m := &NxtermModel{
		server:         server,
		pipeW:          pipeW,
		registry:       registry,
		logRing:        app.LogRing,
		version:        app.Version,
		changelog:      app.Changelog,
		connectFn:      connectFn,
		treeStore:      treeStore,
		tasks:          tasks,
		pendingReplies: make(map[uint64]uint64),
		sm:             sm,
	}

	m.stack = layer.NewStack[RenderState](sm)
	return m
}

// flushPendingSyncAcks writes any queued sync ack markers to the
// program's output stream. Forces a synchronous renderer flush first
// so the ack arrives in the PTY after the frame it's acknowledging.
// Skipped while the focus buffer has pending input — acks must only
// fire after all prior input has been fully processed.
func (m *NxtermModel) flushPendingSyncAcks() {
	if len(m.pendingSyncAcks) == 0 || len(m.focusBuf) > 0 {
		return
	}
	if err := m.program.Flush(); err != nil {
		return
	}
	for _, id := range m.pendingSyncAcks {
		_, _ = m.program.Write([]byte(FormatSyncAck(id)))
	}
	m.pendingSyncAcks = nil
}

// enterCommandMode is called when the prefix key is detected.
func (m *NxtermModel) enterCommandMode() {
	m.commandMode = true
	m.commandBuffer = m.commandBuffer[:0]
}

func (m *NxtermModel) exitCommandMode() {
	m.commandMode = false
	m.commandBuffer = m.commandBuffer[:0]
}

// rawSeqToChordKey converts a single raw terminal token to a chord key
// string for trie matching.
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

// handleCommandInput processes raw bytes while in command mode.
func (m *NxtermModel) handleCommandInput(raw []byte) tea.Cmd {
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
	}
	return nil
}

func resendRemainder(raw []byte, pos int) tea.Cmd {
	if pos >= len(raw) {
		return nil
	}
	rest := make([]byte, len(raw)-pos)
	copy(rest, raw[pos:])
	return func() tea.Msg { return RawInputMsg(rest) }
}

// init returns the initial command. If disconnected, pushes the connect
// overlay; otherwise starts the hint timer. Called by Init in model.go.
func (m *NxtermModel) init() tea.Cmd {
	if len(m.sm.sessions) == 0 {
		recents := LoadRecents()
		return func() tea.Msg {
			return PushLayerMsg{Layer: NewConnectLayer(recents)}
		}
	}
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} })
}

func (m *NxtermModel) quit() (tea.Msg, tea.Cmd) {
	m.sm.Deactivate()
	return nil, tea.Quit
}

func (m *NxtermModel) detach() (tea.Msg, tea.Cmd) {
	m.sm.Deactivate()
	m.Detached = true
	return nil, tea.Quit
}

func (m *NxtermModel) handleCmd(msg MainCmd) (tea.Msg, tea.Cmd, bool) {
	push := func(l layer.Layer[RenderState]) (tea.Msg, tea.Cmd, bool) {
		return nil, func() tea.Msg { return PushLayerMsg{Layer: l} }, true
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
	case "pause-session":
		m.server.Pause()
		return nil, nil, true
	case "resume-session":
		m.server.Resume()
		return nil, nil, true
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
			m.program.Send(upgradeInfoMsg{
				ServerAvailable: ucr.ServerAvailable,
				ServerVersion:   ucr.ServerVersion,
				ClientAvailable: ucr.ClientAvailable,
				ClientVersion:   ucr.ClientVersion,
			})

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

// ── Event loop ──────────────────────────────────────────────────────────

func (m *NxtermModel) Run(p *tea.Program, rawCh <-chan RawInputMsg,
	dialFn func(string) (net.Conn, error), connected bool) error {

	if err := p.Start(); err != nil {
		return err
	}

	m.program = p
	m.sm.SetProgram(p)
	m.rawCh = rawCh

	if connected {
		m.initialSetup()
	} else {
		m.connectOverlay(dialFn)
	}
	m.sm.CheckForUpgrades()

	// Cap on TerminalEvents processed between forced yields to rawCh.
	// Under sustained flood, the priority drain would otherwise keep
	// picking srv.Inbound without ever reaching the blocking select
	// that reads keyboard input. When the budget runs out we skip the
	// priority drain so the blocking select — which includes rawCh —
	// gets a fair shot.
	const serverWorkBudget = 2048
	eventBudget := serverWorkBudget

	// dirty tracks whether any state change since the last render needs
	// to reach the screen. p.Render() rebuilds the whole view tree, so
	// calling it once per priority-drain cycle (instead of per message)
	// keeps view-build cost bounded when a flood produces many small
	// batches. Bubbletea's renderer only draws on its fps tick anyway,
	// so coalescing here costs nothing on the output path.
	dirty := false
	renderIfDirty := func() {
		if dirty {
			p.Render()
			m.flushPendingSyncAcks()
			dirty = false
		}
	}

	for {
		srv := m.server

		// Priority phase: non-blocking drain of bubbletea, server,
		// and lifecycle messages before touching raw input. Skipped
		// when the server-work budget is exhausted so rawCh isn't
		// starved by back-to-back TerminalEvents batches.
		if eventBudget > 0 {
			select {
			case msg := <-p.Msgs():
				if _, err := p.Handle(msg); err != nil {
					p.Stop(nil)
					return nil
				}
				dirty = true
				continue
			case msg := <-srv.Inbound:
				eventBudget -= inboundEventCost(msg)
				m.processServerMsg(msg)
				dirty = true
				continue
			case msg := <-srv.Lifecycle:
				switch msg := msg.(type) {
				case DisconnectedMsg:
					m.reconnectLoop(msg)
				case PausedMsg:
					m.sessionPaused = true
				case ResumedMsg:
					m.sessionPaused = false
				}
				dirty = true
				continue
			case <-p.Context().Done():
				p.Stop(nil)
				return nil
			default:
			}
		}

		// About to block (or run a focus step) — flush any pending
		// render so the screen reflects the drained state.
		renderIfDirty()

		// Process one buffered focus-mode sequence before blocking.
		if len(m.focusBuf) > 0 {
			if err := m.stepFocusSequence(); err != nil {
				p.Stop(nil)
				return nil
			}
			p.Render()
			// flushPendingSyncAcks no-ops while focusBuf is non-empty;
			// the ack only goes out once the buffer fully drains.
			m.flushPendingSyncAcks()
			continue
		}

		// Nothing pending (or budget exhausted) — block on all channels
		// including raw input. rawCh reaching here also refills the
		// server-work budget.
		select {
		case msg := <-p.Msgs():
			if _, err := p.Handle(msg); err != nil {
				p.Stop(nil)
				return nil
			}
			dirty = true

		case raw := <-rawCh:
			eventBudget = serverWorkBudget
			cmd, err := m.processRawInput(raw)
			if err != nil {
				p.Stop(nil)
				return nil
			}
			if err := m.execCmdSync(cmd); err != nil {
				p.Stop(nil)
				return nil
			}
			dirty = true

		case msg := <-srv.Inbound:
			eventBudget -= inboundEventCost(msg)
			m.processServerMsg(msg)
			dirty = true

		case msg := <-srv.Lifecycle:
			switch msg := msg.(type) {
			case DisconnectedMsg:
				m.reconnectLoop(msg)
			case PausedMsg:
				m.sessionPaused = true
			case ResumedMsg:
				m.sessionPaused = false
			}
			dirty = true

		case <-p.Context().Done():
			p.Stop(nil)
			return nil
		}
	}
}

func (m *NxtermModel) stepFocusSequence() error {
	if !needsFocusRouting(m.stack) {
		rest := m.focusBuf
		m.focusBuf = nil
		if len(rest) > 0 {
			cmd, err := m.processRawInput(RawInputMsg(rest))
			if err != nil {
				return err
			}
			return m.execCmdSync(cmd)
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

// inboundEventCost returns how many terminal events a server message
// contributes to the event-loop work budget. Only TerminalEvents
// messages cost more than 1; everything else counts as a single unit.
func inboundEventCost(msg protocol.Message) int {
	if te, ok := msg.Payload.(protocol.TerminalEvents); ok {
		return len(te.Events)
	}
	return 1
}

// processRawInput handles raw terminal input and returns any command
// that needs further processing. Callers pass the returned cmd to
// execCmdSync (or the trampoline queue).
func (m *NxtermModel) processRawInput(raw RawInputMsg) (tea.Cmd, error) {
	// Strip OSC 2459 sync markers — they are test-only signals, not
	// terminal input. Queue acks to be emitted after the next render;
	// emitting inline would beat the render to the PTY and tests would
	// see the ack before the frame it's meant to signal. Surviving
	// bytes (if any) proceed through the normal raw-input path. This
	// runs here rather than at the rawCh select site because multiple
	// code paths read from rawCh (drainUntil and the main Run loop),
	// and sync support must work from all of them.
	remaining, ids := ExtractSyncMarkers([]byte(raw))
	if len(ids) > 0 {
		m.pendingSyncAcks = append(m.pendingSyncAcks, ids...)
		if len(remaining) == 0 {
			return nil, nil
		}
		raw = RawInputMsg(remaining)
	}
	if m.commandMode {
		return m.handleCommandInput([]byte(raw)), nil
	}
	if needsFocusRouting(m.stack) {
		m.focusBuf = append(m.focusBuf, raw...)
		return nil, nil
	}
	if idx := bytes.IndexByte([]byte(raw), m.registry.PrefixKey); idx >= 0 {
		if idx > 0 {
			m.stack.Update(RawInputMsg(raw[:idx]))
		}
		m.enterCommandMode()
		if rest := raw[idx+1:]; len(rest) > 0 {
			return m.handleCommandInput(rest), nil
		}
		return nil, nil
	}
	return m.stack.Update(RawInputMsg(raw)), nil
}

func (m *NxtermModel) execCmdSync(cmd tea.Cmd) error {
	// Trampoline: process commands iteratively to avoid unbounded
	// recursion through execCmdSync → processRawInput → execCmdSync.
	var queue []tea.Cmd
	if cmd != nil {
		queue = append(queue, cmd)
	}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		msg := c()
		if msg == nil {
			continue
		}
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case RawInputMsg:
			cmd, err := m.processRawInput(msg)
			if err != nil {
				return err
			}
			if cmd != nil {
				queue = append(queue, cmd)
			}
		case tea.QuitMsg:
			return tea.ErrProgramQuit
		case MainCmd:
			resp, next, _ := m.handleCmd(msg)
			if _, ok := resp.(tea.QuitMsg); ok {
				return tea.ErrProgramQuit
			}
			if next != nil {
				queue = append(queue, next)
			}
		case SyncMsg:
			m.pendingSyncAcks = append(m.pendingSyncAcks, msg.ID)
		default:
			if next := m.stack.Update(msg); next != nil {
				queue = append(queue, next)
			}
		}
	}
	return nil
}

func (m *NxtermModel) processServerMsg(msg protocol.Message) {
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
		changed := TreeChangedMsg{Tree: m.treeStore.Tree()}
		m.tasks.CheckFilters(changed)
		m.stack.Update(changed)
		return
	case protocol.TreeEvents:
		if !m.treeStore.HandleEvents(tmsg) {
			m.server.Send(protocol.Tagged(protocol.TreeResyncRequest{}))
			return
		}
		changed := TreeChangedMsg{Tree: m.treeStore.Tree()}
		m.tasks.CheckFilters(changed)
		m.stack.Update(changed)
		return
	}

	// Check task filters (Subscribe + WaitFor) before layer dispatch.
	if m.tasks.CheckFilters(msg.Payload) {
		return
	}

	cmd := m.stack.Update(msg.Payload)
	if cmd != nil {
		_ = m.execCmdSync(cmd)
	}
}

func (m *NxtermModel) drainUntil(match func(source string, msg any) bool) (any, error) {
	for {
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
			cmd, err := m.processRawInput(raw)
			if err != nil {
				return nil, err
			}
			if err := m.execCmdSync(cmd); err != nil {
				return nil, err
			}
			m.program.Render()
			m.flushPendingSyncAcks()
			if match("raw", raw) {
				return raw, nil
			}

		case msg := <-m.server.Inbound:
			m.processServerMsg(msg)
			m.program.Render()
			m.flushPendingSyncAcks()
			if match("server", msg) {
				return msg, nil
			}

		case msg := <-m.server.Lifecycle:
			// Keep sessionPaused mirror current even while drainUntil
			// is consuming lifecycle events (e.g., during reconnect).
			// Without this, a Pause issued during reconnect would be
			// silently dropped by the predicate and the indicator
			// would stay off.
			switch msg.(type) {
			case PausedMsg:
				m.sessionPaused = true
			case ResumedMsg:
				m.sessionPaused = false
			}
			m.tasks.CheckFilters(msg)
			if match("lifecycle", msg) {
				return msg, nil
			}

		case <-m.program.Context().Done():
			return nil, m.program.Context().Err()
		}
	}
}

func (m *NxtermModel) initialSetup() {
	// Block until bubbletea's initial tea.WindowSizeMsg is delivered
	// before we process any server traffic or send our first request.
	// The TreeSnapshot that the server sends on connect carries the
	// region list; processing it spawns TerminalLayers whose first
	// screen_update sizes the local hscreen from termHeight. If we
	// processed TreeSnapshot with termHeight still zero, the fallback
	// would create a mis-sized hscreen and every subsequent event
	// replay would drift from the server's region parser by one row
	// per batch. Server messages queue in server.Inbound (cap 256)
	// until the main loop picks them up.
	for {
		select {
		case msg := <-m.program.Msgs():
			processed, err := m.program.Handle(msg)
			if err != nil {
				return
			}
			if _, ok := processed.(tea.WindowSizeMsg); ok {
				goto ready
			}
		case <-m.program.Context().Done():
			return
		}
	}
ready:
	sessions := m.sm.Sessions()
	if len(sessions) == 0 {
		return
	}
	sess := sessions[0]
	sess.server.Send(protocol.SessionConnectRequest{
		Session: sess.sessionName,
		Width:   uint16(m.termWidth),
		Height:  uint16(m.sm.viewportHeight()),
	})
}

func (m *NxtermModel) reconnectLoop(initial DisconnectedMsg) {
	m.sm.SetRetryAt(initial.RetryAt)
	m.stack.Update(SetConnStatusMsg{Status: "reconnecting"})
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
			m.sm.SetRetryAt(msg.RetryAt)
		case ReconnectedMsg:
			close(tickDone)
			m.stack.Update(ReconnectAllMsg{})
			m.sm.CheckForUpgrades()
			return
		}
	}
}

func (m *NxtermModel) connectOverlay(dialFn func(string) (net.Conn, error)) {
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
		m.sm.SetServer(newSrv)
		m.stack.Update(ConnectedMsg{
			Endpoint: connectMsg.Endpoint,
			Session:  connectMsg.Session,
			Server:   newSrv,
		})

		SaveRecent(recentAddress(connectMsg.Endpoint, connectMsg.Session), connectMsg.Endpoint)
		return
	}
}
