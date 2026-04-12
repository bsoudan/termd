package e2e

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestScrollbackBuffer(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Spawn a region and generate enough output to fill scrollback
	regionID := spawnRegion(t, socketPath, "shell")

	// Wait for shell prompt
	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()
	nxt.WaitFor("nxterm$",10*time.Second)

	// Output 200 lines — in a 24-row terminal, early lines scroll off
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$",10*time.Second)

	// Poll scrollback via nxtermctl until early numbers are present.
	// The server's terminal emulator may still be processing output
	// even after the frontend shows the prompt.
	want := []string{"1", "2", "3", "10", "50"}
	deadline := time.After(10 * time.Second)
	for {
		out := runNxtermctl(t, socketPath, "region", "scrollback", regionID)
		lines := strings.Split(strings.TrimSpace(out), "\n")

		found := make(map[string]bool)
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			for _, w := range want {
				if trimmed == w {
					found[w] = true
				}
			}
		}
		allFound := true
		for _, w := range want {
			if !found[w] {
				allFound = false
				break
			}
		}
		if allFound {
			return
		}

		select {
		case <-deadline:
			for _, w := range want {
				if !found[w] {
					t.Errorf("scrollback missing line %q (got %d lines total)", w, len(lines))
				}
			}
			return
		default:
			runtime.Gosched()
		}
	}
}

func TestScrollbackNavigation(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$",10*time.Second)

	// Generate enough output to fill scrollback
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$",10*time.Second)

	// Enter scrollback mode with ctrl+b [
	nxt.Write([]byte{0x02, '['})

	// Tab bar should show "scrollback"
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback indicator in tab bar", 5*time.Second)

	// Page up several times to reach early numbers
	for range 20 {
		nxt.Write([]byte{0x15}) // ctrl+u = page up
		time.Sleep(30 * time.Millisecond)
	}

	// Verify early numbers appear on screen.
	// Use Fields[0] to ignore the scrollbar column at the right edge.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] { // skip tab bar
			if fields := strings.Fields(line); len(fields) > 0 {
				if fields[0] == "1" || fields[0] == "2" || fields[0] == "3" {
					return true
				}
			}
		}
		return false
	}, "early numbers (1/2/3) visible on screen", 5*time.Second)

	// Exit scrollback with q
	nxt.Write([]byte("q"))

	// Tab bar should no longer show "scrollback" and prompt should be visible
	nxt.WaitForScreen(func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "nxterm$") {
				return true
			}
		}
		return false
	}, "prompt visible, scrollback gone from tab bar", 5*time.Second)
}

// TestScrollbackPageUpDown verifies that PageUp and PageDown keys activate
// scrollback mode directly from the terminal (without the prefix key).
func TestScrollbackPageUpDown(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate enough output to fill scrollback
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Send PageUp (\x1b[5~) — should activate scrollback
	nxt.Write([]byte("\x1b[5~"))

	// Tab bar should show "scrollback"
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback activated by PageUp", 5*time.Second)

	// Send more PageUp keys to scroll further back
	for range 20 {
		nxt.Write([]byte("\x1b[5~"))
		time.Sleep(30 * time.Millisecond)
	}

	// Verify early numbers appear on screen.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] {
			if fields := strings.Fields(line); len(fields) > 0 {
				if fields[0] == "1" || fields[0] == "2" || fields[0] == "3" {
					return true
				}
			}
		}
		return false
	}, "early numbers visible via PageUp", 5*time.Second)

	// Exit scrollback with q
	nxt.Write([]byte("q"))

	nxt.WaitForScreen(func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "nxterm$") {
				return true
			}
		}
		return false
	}, "prompt visible after scrollback exit", 5*time.Second)

	// Now test PageDown — should also activate scrollback (at offset 0)
	nxt.Write([]byte("\x1b[6~"))

	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback activated by PageDown", 5*time.Second)

	// Exit with q
	nxt.Write([]byte("q"))

	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited after PageDown test", 5*time.Second)
}

func TestScrollbackScrollWheel(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$",10*time.Second)

	// Generate output that scrolls off screen
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$",10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Send a scroll wheel up event to activate scrollback
	nxt.Write([]byte(fmt.Sprintf("%c[<64;5;5M", ansi.ESC)))

	// Wait for scrollback data to arrive (not just mode activation)
	nxt.WaitForScreen(func(lines []string) bool {
		// Tab bar should show scrollback with non-zero total
		return strings.Contains(lines[0], "scrollback") &&
			!strings.Contains(lines[0], "/0]")
	}, "scrollback data loaded", 5*time.Second)

	// Send more scroll wheel up events to scroll to the top
	for range 70 {
		nxt.Write([]byte(fmt.Sprintf("%c[<64;5;5M", ansi.ESC)))
		time.Sleep(20 * time.Millisecond)
	}

	// Verify early numbers appear on screen.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] {
			if fields := strings.Fields(line); len(fields) > 0 {
				if fields[0] == "1" || fields[0] == "2" || fields[0] == "3" {
					return true
				}
			}
		}
		return false
	}, "early numbers visible via scroll wheel", 5*time.Second)

	// Scroll wheel down past offset 0 to auto-exit scrollback
	for range 80 {
		nxt.Write([]byte(fmt.Sprintf("%c[<65;5;5M", ansi.ESC)))
		time.Sleep(20 * time.Millisecond)
	}

	// Verify scrollback exited and prompt is visible
	nxt.WaitForScreen(func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "nxterm$") {
				return true
			}
		}
		return false
	}, "prompt visible after scroll down exit", 5*time.Second)
}

// TestScrollbackLiveUpdate verifies that the client's scrollback view
// incorporates new output that arrives while the user is in scrollback
// mode, without requiring the user to exit and re-enter.
//
// This is the core scrollback desync bug: the client takes a snapshot
// on entry and never updates it. Once the client uses a local
// HistoryScreen, new lines should appear automatically.
func TestScrollbackLiveUpdate(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate initial output.
	nxt.Write([]byte("for i in $(seq 1 50); do echo \"FIRST_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Start a delayed background job.
	nxt.Write([]byte("(sleep 3; for i in $(seq 1 50); do echo \"SECOND_$i\"; done) &\r"))
	nxt.WaitFor("nxterm$", 5*time.Second)

	// Enter scrollback.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	// Record the initial scrollback total from the status bar.
	// Note: the HintLayer may overlay the status bar text with
	// "ctrl+b, ? for help", but scrollback is still active.
	screen := nxt.ScreenLines()
	t.Logf("initial: %s", strings.TrimSpace(screen[0]))

	// Wait for the SECOND_ output to arrive on the server.
	time.Sleep(5 * time.Second)

	// While still in scrollback, go to the bottom (offset=0).
	// In a correct implementation, SECOND_ lines should be visible.
	nxt.Write([]byte("G")) // end = go to bottom
	time.Sleep(200 * time.Millisecond)

	screen = nxt.ScreenLines()

	// At offset=0 the screen portion shows the live terminal which has
	// SECOND_ lines. But scroll up 1 step — now we should see the
	// transition from scrollback to screen. If the client's snapshot
	// is stale, there will be a gap or duplicate at the boundary.
	nxt.Write([]byte("k")) // up 1 line
	time.Sleep(200 * time.Millisecond)

	screen = nxt.ScreenLines()

	// Collect all visible numbered lines (FIRST_N or SECOND_N).
	var visible []string
	for _, line := range screen[1:] { // skip tab bar
		trimmed := strings.TrimSpace(line)
		// Strip scrollbar character from right edge.
		runes := []rune(trimmed)
		if len(runes) > 0 {
			last := runes[len(runes)-1]
			if last == '·' || last == '█' || last == '▲' || last == '▼' {
				trimmed = strings.TrimSpace(string(runes[:len(runes)-1]))
			}
		}
		if strings.HasPrefix(trimmed, "FIRST_") || strings.HasPrefix(trimmed, "SECOND_") {
			visible = append(visible, trimmed)
		}
	}

	// Check for duplicates or non-sequential ordering at the boundary.
	// In a correct view, the lines should be monotonically ordered with
	// no repeats. In the buggy view, lines from the stale scrollback
	// overlap or leave a gap with the live screen content.
	t.Logf("visible lines at offset=1: %v", visible)

	seen := make(map[string]int)
	for _, v := range visible {
		seen[v]++
	}
	hasDuplicate := false
	for k, count := range seen {
		if count > 1 {
			t.Errorf("duplicate line in scrollback view: %q appears %d times", k, count)
			hasDuplicate = true
		}
	}

	if hasDuplicate {
		t.Error("scrollback/screen boundary has duplicates — client snapshot is stale")
	}

	// Also check for gaps: if we see FIRST_N followed by SECOND_M,
	// there's a missing range of FIRST_ lines between N and 50.
	lastFirst := 0
	firstSecond := 0
	for _, v := range visible {
		var n int
		if _, err := fmt.Sscanf(v, "FIRST_%d", &n); err == nil {
			if n > lastFirst {
				lastFirst = n
			}
		}
		if firstSecond == 0 {
			if _, err := fmt.Sscanf(v, "SECOND_%d", &n); err == nil {
				firstSecond = n
			}
		}
	}
	if lastFirst > 0 && lastFirst < 50 && firstSecond > 0 {
		t.Errorf("gap at scrollback/screen boundary: FIRST_ ends at %d (should go to 50), SECOND_ starts at %d", lastFirst, firstSecond)
		t.Error("client snapshot is stale — lines between FIRST_ and SECOND_ are missing")
	}

	nxt.Write([]byte("q"))
	nxt.Write([]byte("wait\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
}

// TestScrollbackAfterReconnect verifies that scrollback history from before
// a disconnect is available after the client reconnects. The server keeps
// the full scrollback; the client must sync the gap on reconnect.
func TestScrollbackAfterReconnect(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate output that will go into scrollback.
	nxt.Write([]byte("for i in $(seq 1 100); do echo \"BEFORE_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Kill the client connection to force a reconnect.
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Enter scrollback and scroll to the top.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active after reconnect", 5*time.Second)

	for range 15 {
		nxt.Write([]byte{0x15}) // ctrl+u = page up
		time.Sleep(30 * time.Millisecond)
	}

	// The pre-disconnect early lines should be visible. Use BEFORE_5
	// which is definitely in scrollback (not on the 24-line screen
	// which shows the tail end of the output). Match exactly to
	// avoid matching BEFORE_50, BEFORE_55, etc.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "BEFORE_5" || strings.HasPrefix(trimmed, "BEFORE_5 ") {
				return true
			}
		}
		return false
	}, "BEFORE_5 visible in scrollback after reconnect", 5*time.Second)

	nxt.Write([]byte("q"))
	nxt.WaitFor("nxterm$", 5*time.Second)
}

// TestScrollbackAfterReconnectLarge verifies scrollback sync works with
// a large scrollback that requires multiple server chunks (>1000 lines).
func TestScrollbackAfterReconnectLarge(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate 2000+ lines so the server needs multiple chunks (1000 each).
	nxt.Write([]byte("seq 1 2000\r"))
	nxt.WaitFor("nxterm$", 30*time.Second)
	nxt.WaitForSilence(500 * time.Millisecond)

	// Kill the client connection to force a reconnect.
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Enter scrollback.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active after reconnect", 5*time.Second)

	// Wait for sync to complete — the status bar total should be > 0.
	nxt.WaitForScreen(func(lines []string) bool {
		// Status shows "scrollback [N/M]" — M should be > 0.
		status := lines[0]
		if !strings.Contains(status, "scrollback") {
			return false
		}
		// Check it's not [0/0] or [...]
		return !strings.Contains(status, "/0]") && !strings.Contains(status, "[...]")
	}, "scrollback sync complete (non-zero total)", 10*time.Second)

	screen := nxt.ScreenLines()
	t.Logf("scrollback after sync: %s", strings.TrimSpace(screen[0]))

	// Scroll to the very top. Use 'g' (home key) for instant jump.
	nxt.Write([]byte("g"))
	time.Sleep(200 * time.Millisecond)

	// Early numbers (single digits) should be visible near the top.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) > 0 && (fields[0] == "5" || fields[0] == "6" || fields[0] == "7") {
				return true
			}
		}
		return false
	}, "early numbers (5/6/7) visible at top after large reconnect sync", 5*time.Second)

	nxt.Write([]byte("q"))
	nxt.WaitFor("nxterm$", 5*time.Second)
}

// TestScrollbackPageUpAltScreen verifies that pgup/pgdown are forwarded to
// the terminal when the child is in alt-screen mode (less, vim, etc.)
// and enter scrollback only when back in normal screen mode.
func TestScrollbackPageUpAltScreen(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate scrollback so pgup would normally activate scrollback.
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Enter less (alt-screen program).
	nxt.Write([]byte("seq 1 100 | less\r"))
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] {
			if strings.TrimSpace(line) == "1" {
				return true
			}
		}
		return false
	}, "less showing line 1", 5*time.Second)

	// Send PageUp — should be forwarded to less, NOT enter scrollback.
	nxt.Write([]byte("\x1b[5~"))
	time.Sleep(300 * time.Millisecond)

	// Scrollback should NOT be active.
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback not activated in alt-screen", 2*time.Second)

	// Quit less.
	nxt.Write([]byte("q"))
	nxt.WaitFor("nxterm$", 5*time.Second)

	// Now pgup SHOULD enter scrollback (no longer in alt-screen).
	nxt.Write([]byte("\x1b[5~"))
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback activated after leaving alt-screen", 5*time.Second)

	nxt.Write([]byte("q"))
}

// TestScrollbackWheelAltScreen verifies that mouse wheel events are
// forwarded to the child when it has requested mouse tracking, rather
// than entering scrollback.
func TestScrollbackWheelAltScreen(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate scrollback.
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Run mousehelper (enables mouse tracking).
	nxt.Write([]byte("mousehelper\r"))
	time.Sleep(500 * time.Millisecond)

	// Scroll wheel up — should be forwarded to mousehelper, not enter scrollback.
	nxt.Write([]byte(fmt.Sprintf("%c[<64;5;5M", ansi.ESC)))
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.Contains(line, "MOUSE wheelup") {
				return true
			}
		}
		return false
	}, "wheel forwarded to mousehelper", 5*time.Second)

	// Scrollback should NOT be active.
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback not activated with mouse tracking", 2*time.Second)

	// Quit mousehelper.
	nxt.Write([]byte("q"))
	nxt.WaitFor("nxterm$", 5*time.Second)
}

// TestScrollbackCommandPalette verifies that the scroll-up command works
// from the command palette regardless of screen mode (the condition is
// on the key binding, not the command).
func TestScrollbackCommandPalette(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate scrollback.
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Open command palette (ctrl+b :).
	nxt.Write([]byte{0x02, ':'})
	nxt.WaitFor("scroll-up", 5*time.Second)

	// Select scroll-up.
	nxt.Write([]byte("scroll-up\r"))

	// Scrollback should be active.
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback activated via command palette", 5*time.Second)

	nxt.Write([]byte("q"))
}

// TestScrollbackDesync verifies that the client's scrollback view stays
// in sync with the server when new output arrives while the user is
// viewing scrollback.
//
// Currently fails: the client fetches a one-time snapshot from the
// server and doesn't update it as new lines arrive. The server's
// scrollback grows but the client doesn't know, causing the
// scrollback/screen boundary to be wrong.
func TestScrollbackDesync(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	regionID := spawnRegion(t, socketPath, "shell")

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate initial output so there's scrollback.
	nxt.Write([]byte("for i in $(seq 1 100); do echo \"LINE_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Record server scrollback before.
	beforeLines := strings.Split(strings.TrimSpace(
		runNxtermctl(t, socketPath, "region", "scrollback", regionID)), "\n")
	beforeCount := len(beforeLines)
	t.Logf("server scrollback before: %d lines", beforeCount)

	// Enter scrollback (this fetches the snapshot).
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	// Exit scrollback — the snapshot is now taken and discarded.
	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited", 3*time.Second)

	// Generate more output.
	nxt.Write([]byte("for i in $(seq 200 300); do echo \"LATE_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Record server scrollback after.
	afterLines := strings.Split(strings.TrimSpace(
		runNxtermctl(t, socketPath, "region", "scrollback", regionID)), "\n")
	afterCount := len(afterLines)
	t.Logf("server scrollback after: %d lines", afterCount)

	growth := afterCount - beforeCount
	if growth <= 0 {
		t.Fatal("server scrollback did not grow")
	}
	t.Logf("server scrollback grew by %d lines", growth)

	// Re-enter scrollback. A correct implementation would have the
	// client's local history already containing the new lines (from
	// replaying terminal_events onto a HistoryScreen). The client
	// would only need to fetch the gap between its history and the
	// server's for lines from before the client connected.
	//
	// With the current implementation, the client fetches a fresh
	// snapshot each time — so re-entering works, but while IN
	// scrollback, the view is frozen. This test verifies the
	// re-entered view is correct.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback re-entered", 5*time.Second)

	// Scroll up a few pages — LATE_ lines should be near the bottom
	// of scrollback (they're the most recent output).
	for range 5 {
		nxt.Write([]byte{0x15}) // ctrl+u
		time.Sleep(30 * time.Millisecond)
	}

	// LATE_ lines should be in the scrollback.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] {
			if strings.Contains(line, "LATE_") {
				return true
			}
		}
		return false
	}, "LATE_ lines in scrollback", 5*time.Second)

	// Scroll all the way to the top to find LINE_1.
	for range 30 {
		nxt.Write([]byte{0x15})
		time.Sleep(30 * time.Millisecond)
	}

	// LINE_1 should be at the top.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "LINE_1" || strings.HasPrefix(trimmed, "LINE_1 ") {
				return true
			}
		}
		return false
	}, "LINE_1 at top of scrollback", 5*time.Second)

	nxt.Write([]byte("q"))
	nxt.WaitFor("nxterm$", 5*time.Second)
}
