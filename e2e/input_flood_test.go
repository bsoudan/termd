package e2e

import (
	"bytes"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nxtermd/internal/nxtest"
)

// TestInputNotStarvedByFlood asserts that a keystroke reaches the
// server within a bounded window while the frontend is being flooded
// with sustained terminal output. Exercises fairness between
// srv.Inbound and rawCh in internal/tui/mainlayer.go's Run loop and
// the batch cap in dispatchInbound.
//
// Flood strategy: the driver alternates Output() bursts with
// WriteSync() markers. The sync marker is not a ptyDataMsg, so the
// actor's coalesce loop breaks on it and issues a separate broadcast
// per burst. This produces many medium-sized broadcasts back-to-back
// so the frontend's srv.Inbound stays continuously populated — the
// exact condition the bug triggers on.
func TestInputNotStarvedByFlood(t *testing.T) {
	t.Parallel()

	// Large writeCh cap so the server doesn't drop broadcasts on
	// backpressure — the bug is about the frontend's event loop, not
	// server-side drops.
	socketPath, cleanup := startServerTinyWriteCh(t, 16384)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-flood", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-flood")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")
	region.DrainInput()

	// Each burst → a broadcast of ~450 events. WriteSync after each
	// burst breaks the actor's coalesce so the next burst gets its
	// own broadcast. Small bursts keep broadcasts arriving back-to-back.
	unit := []byte("\x1b[31mA\x1b[32mB\x1b[33mC\x1b[34mD\x1b[0mE\x1b[1mF\x1b[0mG\x1b[7mH\x1b[0mI\r\n")
	burst := bytes.Repeat(unit, 50)

	var stopped atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; !stopped.Load(); i++ {
			region.Output(burst)
			region.WriteSync(fmt.Sprintf("f-%d", i))
		}
	}()
	defer func() {
		stopped.Store(true)
		<-done
	}()

	// Let the flood establish a sustained stream.
	time.Sleep(1 * time.Second)

	// Verify the frontend actually renders the flood rather than freezing.
	// Sample the screen at the end of the warm-up and after a short
	// additional interval; the virtual screen should be showing the
	// flood content and advancing.
	preSample := nxterm.ScreenLines()
	preHasFlood := false
	for _, l := range preSample {
		if bytes.Contains([]byte(l), []byte("ABCDEFGHI")) {
			preHasFlood = true
			break
		}
	}
	if !preHasFlood {
		t.Fatalf("flood not visible on screen during sustained output\nscreen:\n%s", strings.Join(preSample, "\n"))
	}

	start := time.Now()
	nxterm.Write([]byte("k"))

	const budget = 200 * time.Millisecond
	deadline := time.After(10 * time.Second)
	for {
		select {
		case data := <-region.Input():
			if !bytes.Contains(data, []byte("k")) {
				continue
			}
			elapsed := time.Since(start)
			if elapsed > budget {
				t.Fatalf("keystroke took %v under flood (want < %v)", elapsed, budget)
			}
			t.Logf("keystroke landed in %v", elapsed)
			return
		case <-deadline:
			t.Fatal("keystroke never reached server under flood")
		}
	}
}
