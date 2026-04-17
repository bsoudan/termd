// Package nxtest — driver.go: protocol client wrapper for driving native
// regions from tests. Wraps internal/client with *testing.T so helpers
// can t.Fatal directly; demultiplexes inbound messages onto per-region
// channels and an awaiting-spawn channel.
package nxtest

import (
	"encoding/base64"
	"fmt"
	"sync"
	"testing"
	"time"

	"nxtermd/internal/client"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
)

// Driver is a protocol client that spawns and drives native regions.
// Construct with DialDriver; clean up is registered via t.Cleanup.
type Driver struct {
	t *testing.T
	c *client.Client

	mu      sync.Mutex
	regions map[string]*NativeRegion

	spawnCh chan protocol.NativeRegionSpawnResponse
	done    chan struct{}
}

// NativeRegion is a handle to a native region spawned by the driver.
type NativeRegion struct {
	id     string
	name   string
	driver *Driver
	width  int
	height int
	input  chan []byte
}

// DialDriver connects to the given server socket as a protocol client,
// identifies as "nxtest-driver", and starts the message dispatcher.
// Registers t.Cleanup to close the connection.
func DialDriver(t *testing.T, socketPath string) *Driver {
	t.Helper()
	conn, err := transport.Dial("unix:" + socketPath)
	if err != nil {
		t.Fatalf("dial driver: %v", err)
	}
	c := client.New(conn)
	c.SendIdentify("nxtest-driver")
	d := &Driver{
		t:       t,
		c:       c,
		regions: make(map[string]*NativeRegion),
		spawnCh: make(chan protocol.NativeRegionSpawnResponse, 4),
		done:    make(chan struct{}),
	}
	go d.dispatchLoop()
	t.Cleanup(d.Close)
	return d
}

// Close shuts the connection and waits for the dispatcher to exit.
// Safe to call multiple times (cleanup registers one call).
func (d *Driver) Close() {
	d.c.Close()
	<-d.done
}

// SpawnNativeRegion creates a new native region in sessionName (created
// if missing) and returns a handle. Calls t.Fatal on error.
func (d *Driver) SpawnNativeRegion(sessionName, name string, width, height int) *NativeRegion {
	d.t.Helper()
	if err := d.c.Send(protocol.NativeRegionSpawnRequest{
		Type: "native_region_spawn_request", Session: sessionName, Name: name,
		Width: width, Height: height,
	}); err != nil {
		d.t.Fatalf("SpawnNativeRegion send: %v", err)
	}
	select {
	case resp := <-d.spawnCh:
		if resp.Error {
			d.t.Fatalf("SpawnNativeRegion: %s", resp.Message)
		}
		r := &NativeRegion{
			id:     resp.RegionID,
			name:   name,
			driver: d,
			width:  resp.Width,
			height: resp.Height,
			input:  make(chan []byte, 16),
		}
		d.mu.Lock()
		d.regions[resp.RegionID] = r
		d.mu.Unlock()
		return r
	case <-time.After(5 * time.Second):
		d.t.Fatal("timeout awaiting native_region_spawn_response")
		return nil
	}
}

func (d *Driver) dispatchLoop() {
	defer close(d.done)
	for msg := range d.c.Recv() {
		switch payload := msg.Payload.(type) {
		case protocol.NativeRegionSpawnResponse:
			select {
			case d.spawnCh <- payload:
			default:
			}
		case protocol.NativeInput:
			d.mu.Lock()
			r := d.regions[payload.RegionID]
			d.mu.Unlock()
			if r != nil {
				data, err := base64.StdEncoding.DecodeString(payload.Data)
				if err == nil && len(data) > 0 {
					select {
					case r.input <- data:
					default:
						// Tests that don't consume input won't block the
						// dispatcher. If capacity matters, use Drain.
					}
				}
			}
		}
	}
}

// ID returns the server-assigned region UUID.
func (r *NativeRegion) ID() string { return r.id }

// Name returns the region's display name.
func (r *NativeRegion) Name() string { return r.name }

// Width returns the region's width in columns.
func (r *NativeRegion) Width() int { return r.width }

// Height returns the region's height in rows.
func (r *NativeRegion) Height() int { return r.height }

// Output sends bytes into the region's VT parser server-side.
// Fire-and-forget — pair with WriteSync + nxtest.T.WaitSync to know
// when the bytes have been rendered on a subscribed TUI.
func (r *NativeRegion) Output(data []byte) {
	if err := r.driver.c.Send(protocol.NativeRegionOutput{
		Type: "native_region_output", RegionID: r.id,
		Data: base64.StdEncoding.EncodeToString(data),
	}); err != nil {
		r.driver.t.Fatalf("NativeRegion.Output: %v", err)
	}
}

// WriteSync tells the server to emit a sync marker into the region's
// terminal_events stream, ordered after any pending output. Subscribers
// see it as a TerminalEvent{Op:"sync", Data:id}. Pair with WaitSync on
// the TUI handle to barrier on "server processed and TUI rendered."
func (r *NativeRegion) WriteSync(id string) {
	if err := r.driver.c.Send(protocol.NativeRegionSync{
		Type: "native_region_sync", RegionID: r.id, ID: id,
	}); err != nil {
		r.driver.t.Fatalf("NativeRegion.WriteSync: %v", err)
	}
}

// Input returns a channel of input bytes forwarded from subscribed
// TUI clients. Drop semantics: if nobody reads, input is discarded.
func (r *NativeRegion) Input() <-chan []byte { return r.input }

// DrainInput reads all currently-buffered input bytes without blocking.
func (r *NativeRegion) DrainInput() []byte {
	var out []byte
	for {
		select {
		case b := <-r.input:
			out = append(out, b...)
		default:
			return out
		}
	}
}

// syncPayload returns the OSC 2459 sync sequence the TUI rawio expects.
// Used by nxtest.T.WriteSync.
func syncPayload(id string) []byte {
	return fmt.Appendf(nil, "\x1b]2459;nx;sync;%s\x07", id)
}
