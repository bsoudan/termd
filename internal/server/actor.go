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
	"strconv"
	"time"

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

	// Counters exposed via region_stats_request. Actor-owned so no
	// atomics or mutexes are needed — every writer and reader goes
	// through the actor goroutine.
	stats regionStats

	// Channels.
	msgs      chan regionMsg
	actorDone chan struct{}
	stopped   bool
}

// regionStats mirrors protocol.RegionStats; kept separate so the
// server package doesn't import-reference protocol types in the actor
// struct definition.
type regionStats struct {
	scrollbackQueries uint64
	droppedBroadcasts uint64
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
		Lines:           lines,
		CursorRow:       uint16(a.screen.Cursor.Row),
		CursorCol:       uint16(a.screen.Cursor.Col),
		Cells:           cells,
		Modes:           modes,
		Title:           a.screen.Title,
		IconName:        a.screen.IconName,
		ScrollbackLen:   a.hscreen.Scrollback(),
		ScrollbackTotal: a.hscreen.TotalAdded(),
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

// behindTimeout is how long a subscriber can stay marked "behind"
// before the broadcast loop disconnects it. Drops recover on the
// next broadcast after writeCh drains; a client stuck for this long
// is assumed to be dead or dangerously slow and is shed so the rest
// of the system isn't dragged down. Tests that need to exercise the
// circuit breaker can shorten it via NXTERMD_BEHIND_TIMEOUT_MS so they
// don't have to idle for a multi-second wall clock window.
var behindTimeout = resolveBehindTimeout()

func resolveBehindTimeout() time.Duration {
	if s := os.Getenv("NXTERMD_BEHIND_TIMEOUT_MS"); s != "" {
		if ms, err := strconv.Atoi(s); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 5 * time.Second
}

// broadcastToSubscribers sends terminal updates to all subscribers.
// Implements the slow-client recovery dance (A+B+C):
//
//   A. After any drop on a subscriber, send a ScrollbackDesync-flagged
//      ScreenUpdate on every subsequent broadcast until it lands. The
//      snapshot carries the current state, so once delivered the
//      client is immediately back in sync; drops in-between are
//      therefore self-healing without per-event replay.
//   B. The snapshot is only serialized lazily — if the subscriber's
//      writeCh is still full, skip the work and try again next round.
//   C. If the subscriber stays behind past behindTimeout, disconnect;
//      the client will reconnect and resync via the normal subscribe
//      path.
//
// The regular message (events, snapshot, or sync-only) is attempted
// on every subscriber regardless of behind state. Events applied to a
// stale hscreen are harmless because the catchup ScreenUpdate that
// follows rebuilds from cells, overwriting whatever the events did.
// Sync markers ride the regular broadcast path and therefore also
// drop when writeCh is full — tests that need sync delivery across a
// sustained backpressure window must retry.
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
		a.deliverBroadcast(snapMsg, func() any {
			s := newScreenUpdate(a.id, a.compositedSnapshot())
			s.ScrollbackDesync = true
			return s
		})
		a.broadcastSyncs(syncs)
		return
	}

	if needsSnapshot {
		snapMsg := newScreenUpdate(a.id, a.snapshot())
		a.deliverBroadcast(snapMsg, func() any {
			s := newScreenUpdate(a.id, a.snapshot())
			s.ScrollbackDesync = true
			return s
		})
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
	a.deliverBroadcast(msg, func() any {
		s := newScreenUpdate(a.id, a.snapshot())
		s.ScrollbackDesync = true
		return s
	})
}

// deliverBroadcast sends msg to each subscriber and, when the regular
// send succeeds against a subscriber that is currently behind,
// additionally sends a catchup ScreenUpdate (lazily built via
// catchupBuilder) to clear the behind state. Maintains the per-client
// behind flag and the circuit breaker. Increments droppedBroadcasts
// on drops.
//
// We intentionally never send the catchup ahead of the regular msg, nor
// in the same broadcast that just dropped a regular msg: the catchup
// would steal writeCh slots from subsequent regular msgs (sync markers,
// event batches), deadlocking the test harness on sync-ack timeouts.
// Instead, we wait for the regular msg to land — which implies writeCh
// has drained enough for the catchup to fit too — then piggyback the
// catchup. If the regular msg drops, behind stays set; the next broadcast
// retries naturally.
func (a *regionActor) deliverBroadcast(msg any, catchupBuilder func() any) {
	var catchup any
	for _, c := range a.subscribers {
		if !c.SendMessage(msg) {
			a.stats.droppedBroadcasts++
			if c.behind.CompareAndSwap(false, true) {
				c.behindSinceNanos.Store(time.Now().UnixNano())
			}
			// Circuit breaker (C). Use behindSinceNanos as the one-shot
			// latch too: CAS it to 0 so follow-up broadcasts during the
			// async close → removeSubscriber window don't re-log.
			since := c.behindSinceNanos.Load()
			if since > 0 && time.Since(time.Unix(0, since)) > behindTimeout {
				if c.behindSinceNanos.CompareAndSwap(since, 0) {
					slog.Warn("client stuck behind — disconnecting",
						"region_id", a.id, "client_id", c.id)
					c.Close()
				}
			}
			continue
		}

		if !c.behind.Load() {
			continue
		}

		// Regular send succeeded on a behind client — writeCh has some
		// room, so try to land the catchup too. Lazy (B): skip if
		// writeCh is now full from this send.
		if !c.WriteChHasRoom() {
			continue
		}
		if catchup == nil {
			catchup = catchupBuilder()
		}
		if c.SendMessage(catchup) {
			c.behind.Store(false)
			c.behindSinceNanos.Store(0)
		}
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
	a.deliverBroadcast(msg, func() any {
		s := newScreenUpdate(a.id, a.snapshot())
		s.ScrollbackDesync = true
		return s
	})
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

type ScrollbackResult struct {
	Lines [][]protocol.ScreenCell
	Total uint64
}

type scrollbackMsg struct{ resp chan ScrollbackResult }

func (m scrollbackMsg) handleRegion(a *regionActor) {
	a.stats.scrollbackQueries++
	m.resp <- ScrollbackResult{
		Lines: a.getScrollback(),
		Total: a.hscreen.TotalAdded(),
	}
}

type regionStatsMsg struct{ resp chan regionStats }

func (m regionStatsMsg) handleRegion(a *regionActor) {
	m.resp <- a.stats
}

// readRegionStats fetches the actor's counters, returning a zero-valued
// protocol.RegionStats if the actor has already stopped.
func readRegionStats(a *regionActor) protocol.RegionStats {
	resp := make(chan regionStats, 1)
	select {
	case a.msgs <- regionStatsMsg{resp: resp}:
	case <-a.actorDone:
		return protocol.RegionStats{}
	}
	select {
	case s := <-resp:
		return protocol.RegionStats{
			ScrollbackQueries: s.scrollbackQueries,
			DroppedBroadcasts: s.droppedBroadcasts,
		}
	case <-a.actorDone:
		return protocol.RegionStats{}
	}
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
	// Route through HistoryScreen.Resize so rows that no longer fit on
	// the shrunken viewport scroll into scrollback instead of being
	// discarded. Screen.Resize alone DeleteLines-es them into oblivion.
	a.hscreen.Resize(int(m.height), int(m.width))
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
