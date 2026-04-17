package server

// native_backend.go implements regionBackend for native regions: a Region
// whose data source is a connected protocol client (the "driver") rather
// than a PTY + child process. Output bytes are pushed from the driver via
// NativeRegionOutput; subscriber input is forwarded to the driver via
// NativeInput.

import (
	"encoding/base64"
	"fmt"
	"os"
	"sync"

	"nxtermd/internal/protocol"
)

// nativeBackend is a regionBackend whose data source is a driver Client.
type nativeBackend struct {
	id     string
	driver *Client

	mu       sync.Mutex
	msgs     chan<- regionMsg
	done     chan struct{}
	stopped  bool
}

func newNativeBackend(id string, driver *Client) *nativeBackend {
	return &nativeBackend{
		id:     id,
		driver: driver,
		done:   make(chan struct{}),
	}
}

// Feed pushes raw output bytes from the driver into the region's VT parser
// by sending them to the actor as a ptyDataMsg. Safe to call from any
// goroutine.
func (b *nativeBackend) Feed(data []byte) {
	b.mu.Lock()
	msgs := b.msgs
	stopped := b.stopped
	b.mu.Unlock()
	if msgs == nil || stopped {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case msgs <- ptyDataMsg{data: cp}:
	case <-b.done:
	}
}

// DriverDisconnected is invoked when the driver's connection closes. It
// pushes a childExitedMsg so the actor destroys the region.
func (b *nativeBackend) DriverDisconnected() {
	b.mu.Lock()
	msgs := b.msgs
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.stopped = true
	b.mu.Unlock()
	if msgs == nil {
		return
	}
	select {
	case msgs <- childExitedMsg{}:
	case <-b.done:
	}
}

// Driver returns the client that owns this native region.
func (b *nativeBackend) Driver() *Client { return b.driver }

// ── regionBackend implementation ─────────────────────────────────────────────

func (b *nativeBackend) Start(msgs chan<- regionMsg, actorDone <-chan struct{}) {
	b.mu.Lock()
	b.msgs = msgs
	b.mu.Unlock()
	go func() {
		<-actorDone
		close(b.done)
	}()
}

func (b *nativeBackend) WriteInput(data []byte) {
	b.driver.SendMessage(protocol.NativeInput{
		Type:     "native_input",
		RegionID: b.id,
		Data:     base64.StdEncoding.EncodeToString(data),
	})
}

func (b *nativeBackend) Resize(rows, cols uint16) error {
	// No kernel resize for native regions; the actor still updates its
	// screen. The driver can watch the tree for size changes if it
	// needs to react.
	return nil
}

func (b *nativeBackend) SaveTermios()    {}
func (b *nativeBackend) RestoreTermios() {}

func (b *nativeBackend) Stop() error {
	b.mu.Lock()
	b.stopped = true
	b.mu.Unlock()
	return nil
}

func (b *nativeBackend) ResumeReader() error {
	b.mu.Lock()
	b.stopped = false
	b.mu.Unlock()
	return nil
}

func (b *nativeBackend) Close() error { return nil }
func (b *nativeBackend) Kill()        {}

func (b *nativeBackend) DetachForUpgrade() (*os.File, error) {
	return nil, fmt.Errorf("native regions cannot be transferred across upgrade")
}

func (b *nativeBackend) Done() <-chan struct{} { return b.done }
