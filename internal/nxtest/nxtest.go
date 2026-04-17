// Package nxtest provides a reusable test harness for driving nxterm/nxtermd
// in a PTY. It includes PtyIO (virtual screen over a PTY), ServerProcess,
// Frontend, config helpers, and a T type that wraps *testing.T for
// ergonomic e2e test code.
package nxtest

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// syncCounter is a shared auto-incrementing counter used by WriteHandle
// and RegionWriteHandle to generate unique sync ids without test code
// having to pick them. Shared across all test instances in the process.
var syncCounter atomic.Uint64

// nextSyncID returns a unique sync id.
func nextSyncID() string {
	return fmt.Sprintf("auto-%d", syncCounter.Add(1))
}

// WriteHandle is returned by T.Write. Its Sync method writes a sync
// marker to the TUI's stdin and blocks until the matching ack is
// observed on stdout — a convenient "process my writes and catch up"
// barrier without the test having to pick ids.
type WriteHandle struct {
	t *T
}

// Sync writes an auto-id sync marker onto the TUI's stdin and waits
// for the ack, ensuring all bytes queued before this Sync have been
// processed (and rendered) by the TUI before returning. desc is
// included in the failure message on timeout.
func (w *WriteHandle) Sync(desc string) {
	w.t.Helper()
	id := nextSyncID()
	w.t.PtyIO.WriteSync(id)
	if err := w.t.PtyIO.WaitSync(id, 10*time.Second); err != nil {
		w.t.Fatalf("sync %q: %v", desc, err)
	}
}

// T wraps a testing.T and a PtyIO (and optionally a Frontend) so that
// test code can use a single object for all interactions:
//
//	nxt.WaitFor("prompt$", 10*time.Second)
//	nxt.Write([]byte("echo hello\r"))
//	nxt.WaitForSilence(200 * time.Millisecond)
//	lines := nxt.ScreenLines()
type T struct {
	*testing.T
	*PtyIO
	Frontend *Frontend // nil when wrapping a bare PtyIO
}

// New wraps a bare PtyIO (no frontend process).
func New(t *testing.T, pio *PtyIO) *T {
	return &T{T: t, PtyIO: pio}
}

// NewFromFrontend wraps a Frontend (which embeds a PtyIO).
func NewFromFrontend(t *testing.T, fe *Frontend) *T {
	return &T{T: t, PtyIO: fe.PtyIO, Frontend: fe}
}

// WaitFor waits for needle to appear on the virtual screen.
// Calls t.Fatal on timeout or PTY close.
func (t *T) WaitFor(needle string, timeout time.Duration) []string {
	t.Helper()
	lines, err := t.PtyIO.WaitFor(needle, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// WaitForScreen waits for check to return true against the screen content.
// Calls t.Fatal on timeout or PTY close.
func (t *T) WaitForScreen(check func([]string) bool, desc string, timeout time.Duration) []string {
	t.Helper()
	lines, err := t.PtyIO.WaitForScreen(check, desc, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// FindOnScreen returns the row and column where needle first appears
// on the current screen, or (-1, -1).
func (t *T) FindOnScreen(needle string) (row, col int) {
	return FindOnScreen(t.ScreenLines(), needle)
}

// Write writes raw bytes to the TUI's stdin and returns a WriteHandle
// whose Sync method blocks until the TUI has processed them and
// rendered the result. Shadows the embedded PtyIO.Write so existing
// test code that discards the return value works unchanged.
func (t *T) Write(data []byte) *WriteHandle {
	t.Helper()
	t.PtyIO.Write(data)
	return &WriteHandle{t: t}
}

// Sync injects a sync marker without a preceding write and blocks
// until the TUI has rendered through everything currently queued on
// stdin. Useful after a loop of Write calls where only the final
// Sync matters. desc is included in the failure message on timeout.
func (t *T) Sync(desc string) {
	t.Helper()
	(&WriteHandle{t: t}).Sync(desc)
}

// WriteSync injects an OSC 2459;nx;sync;<id> marker into the TUI's
// stdin. The rawio path strips it and emits a SyncMsg in the TUI,
// FIFO-ordered with any other keystrokes sent before it.
func (t *T) WriteSync(id string) {
	t.Helper()
	t.PtyIO.WriteSync(id)
}

// WaitSync blocks until the TUI emits the matching OSC 2459;nx;ack;<id>
// on stdout. Calls t.Fatal on timeout. Default timeout is 10s.
func (t *T) WaitSync(id string) {
	t.Helper()
	if err := t.PtyIO.WaitSync(id, 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

// WaitSyncWithTimeout blocks until the matching ack is seen or timeout
// expires. Calls t.Fatal on timeout.
func (t *T) WaitSyncWithTimeout(id string, timeout time.Duration) {
	t.Helper()
	if err := t.PtyIO.WaitSync(id, timeout); err != nil {
		t.Fatal(err)
	}
}

// RequireTabBarContains asserts that the tab bar (row 0) contains
// want, failing the test otherwise.
func (t *T) RequireTabBarContains(want string) {
	t.Helper()
	screen := t.ScreenLines()
	if len(screen) == 0 || !strings.Contains(screen[0], want) {
		got := ""
		if len(screen) > 0 {
			got = screen[0]
		}
		t.Fatalf("expected tab bar to contain %q, got %q", want, got)
	}
}

// RequireTabBarDoesNotContain asserts that the tab bar does not contain
// unwanted, failing the test otherwise.
func (t *T) RequireTabBarDoesNotContain(unwanted string) {
	t.Helper()
	screen := t.ScreenLines()
	if len(screen) > 0 && strings.Contains(screen[0], unwanted) {
		t.Fatalf("expected tab bar to not contain %q, got %q", unwanted, screen[0])
	}
}

// Kill forcibly terminates the frontend process.
// Panics if T was created with New (no frontend).
func (t *T) Kill() {
	t.Frontend.Kill()
}

// Wait waits for the frontend process to exit.
// Panics if T was created with New (no frontend).
func (t *T) Wait(timeout time.Duration) error {
	return t.Frontend.Wait(timeout)
}
