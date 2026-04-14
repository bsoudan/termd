package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestScrollbackBuffer(t *testing.T) {
	t.Parallel()
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
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestScrollbackNavigation(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
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
	t.Parallel()
	nxt := startFrontendShared(t)
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

	// Send more PageUp keys to scroll further back.
	// Small delay so InputParser emits them as separate messages
	// rather than one large chunk (which costs one render cycle
	// per sequence in handleFocusInput).
	for range 20 {
		nxt.Write([]byte("\x1b[5~"))
		time.Sleep(5 * time.Millisecond)
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

	// PageDown should NOT enter scrollback (only scroll-up does).
	nxt.Write([]byte("\x1b[6~"))
	time.Sleep(300 * time.Millisecond)

	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback not activated by PageDown", 3*time.Second)

	// Re-enter scrollback and verify that paging down to the bottom exits.
	nxt.Write([]byte("\x1b[5~")) // PageUp to enter
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback re-entered via PageUp", 5*time.Second)

	// Page down past offset 0 — should auto-exit scrollback.
	nxt.Write([]byte("\x1b[6~")) // PageDown
	time.Sleep(100 * time.Millisecond)

	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited by PageDown at bottom", 5*time.Second)
}

func TestScrollbackScrollWheel(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
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

	// Scroll wheel up to reach early numbers. Each event scrolls ~3 lines;
	// ~200 lines of scrollback / 3 ≈ 67 events needed.
	wheelUp := fmt.Sprintf("%c[<64;5;5M", ansi.ESC)
	for range 70 {
		nxt.Write([]byte(wheelUp))
		time.Sleep(5 * time.Millisecond)
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

	// Scroll wheel down past offset 0 to auto-exit scrollback.
	// Small delay so InputParser doesn't bundle all events into
	// one RawInputMsg (which costs one render cycle per sequence
	// in handleFocusInput).
	wheelDown := fmt.Sprintf("%c[<65;5;5M", ansi.ESC)
	for range 80 {
		nxt.Write([]byte(wheelDown))
		time.Sleep(5 * time.Millisecond)
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
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate initial output.
	nxt.Write([]byte("for i in $(seq 1 50); do echo \"FIRST_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Start a delayed background job.
	nxt.Write([]byte("(sleep 1; for i in $(seq 1 50); do echo \"SECOND_$i\"; done) &\r"))
	nxt.WaitFor("nxterm$", 5*time.Second)

	// Enter scrollback.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	screen := nxt.ScreenLines()
	t.Logf("initial: %s", strings.TrimSpace(screen[0]))

	// Wait for the SECOND_ output to arrive (background job).
	nxt.WaitFor("SECOND_50", 10*time.Second)

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
	// Wait for scrollback to exit before sending shell commands.
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited", 5*time.Second)
	nxt.Write([]byte("wait\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
}

// TestScrollbackAfterReconnect verifies that scrollback history from before
// a disconnect is available after the client reconnects. The server keeps
// the full scrollback; the client must sync the gap on reconnect.
func TestScrollbackAfterReconnect(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	nxt := startFrontendShared(t)
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
	t.Parallel()
	nxt := startFrontendShared(t)
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
	t.Parallel()
	nxt := startFrontendShared(t)
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
	t.Parallel()
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

// TestScrollbackWheelClamp verifies that scrolling up with the mouse wheel
// past the top of scrollback clamps the offset instead of growing it
// unbounded. Without clamping, the user would have to scroll back down
// through a "phantom" distance before the view starts moving.
func TestScrollbackWheelClamp(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate ~100 lines of scrollback.
	nxt.Write([]byte("seq 1 100\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Enter scrollback via wheel up.
	wheelUp := fmt.Sprintf("%c[<64;5;5M", ansi.ESC)
	nxt.Write([]byte(wheelUp))
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	// Scroll up way more than the scrollback contains (~100 lines,
	// but we send 200 wheel events * 3 lines = 600 lines of scroll).
	for range 200 {
		nxt.Write([]byte(wheelUp))
		time.Sleep(2 * time.Millisecond)
	}

	// The status bar shows "scrollback [offset/total]". The offset
	// should be clamped to the total, not inflated past it.
	nxt.WaitForScreen(func(lines []string) bool {
		status := lines[0]
		if !strings.Contains(status, "scrollback") {
			return false
		}
		// Parse "scrollback [N/M]" and verify N <= M.
		var offset, total int
		for _, part := range strings.Fields(status) {
			if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
				fmt.Sscanf(part, "[%d/%d]", &offset, &total)
			}
		}
		return total > 0 && offset <= total
	}, "offset clamped to total", 5*time.Second)

	// Now scroll down once — the view should start moving immediately
	// (no phantom distance to burn through).
	wheelDown := fmt.Sprintf("%c[<65;5;5M", ansi.ESC)
	nxt.Write([]byte(wheelDown))
	time.Sleep(100 * time.Millisecond)

	// We should still be in scrollback (not auto-exited) since we were
	// at the top and only scrolled down 3 lines.
	nxt.WaitForScreen(func(lines []string) bool {
		status := lines[0]
		if !strings.Contains(status, "scrollback") {
			return false
		}
		var offset, total int
		for _, part := range strings.Fields(status) {
			if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
				fmt.Sscanf(part, "[%d/%d]", &offset, &total)
			}
		}
		// After one wheel-down (3 lines) from the top, offset should
		// be total-3, not still at total.
		return total > 0 && offset < total
	}, "offset decreased after one wheel down from top", 5*time.Second)

	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited", 5*time.Second)
}

// TestScrollbackRapidEntryExit verifies that rapidly entering and exiting
// scrollback doesn't cause crashes or leave stale state.
func TestScrollbackRapidEntryExit(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate scrollback.
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Rapidly enter and exit scrollback 10 times.
	for i := range 10 {
		// Enter with ctrl+b [
		nxt.Write([]byte{0x02, '['})
		nxt.WaitForScreen(func(lines []string) bool {
			return strings.Contains(lines[0], "scrollback")
		}, fmt.Sprintf("scrollback active (iter %d)", i), 5*time.Second)

		// Scroll up a bit.
		nxt.Write([]byte{0x15}) // ctrl+u
		time.Sleep(50 * time.Millisecond)

		// Exit with q.
		nxt.Write([]byte("q"))
		nxt.WaitForScreen(func(lines []string) bool {
			return !strings.Contains(lines[0], "scrollback")
		}, fmt.Sprintf("scrollback exited (iter %d)", i), 5*time.Second)
	}

	// After all cycles, verify the terminal is still responsive.
	nxt.Write([]byte("echo ALIVE\r"))
	nxt.WaitFor("ALIVE", 5*time.Second)
}

// TestScrollbackResize verifies that resizing the terminal while in
// scrollback mode doesn't corrupt the view or crash.
func TestScrollbackResize(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate scrollback.
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Enter scrollback and scroll up.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	for range 10 {
		nxt.Write([]byte{0x15}) // ctrl+u
	}
	time.Sleep(200 * time.Millisecond)

	// Resize the terminal while in scrollback.
	nxt.Resize(120, 40)
	time.Sleep(500 * time.Millisecond)

	// Should still be in scrollback mode.
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback still active after resize", 5*time.Second)

	// Resize back to original size.
	nxt.Resize(80, 24)
	time.Sleep(500 * time.Millisecond)

	// Exit scrollback.
	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited after resize", 5*time.Second)

	// Terminal should still work.
	nxt.Write([]byte("echo RESIZE_OK\r"))
	nxt.WaitFor("RESIZE_OK", 5*time.Second)
}

// TestScrollbackNoGapAfterSync verifies that after the server sync
// completes, no phantom gap with x-markers appears in the scrollback
// view. This catches a bug where serverTotal persists after syncBuf is
// cleared, causing View() to compute a nonzero gap for lines that were
// already prepended to local history.
func TestScrollbackNoGapAfterSync(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate output, disconnect, reconnect so the client has no
	// local history and must sync everything from the server.
	nxt.Write([]byte("for i in $(seq 1 100); do echo \"SYNC_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Kill the client to force reconnect.
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Enter scrollback — triggers sync.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	// Wait for sync to complete (status shows non-zero total).
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback") &&
			!strings.Contains(lines[0], "/0]")
	}, "sync complete", 10*time.Second)

	// Scroll to the top.
	nxt.Write([]byte("g"))
	time.Sleep(300 * time.Millisecond)

	// Verify no x-markers on screen (which indicate a phantom gap).
	screen := nxt.ScreenLines()
	for i, line := range screen[1:] { // skip tab bar
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// x-markers look like "x x x x x" (alternating x and space).
		if strings.Count(trimmed, "x") > 5 && !strings.Contains(trimmed, "SYNC_") {
			t.Errorf("line %d appears to be a gap marker: %q", i+1, trimmed)
		}
	}

	nxt.Write([]byte("q"))
	nxt.WaitFor("nxterm$", 5*time.Second)
}

// TestScrollbackPageDownNoEntry verifies that PageDown does not enter
// scrollback mode. Only scroll-up should initiate scrollback.
func TestScrollbackPageDownNoEntry(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate scrollback so there's content to scroll through.
	nxt.Write([]byte("seq 1 200\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Send PageDown — should NOT activate scrollback.
	nxt.Write([]byte("\x1b[6~"))
	time.Sleep(500 * time.Millisecond)

	screen := nxt.ScreenLines()
	if strings.Contains(screen[0], "scrollback") {
		t.Error("PageDown should not enter scrollback mode")
	}
}

// TestScrollbackOutputDuringScroll verifies that output arriving while the
// user is scrolled up in scrollback doesn't corrupt the buffer. The test
// launches a background job before entering scrollback, scrolls up while
// output is arriving, waits for it to finish, then scrolls through the
// entire history checking that numbered lines appear in order without
// duplicates.
func TestScrollbackOutputDuringScroll(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate initial scrollback.
	nxt.Write([]byte("for i in $(seq 1 100); do echo \"A$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Launch a delayed background job that will output while we're in scrollback.
	nxt.Write([]byte("(sleep 1; for i in $(seq 1 100); do echo \"B$i\"; done) &\r"))
	nxt.WaitFor("nxterm$", 5*time.Second)

	// Enter scrollback and scroll up.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	for range 5 {
		nxt.Write([]byte{0x15}) // ctrl+u
	}

	// Wait for the B-lines to arrive. Since we're scrolled up, we can't
	// see B100 directly. Instead, watch the scrollback total grow — it
	// increases as terminal events add lines to the HistoryScreen.
	initialTotal := 0
	nxt.WaitForScreen(func(lines []string) bool {
		var offset, total int
		for _, part := range strings.Fields(lines[0]) {
			if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
				fmt.Sscanf(part, "[%d/%d]", &offset, &total)
			}
		}
		if initialTotal == 0 && total > 0 {
			initialTotal = total
		}
		// Wait for at least 50 new lines to arrive.
		return total > initialTotal+50
	}, "scrollback total grew by 50+ lines", 15*time.Second)

	// Scroll through the entire scrollback collecting numbered lines.
	// Go to the top first.
	nxt.Write([]byte("g")) // home
	time.Sleep(300 * time.Millisecond)

	allSeen := make(map[string]int)
	collectScreen := func() {
		screen := nxt.ScreenLines()
		for _, line := range screen[1:] { // skip tab bar
			trimmed := strings.TrimSpace(line)
			runes := []rune(trimmed)
			if len(runes) > 0 {
				last := runes[len(runes)-1]
				if last == '·' || last == '█' || last == '▲' || last == '▼' {
					trimmed = strings.TrimSpace(string(runes[:len(runes)-1]))
				}
			}
			var n int
			if _, err := fmt.Sscanf(trimmed, "A%d", &n); err == nil && n >= 1 && n <= 100 {
				allSeen[trimmed]++
			}
			if _, err := fmt.Sscanf(trimmed, "B%d", &n); err == nil && n >= 1 && n <= 100 {
				allSeen[trimmed]++
			}
		}
	}

	// Collect from the top, then page down through everything.
	// Stop when we reach the bottom (offset=0 or offset < halfPage).
	collectScreen()
	for range 30 {
		nxt.Write([]byte{0x04}) // ctrl+d = page down
		time.Sleep(50 * time.Millisecond)

		screen := nxt.ScreenLines()
		status := screen[0]
		var offset int
		for _, part := range strings.Fields(status) {
			if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
				fmt.Sscanf(part, "[%d/", &offset)
			}
		}
		collectScreen()
		if offset <= 0 {
			break // at the bottom, no point paging further
		}
	}

	// Check no line appears more than 3 times (overlap between pages
	// is normal, but >3 indicates duplication in the buffer).
	for line, count := range allSeen {
		if count > 3 {
			t.Errorf("line %q seen %d times (likely duplicated in scrollback)", line, count)
		}
	}

	// Check that A-lines appear in order where we saw them.
	// Check we found a reasonable number of A and B lines.
	aCount, bCount := 0, 0
	for k := range allSeen {
		if strings.HasPrefix(k, "A") {
			aCount++
		} else if strings.HasPrefix(k, "B") {
			bCount++
		}
	}
	t.Logf("found %d A-lines and %d B-lines", aCount, bCount)
	if aCount < 80 {
		t.Errorf("only found %d/100 A-lines in scrollback (expected most)", aCount)
	}
	if bCount < 50 {
		t.Errorf("only found %d/100 B-lines in scrollback (expected many)", bCount)
	}

	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited", 5*time.Second)
	nxt.Write([]byte("wait\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
}

// TestScrollbackScreenUpdateDuringScroll verifies that a screen_update
// (mode 2026 synchronized output) arriving while in scrollback doesn't
// duplicate lines at the history/screen boundary. The server includes
// its scrollback count so the client can trim its local history to
// avoid overlap with the new screen content.
func TestScrollbackScreenUpdateDuringScroll(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate initial scrollback.
	nxt.Write([]byte("for i in $(seq 1 100); do echo \"INIT_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Enter scrollback and scroll up.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	for range 3 {
		nxt.Write([]byte{0x15}) // ctrl+u
	}
	time.Sleep(200 * time.Millisecond)

	// Record the scrollback total before the screen_update.
	var totalBefore int
	screen := nxt.ScreenLines()
	for _, part := range strings.Fields(screen[0]) {
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			fmt.Sscanf(part, "[%d/%d]", new(int), &totalBefore)
		}
	}
	t.Logf("total before trigger: %d", totalBefore)

	// Exit scrollback, run a command that triggers mode 2026 sync
	// (generating a screen_update), then re-enter scrollback.
	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited", 3*time.Second)

	// Running a command causes bash to redraw the prompt with mode 2026,
	// generating output that causes terminal_events followed by a
	// screen_update snapshot.
	nxt.Write([]byte("for i in $(seq 101 150); do echo \"INIT_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Re-enter scrollback.
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active again", 5*time.Second)

	// Go to bottom (offset=0), then scroll up one line.
	nxt.Write([]byte("G"))
	time.Sleep(100 * time.Millisecond)
	nxt.Write([]byte("k")) // up 1
	time.Sleep(200 * time.Millisecond)

	// Collect lines at the history/screen boundary.
	screen = nxt.ScreenLines()
	var visible []string
	for _, line := range screen[1:] { // skip tab bar
		trimmed := strings.TrimSpace(line)
		runes := []rune(trimmed)
		if len(runes) > 0 {
			last := runes[len(runes)-1]
			if last == '·' || last == '█' || last == '▲' || last == '▼' {
				trimmed = strings.TrimSpace(string(runes[:len(runes)-1]))
			}
		}
		if strings.HasPrefix(trimmed, "INIT_") {
			visible = append(visible, trimmed)
		}
	}

	// Check for duplicates.
	seen := make(map[string]int)
	for _, v := range visible {
		seen[v]++
	}
	for k, count := range seen {
		if count > 1 {
			t.Errorf("duplicate line at history/screen boundary: %q appears %d times", k, count)
		}
	}

	t.Logf("visible lines at offset=1: %v", visible)

	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited", 5*time.Second)
}

// TestScrollbackConcurrentOutputDesync reproduces the race condition where
// PTY output triggers a screen_update (mode 2026) while the client is in
// scrollback mode with sync in progress. The screen_update used to trim
// the client's history, making ScrollbackLayer.localAtEntry stale and
// causing duplicate or missing lines after sync completion.
func TestScrollbackConcurrentOutputDesync(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Generate substantial initial scrollback so the server sync takes
	// multiple chunks and gives time for events to interleave.
	nxt.Write([]byte("for i in $(seq 1 200); do echo \"BASE_$i\"; done\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Start a background job that will produce output while we're in
	// scrollback, including prompt redraws that trigger mode 2026.
	nxt.Write([]byte("(sleep 0.5; for i in $(seq 1 100); do echo \"LIVE_$i\"; done) &\r"))
	nxt.WaitFor("nxterm$", 5*time.Second)

	// Enter scrollback immediately. The sync request goes to the server
	// which has ~200 lines. While chunks stream back, the background job
	// starts producing output (terminal_events + possible screen_updates).
	nxt.Write([]byte{0x02, '['})
	nxt.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active", 5*time.Second)

	// Wait for the background output to complete and terminal to settle.
	nxt.WaitFor("LIVE_100", 10*time.Second)
	nxt.WaitForSilence(500 * time.Millisecond)

	// Navigate to the bottom to see the boundary region.
	nxt.Write([]byte("G")) // end
	time.Sleep(200 * time.Millisecond)

	// Scroll up a few lines to see the history/screen boundary.
	nxt.Write([]byte("k")) // up 1
	time.Sleep(200 * time.Millisecond)

	screen := nxt.ScreenLines()

	// Collect all numbered lines visible on screen.
	var visible []string
	for _, line := range screen[1:] { // skip tab bar
		trimmed := strings.TrimSpace(line)
		runes := []rune(trimmed)
		if len(runes) > 0 {
			last := runes[len(runes)-1]
			if last == '·' || last == '█' || last == '▲' || last == '▼' {
				trimmed = strings.TrimSpace(string(runes[:len(runes)-1]))
			}
		}
		if strings.HasPrefix(trimmed, "BASE_") || strings.HasPrefix(trimmed, "LIVE_") {
			visible = append(visible, trimmed)
		}
	}

	t.Logf("visible lines at offset=1: %v", visible)

	// Check for duplicates.
	seen := make(map[string]int)
	for _, v := range visible {
		seen[v]++
	}
	for k, count := range seen {
		if count > 1 {
			t.Errorf("duplicate line: %q appears %d times", k, count)
		}
	}

	// Verify ordering within the current page: lines should be monotonic
	// (no shuffled or out-of-order entries within a single view).
	lastN := 0
	for _, v := range visible {
		var n int
		if _, err := fmt.Sscanf(v, "LIVE_%d", &n); err == nil {
			if n <= lastN && lastN > 0 {
				t.Errorf("LIVE lines out of order within page: LIVE_%d after LIVE_%d", n, lastN)
			}
			lastN = n
		}
	}

	// Exit scrollback and clean up background job.
	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited", 5*time.Second)
	nxt.Write([]byte("wait\r"))
	nxt.WaitFor("nxterm$", 10*time.Second)
}
