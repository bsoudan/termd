// Package nxtest provides a reusable test harness for driving nxterm/nxtermd
// in a PTY. It includes PtyIO (virtual screen over a PTY), ServerProcess,
// Frontend, config helpers, and a T type that wraps *testing.T for
// ergonomic e2e test code.
package nxtest

import (
	"testing"
	"time"
)

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
