package server

// actor.go implements the per-region actor goroutine. Each region runs a
// single actor goroutine that owns all mutable screen state, subscribers,
// and overlay data. No mutex is needed — all access is serialized through
// the actor's message channel. The data source is abstracted by
// regionBackend, so PTY-backed and native (driver-backed) regions share
// the same actor implementation.

import (
	"log/slog"
	"os"

	te "nxtermd/pkg/te"
	"nxtermd/internal/protocol"
)

// regionMsg is the interface all actor messages implement.
type regionMsg interface {
	handleRegion(a *regionActor)
}

// regionBackend is the data source for a region. PTY-backed regions use
// ptyBackend; native (driver-backed) regions will use nativeBackend.
type regionBackend interface {
	// Start spawns any backend goroutines. Output flows into msgs as
	// ptyDataMsg; backend termination flows as childExitedMsg.
	Start(msgs chan<- regionMsg, actorDone <-chan struct{})

	// WriteInput writes client-sourced input to the backend. Thread-safe;
	// called from the region's WriteInput without going through the actor.
	WriteInput(data []byte)

	// Resize updates backend sizing.
	Resize(rows, cols uint16) error

	// SaveTermios / RestoreTermios capture backend-specific terminal
	// mode state around overlays. Native backends should no-op.
	SaveTermios()
	RestoreTermios()

	// Stop interrupts the backend's read path so actor shutdown can
	// proceed. Used by live upgrade.
	Stop() error

	// ResumeReader reopens the backend's read path after Stop. Used by
	// live-upgrade rollback.
	ResumeReader() error

	// Close releases backend resources.
	Close() error

	// Kill signals the backend's child process if any. No-op for native.
	Kill()

	// DetachForUpgrade returns a file handle for transfer to a new
	// server process. Returns an error for backends that can't be
	// transferred (e.g. native).
	DetachForUpgrade() (*os.File, error)

	// Done is closed when the backend's read path has exited.
	Done() <-chan struct{}
}

const actorChanSize = 256

// regionActor owns all mutable state for a single region.
type regionActor struct {
	// Identity — immutable after construction.
	id        string
	backend   regionBackend
	destroyFn func(regionID string)

	// Mutable state — accessed only by the actor goroutine.
	width   int
	height  int
	screen  *te.Screen
	hscreen *te.HistoryScreen
	proxy   *EventProxy
	stream  *te.Stream

	subscribers map[uint32]*Client
	overlay     *overlayState

	// Channels.
	msgs      chan regionMsg
	actorDone chan struct{}
	stopped   bool
}

func newRegionActor(
	id string, backend regionBackend,
	width, height int, hscreen *te.HistoryScreen,
	destroyFn func(string),
) *regionActor {
	proxy := NewEventProxy(hscreen)
	stream := te.NewStream(proxy, false)
	return &regionActor{
		id:          id,
		backend:     backend,
		destroyFn:   destroyFn,
		width:       width,
		height:      height,
		screen:      hscreen.Screen,
		hscreen:     hscreen,
		proxy:       proxy,
		stream:      stream,
		subscribers: make(map[uint32]*Client),
		msgs:        make(chan regionMsg, actorChanSize),
		actorDone:   make(chan struct{}),
	}
}

func (a *regionActor) start() {
	a.backend.Start(a.msgs, a.actorDone)
	go a.run()
}

func (a *regionActor) run() {
	defer close(a.actorDone)
	for {
		msg, ok := <-a.msgs
		if !ok {
			return
		}
		msg.handleRegion(a)
		if a.stopped {
			return
		}
	}
}

// snapshot returns the current screen state without overlay compositing.
func (a *regionActor) snapshot() Snapshot {
	display := a.screen.Display()
	lines := make([]string, a.height)
	for i := 0; i < a.height; i++ {
		if i < len(display) {
			lines[i] = padLine(display[i], a.width)
		} else {
			lines[i] = blankLine(a.width)
		}
	}

	numRows := a.height
	if numRows > len(a.screen.Buffer) {
		numRows = len(a.screen.Buffer)
	}
	cells := make([][]protocol.ScreenCell, numRows)
	for row := 0; row < numRows; row++ {
		srcRow := a.screen.Buffer[row]
		cells[row] = make([]protocol.ScreenCell, len(srcRow))
		for col, c := range srcRow {
			cells[row][col] = cellToProtocol(c)
		}
	}

	var modes map[int]bool
	if len(a.screen.Mode) > 0 {
		modes = make(map[int]bool, len(a.screen.Mode))
		for k := range a.screen.Mode {
			modes[k] = true
		}
	}

	return Snapshot{
		Lines:         lines,
		CursorRow:     uint16(a.screen.Cursor.Row),
		CursorCol:     uint16(a.screen.Cursor.Col),
		Cells:         cells,
		Modes:         modes,
		Title:         a.screen.Title,
		IconName:      a.screen.IconName,
		ScrollbackLen: a.hscreen.Scrollback(),
	}
}

// compositedSnapshot returns the snapshot composited with the overlay if active.
func (a *regionActor) compositedSnapshot() Snapshot {
	snap := a.snapshot()
	if a.overlay != nil {
		snap = compositeSnapshot(snap, a.overlay)
	}
	return snap
}

func (a *regionActor) getScrollback() [][]protocol.ScreenCell {
	history := a.hscreen.History()
	if len(history) == 0 {
		return nil
	}
	cells := make([][]protocol.ScreenCell, len(history))
	for i, row := range history {
		last := len(row) - 1
		for last >= 0 {
			c := row[last]
			if c.Data != "" && c.Data != " " && c.Data != "\x00" {
				break
			}
			if c.Attr != (te.Attr{}) {
				break
			}
			last--
		}
		trimmed := row[:last+1]
		cells[i] = make([]protocol.ScreenCell, len(trimmed))
		for j, c := range trimmed {
			cells[i][j] = cellToProtocol(c)
		}
	}
	return cells
}

// broadcastToSubscribers sends terminal updates to all subscribers.
func (a *regionActor) broadcastToSubscribers() {
	events, needsSnapshot, syncs := a.proxy.Flush()
	if !needsSnapshot && len(events) == 0 && len(syncs) == 0 {
		return
	}
	if len(a.subscribers) == 0 {
		return
	}

	// When an overlay is active, always send a composited snapshot
	// instead of raw events.
	if a.overlay != nil {
		snapMsg := newScreenUpdate(a.id, a.compositedSnapshot())
		for _, c := range a.subscribers {
			c.SendMessage(snapMsg)
		}
		a.broadcastSyncs(syncs)
		return
	}

	if needsSnapshot {
		snapMsg := newScreenUpdate(a.id, a.snapshot())
		for _, c := range a.subscribers {
			c.SendMessage(snapMsg)
		}
		a.broadcastSyncs(syncs)
		return
	}

	// Combine events and syncs into one TerminalEvents message so they
	// arrive in order on the client.
	combined := events
	for _, id := range syncs {
		combined = append(combined, protocol.TerminalEvent{Op: "sync", Data: id})
	}
	msg := protocol.TerminalEvents{
		Type:     "terminal_events",
		RegionID: a.id,
		Events:   combined,
	}
	for _, c := range a.subscribers {
		c.SendMessage(msg)
	}
}

func (a *regionActor) broadcastSyncs(syncs []string) {
	if len(syncs) == 0 {
		return
	}
	syncEvents := make([]protocol.TerminalEvent, len(syncs))
	for i, id := range syncs {
		syncEvents[i] = protocol.TerminalEvent{Op: "sync", Data: id}
	}
	msg := protocol.TerminalEvents{
		Type:     "terminal_events",
		RegionID: a.id,
		Events:   syncEvents,
	}
	for _, c := range a.subscribers {
		c.SendMessage(msg)
	}
}

// clearOverlayInternal clears the overlay and sends a plain snapshot to subscribers.
func (a *regionActor) clearOverlayInternal() {
	a.overlay = nil
	a.backend.RestoreTermios()
	slog.Info("overlay cleared", "region_id", a.id)
	if len(a.subscribers) > 0 {
		snapMsg := newScreenUpdate(a.id, a.snapshot())
		for _, c := range a.subscribers {
			c.SendMessage(snapMsg)
		}
	}
}

// ── Actor message types and handlers ─────────────────────────────────────────

type ptyDataMsg struct{ data []byte }

func (m ptyDataMsg) handleRegion(a *regionActor) {
	a.stream.FeedBytes(m.data)
	// Coalesce: drain pending ptyDataMsg entries before broadcasting.
	for {
		select {
		case next := <-a.msgs:
			if pd, ok := next.(ptyDataMsg); ok {
				a.stream.FeedBytes(pd.data)
				continue
			}
			a.broadcastToSubscribers()
			next.handleRegion(a)
			return
		default:
			a.broadcastToSubscribers()
			return
		}
	}
}

type childExitedMsg struct{}

func (m childExitedMsg) handleRegion(a *regionActor) {
	a.broadcastToSubscribers()
	if a.destroyFn != nil {
		a.destroyFn(a.id)
	}
	a.stopped = true
}

// addSubscriberMsg adds a client to the subscriber set and returns
// the initial composited snapshot. The screen_update is sent to the
// client inside the handler, before the subscriber is added, to
// guarantee ordering relative to subsequent terminal_events.
type addSubscriberMsg struct {
	client *Client
	resp   chan Snapshot
}

func (m addSubscriberMsg) handleRegion(a *regionActor) {
	snap := a.compositedSnapshot()
	m.client.SendMessage(newScreenUpdate(a.id, snap))
	// Deliver the region's accumulated sync marker history so the new
	// subscriber can catch up on markers emitted before it joined. Sent
	// as a terminal_events follow-up ordered behind the screen_update
	// so the client's TerminalLayer sees them in order.
	if syncs := a.proxy.AllSyncs(); len(syncs) > 0 {
		events := make([]protocol.TerminalEvent, len(syncs))
		for i, id := range syncs {
			events[i] = protocol.TerminalEvent{Op: "sync", Data: id}
		}
		m.client.SendMessage(protocol.TerminalEvents{
			Type:     "terminal_events",
			RegionID: a.id,
			Events:   events,
		})
	}
	a.subscribers[m.client.id] = m.client
	m.resp <- snap
}

type removeSubscriberMsg struct{ clientID uint32 }

func (m removeSubscriberMsg) handleRegion(a *regionActor) {
	delete(a.subscribers, m.clientID)
	if a.overlay != nil && a.overlay.clientID == m.clientID {
		a.clearOverlayInternal()
	}
}

type snapshotMsg struct{ resp chan Snapshot }

func (m snapshotMsg) handleRegion(a *regionActor) {
	m.resp <- a.compositedSnapshot()
}

type scrollbackMsg struct{ resp chan [][]protocol.ScreenCell }

func (m scrollbackMsg) handleRegion(a *regionActor) {
	m.resp <- a.getScrollback()
}

type scrollbackLenMsg struct{ resp chan int }

func (m scrollbackLenMsg) handleRegion(a *regionActor) {
	m.resp <- a.hscreen.Scrollback()
}

type resizeMsg struct {
	width  uint16
	height uint16
	resp   chan error
}

func (m resizeMsg) handleRegion(a *regionActor) {
	if err := a.backend.Resize(m.height, m.width); err != nil {
		m.resp <- err
		return
	}
	a.screen.Resize(int(m.height), int(m.width))
	a.width = int(m.width)
	a.height = int(m.height)
	slog.Debug("region resized", "region_id", a.id, "width", m.width, "height", m.height)
	m.resp <- nil
}

type overlayRegisterMsg struct {
	client *Client
	resp   chan overlayRegisterResult
}

func (m overlayRegisterMsg) handleRegion(a *regionActor) {
	a.backend.SaveTermios()
	a.overlay = &overlayState{
		clientID: m.client.id,
		regionID: a.id,
	}
	slog.Info("overlay registered", "region_id", a.id, "client_id", m.client.id)
	m.resp <- overlayRegisterResult{width: a.width, height: a.height}
}

type overlayRenderMsg struct {
	clientID  uint32
	cells     [][]protocol.ScreenCell
	cursorRow uint16
	cursorCol uint16
	modes     map[int]bool
}

func (m overlayRenderMsg) handleRegion(a *regionActor) {
	ov := a.overlay
	if ov == nil || ov.clientID != m.clientID {
		return
	}
	ov.cells = m.cells
	ov.cursorRow = m.cursorRow
	ov.cursorCol = m.cursorCol
	ov.modes = m.modes
	snapMsg := newScreenUpdate(a.id, a.compositedSnapshot())
	for _, c := range a.subscribers {
		c.SendMessage(snapMsg)
	}
}

type overlayClearMsg struct{ clientID uint32 }

func (m overlayClearMsg) handleRegion(a *regionActor) {
	ov := a.overlay
	if ov == nil || ov.clientID != m.clientID {
		return
	}
	a.clearOverlayInternal()
}

// syncMarkerMsg asks the actor to emit a sync marker into the current
// batch, ordered after any already-received output. It is always
// followed by broadcastToSubscribers so pending output + marker flow
// out together.
type syncMarkerMsg struct{ id string }

func (m syncMarkerMsg) handleRegion(a *regionActor) {
	a.proxy.EmitSyncMarker(m.id)
	a.broadcastToSubscribers()
}

type stopActorMsg struct{ resp chan struct{} }

func (m stopActorMsg) handleRegion(a *regionActor) {
	a.backend.Stop()
	m.resp <- struct{}{}
	a.stopped = true
}
