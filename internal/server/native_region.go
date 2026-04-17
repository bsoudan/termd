package server

// native_region.go defines NativeRegion: a Region backed by a protocol
// client (the "driver") rather than a PTY. Shares the actor and
// EventProxy plumbing with PTYRegion; only the backend differs.

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	te "nxtermd/pkg/te"
	"nxtermd/internal/protocol"
)

// NativeRegion is a Region whose data source is a protocol-connected driver.
type NativeRegion struct {
	id      string
	name    string
	session string

	actor   *regionActor
	backend *nativeBackend

	width  atomic.Int32
	height atomic.Int32
}

func (r *NativeRegion) ID() string          { return r.id }
func (r *NativeRegion) Name() string        { return r.name }
func (r *NativeRegion) Cmd() string         { return "native:" + r.name }
func (r *NativeRegion) Pid() int            { return 0 }
func (r *NativeRegion) Session() string     { return r.session }
func (r *NativeRegion) SetSession(s string) { r.session = s }
func (r *NativeRegion) Width() int          { return int(r.width.Load()) }
func (r *NativeRegion) Height() int         { return int(r.height.Load()) }
func (r *NativeRegion) IsNative() bool      { return true }

// Driver returns the client that owns this region.
func (r *NativeRegion) Driver() *Client { return r.backend.Driver() }

// Feed pushes output bytes from the driver into the VT parser.
func (r *NativeRegion) Feed(data []byte) { r.backend.Feed(data) }

// DriverDisconnected is called when the driver's connection closes, to
// destroy the region via the actor's childExitedMsg path.
func (r *NativeRegion) DriverDisconnected() { r.backend.DriverDisconnected() }

func (r *NativeRegion) Snapshot() Snapshot {
	resp := make(chan Snapshot, 1)
	select {
	case r.actor.msgs <- snapshotMsg{resp: resp}:
	case <-r.actor.actorDone:
		return Snapshot{}
	}
	select {
	case snap := <-resp:
		return snap
	case <-r.actor.actorDone:
		return Snapshot{}
	}
}

func (r *NativeRegion) GetScrollback() [][]protocol.ScreenCell {
	resp := make(chan [][]protocol.ScreenCell, 1)
	select {
	case r.actor.msgs <- scrollbackMsg{resp: resp}:
	case <-r.actor.actorDone:
		return nil
	}
	select {
	case sb := <-resp:
		return sb
	case <-r.actor.actorDone:
		return nil
	}
}

func (r *NativeRegion) ScrollbackLen() int {
	resp := make(chan int, 1)
	select {
	case r.actor.msgs <- scrollbackLenMsg{resp: resp}:
	case <-r.actor.actorDone:
		return 0
	}
	select {
	case n := <-resp:
		return n
	case <-r.actor.actorDone:
		return 0
	}
}

func (r *NativeRegion) WriteInput(data []byte) {
	r.actor.backend.WriteInput(data)
}

func (r *NativeRegion) Resize(width, height uint16) error {
	resp := make(chan error, 1)
	select {
	case r.actor.msgs <- resizeMsg{width: width, height: height, resp: resp}:
	case <-r.actor.actorDone:
		return fmt.Errorf("region stopped")
	}
	select {
	case err := <-resp:
		if err == nil {
			r.width.Store(int32(width))
			r.height.Store(int32(height))
		}
		return err
	case <-r.actor.actorDone:
		return fmt.Errorf("region stopped")
	}
}

func (r *NativeRegion) Kill()  { r.DriverDisconnected() }
func (r *NativeRegion) Close() {}

func (r *NativeRegion) AddSubscriber(c *Client) Snapshot {
	resp := make(chan Snapshot, 1)
	select {
	case r.actor.msgs <- addSubscriberMsg{client: c, resp: resp}:
	case <-r.actor.actorDone:
		return Snapshot{}
	}
	select {
	case snap := <-resp:
		return snap
	case <-r.actor.actorDone:
		return Snapshot{}
	}
}

func (r *NativeRegion) RemoveSubscriber(clientID uint32) {
	select {
	case r.actor.msgs <- removeSubscriberMsg{clientID: clientID}:
	case <-r.actor.actorDone:
	}
}

func (r *NativeRegion) RegisterOverlay(client *Client) overlayRegisterResult {
	resp := make(chan overlayRegisterResult, 1)
	select {
	case r.actor.msgs <- overlayRegisterMsg{client: client, resp: resp}:
	case <-r.actor.actorDone:
		return overlayRegisterResult{err: "region stopped"}
	}
	select {
	case result := <-resp:
		return result
	case <-r.actor.actorDone:
		return overlayRegisterResult{err: "region stopped"}
	}
}

func (r *NativeRegion) RenderOverlay(clientID uint32, cells [][]protocol.ScreenCell, cursorRow, cursorCol uint16, modes map[int]bool) {
	select {
	case r.actor.msgs <- overlayRenderMsg{
		clientID: clientID, cells: cells,
		cursorRow: cursorRow, cursorCol: cursorCol, modes: modes,
	}:
	case <-r.actor.actorDone:
	}
}

func (r *NativeRegion) ClearOverlay(clientID uint32) {
	select {
	case r.actor.msgs <- overlayClearMsg{clientID: clientID}:
	case <-r.actor.actorDone:
	}
}

// ── Construction ─────────────────────────────────────────────────────────────

// NewNativeRegion creates a native region driven by the given client. The
// actor is started immediately; callers should add the returned region to
// the tree via the event loop.
func NewNativeRegion(driver *Client, name string, width, height int, destroyFn func(string)) *NativeRegion {
	id := generateUUID()
	backend := newNativeBackend(id, driver)

	hscreen := te.NewHistoryScreen(width, height, scrollbackSize)
	hscreen.Screen.WriteProcessInput = func(data string) {
		backend.WriteInput([]byte(data))
	}

	actor := newRegionActor(id, backend, width, height, hscreen, destroyFn)

	r := &NativeRegion{
		id:      id,
		name:    name,
		actor:   actor,
		backend: backend,
	}
	r.width.Store(int32(width))
	r.height.Store(int32(height))

	slog.Debug("created native region", "region_id", id, "name", name, "driver", driver.id)
	actor.start()

	return r
}
