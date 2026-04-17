package e2e

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"nxtermd/internal/nxtest"
)

func TestScrollbackBuffer(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbbuf", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbbuf")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Output 200 lines — in a 24-row terminal, early lines scroll into
	// the server's scrollback buffer.
	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	// Now that the sync proves server and client have processed all
	// output, the scrollback response must contain the expected lines
	// without polling.
	out := runNxtermctl(t, socketPath, "region", "scrollback", region.ID())
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{"1", "2", "3", "10", "50"}
	found := make(map[string]bool)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, w := range want {
			if trimmed == w {
				found[w] = true
			}
		}
	}
	for _, w := range want {
		if !found[w] {
			t.Errorf("scrollback missing line %q (got %d lines total)", w, len(lines))
		}
	}
}

// TestScrollbackNavigation drives scrollback entry, paging, and exit
// against a native region — no shell, no prompt heuristic, all
// synchronization via sync markers. Every assertion runs after a
// Sync() so the TUI is known caught-up; no polling or sleeps.
func TestScrollbackNavigation(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbnav", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbnav")
	defer nxterm.Kill()

	// Boot sync: queued before subscribe, delivered to the TUI with
	// the initial snapshot. Proves the TUI has connected, subscribed,
	// received the snapshot, and processed it — subsequent output
	// events will flow as live terminal events and populate the
	// TUI's scrollback buffer.
	region.Sync(nxterm, "TUI boot + subscribe")

	// Feed 200 lines — server parses, broadcasts, TUI renders.
	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	// Enter scrollback via ctrl+b [
	nxterm.Write([]byte{0x02, '['}).Sync("enter scrollback")
	nxterm.RequireTabBarContains("scrollback")

	// Jump to top of scrollback (g). Early numbers should be visible.
	nxterm.Write([]byte("g")).Sync("jump to top")
	requireEarlyNumbersVisible(t, nxterm)

	// Exit scrollback with q.
	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")
}

func requireEarlyNumbersVisible(t *testing.T, nxterm *nxtest.T) {
	t.Helper()
	screen := nxterm.ScreenLines()
	for _, line := range screen[1:] {
		if fields := strings.Fields(line); len(fields) > 0 {
			if fields[0] == "1" || fields[0] == "2" || fields[0] == "3" {
				return
			}
		}
	}
	t.Fatalf("expected early numbers (1/2/3) visible, got:\n%s", strings.Join(screen, "\n"))
}

// TestScrollbackPageUpDown verifies that PageUp and PageDown keys activate
// scrollback mode directly from the terminal (without the prefix key).
func TestScrollbackPageUpDown(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbpud", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbpud")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	// PageUp (\x1b[5~) activates scrollback.
	nxterm.Write([]byte("\x1b[5~")).Sync("pageup to enter scrollback")
	nxterm.RequireTabBarContains("scrollback")

	// More PageUps to reach early numbers. Batch with syncs so
	// InputParser doesn't bundle them into one RawInputMsg.
	for range 4 {
		for range 5 {
			nxterm.Write([]byte("\x1b[5~"))
		}
		nxterm.Sync("batch of 5 pageups")
	}
	requireEarlyNumbersVisible(t, nxterm)

	// Exit with q.
	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")

	// PageDown should NOT enter scrollback (only scroll-up does).
	nxterm.Write([]byte("\x1b[6~")).Sync("pagedown from live (no-op)")
	nxterm.RequireTabBarDoesNotContain("scrollback")

	// PageUp to re-enter; PageDown past offset 0 should auto-exit.
	nxterm.Write([]byte("\x1b[5~")).Sync("pageup re-enter")
	nxterm.RequireTabBarContains("scrollback")
	nxterm.Write([]byte("\x1b[6~")).Sync("pagedown to auto-exit")
	nxterm.RequireTabBarDoesNotContain("scrollback")
}

func TestScrollbackScrollWheel(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbwhl", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbwhl")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	// Scroll wheel up activates scrollback with data loaded.
	nxterm.MouseWheelUp(5, 5).Sync("wheel up to enter scrollback")
	nxterm.RequireTabBarContains("scrollback")

	// Scroll wheel up to reach early numbers. Each event scrolls ~3 lines;
	// ~200 lines of scrollback / 3 ≈ 67 events needed. Batch with syncs
	// between so InputParser doesn't bundle all events into one
	// RawInputMsg (focus-input renders once per sequence).
	for range 7 {
		for range 10 {
			nxterm.MouseWheelUp(5, 5)
		}
		nxterm.Sync("batch of 10 wheelups")
	}
	requireEarlyNumbersVisible(t, nxterm)

	// Scroll wheel down past offset 0 to auto-exit scrollback.
	for range 8 {
		for range 10 {
			nxterm.MouseWheelDown(5, 5)
		}
		nxterm.Sync("batch of 10 wheeldowns")
	}
	nxterm.RequireTabBarDoesNotContain("scrollback")
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
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sblu", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sblu")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Initial output: FIRST_1..FIRST_50.
	var buf bytes.Buffer
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&buf, "FIRST_%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed FIRST_1..50")

	// Enter scrollback.
	nxterm.Write([]byte{0x02, '['}).Sync("enter scrollback")
	nxterm.RequireTabBarContains("scrollback")

	// Feed SECOND_1..SECOND_50 while in scrollback — live updates should
	// land in the client's HistoryScreen.
	buf.Reset()
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&buf, "SECOND_%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed SECOND_1..50 during scrollback")

	// Go to bottom (offset=0), then scroll up 1 line to view the
	// scrollback/live boundary.
	nxterm.Write([]byte("G")).Sync("go to bottom")
	nxterm.Write([]byte("k")).Sync("up 1 line")

	// Collect all visible numbered lines.
	screen := nxterm.ScreenLines()
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
		if strings.HasPrefix(trimmed, "FIRST_") || strings.HasPrefix(trimmed, "SECOND_") {
			visible = append(visible, trimmed)
		}
	}
	t.Logf("visible lines at offset=1: %v", visible)

	// No duplicates at the boundary.
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

	// No gap across the FIRST_→SECOND_ boundary.
	lastFirst, firstSecond := 0, 0
	for _, v := range visible {
		var n int
		if _, err := fmt.Sscanf(v, "FIRST_%d", &n); err == nil && n > lastFirst {
			lastFirst = n
		}
		if firstSecond == 0 {
			if _, err := fmt.Sscanf(v, "SECOND_%d", &n); err == nil {
				firstSecond = n
			}
		}
	}
	if lastFirst > 0 && lastFirst < 50 && firstSecond > 0 {
		t.Errorf("gap at scrollback/screen boundary: FIRST_ ends at %d (should go to 50), SECOND_ starts at %d", lastFirst, firstSecond)
	}

	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")
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
	nxt.Sync("render settle")

	// Kill the client connection to force a reconnect.
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Sync("render settle")

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
	nxt.Sync("render settle 500ms")

	// Kill the client connection to force a reconnect.
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Sync("render settle")

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
	nxt.Write([]byte("g")).Sync("jump to top")

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
	startMouseHelper(t, nxt)

	// Scroll wheel up — should be forwarded to mousehelper, not enter scrollback.
	nxt.MouseWheelUp(5, 5)
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
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbcmd", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbcmd")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	// Open command palette (ctrl+b :), type scroll-up, execute.
	nxterm.Write([]byte{0x02, ':'}).Sync("open command palette")
	nxterm.WaitFor("scroll-up", 5*time.Second)
	nxterm.Write([]byte("scroll-up\r")).Sync("execute scroll-up")
	// Command palette submission triggers async scrollback layer push;
	// wait for the tab bar to reflect scrollback mode.
	nxterm.WaitForScreen(func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback active after palette command", 5*time.Second)
	nxterm.Write([]byte("q")).Sync("exit scrollback")
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
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbdsy", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbdsy")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Initial batch: LINE_1..LINE_100.
	var buf bytes.Buffer
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&buf, "LINE_%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed LINE_1..100")

	beforeCount := len(strings.Split(strings.TrimSpace(
		runNxtermctl(t, socketPath, "region", "scrollback", region.ID())), "\n"))
	t.Logf("server scrollback before: %d lines", beforeCount)

	// Enter then exit scrollback (snapshot taken and discarded).
	nxterm.Write([]byte{0x02, '['}).Sync("enter scrollback")
	nxterm.RequireTabBarContains("scrollback")
	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")

	// Second batch: LATE_200..LATE_300 (via driver, while live view).
	buf.Reset()
	for i := 200; i <= 300; i++ {
		fmt.Fprintf(&buf, "LATE_%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed LATE_200..300")

	afterCount := len(strings.Split(strings.TrimSpace(
		runNxtermctl(t, socketPath, "region", "scrollback", region.ID())), "\n"))
	t.Logf("server scrollback after: %d lines", afterCount)
	if afterCount <= beforeCount {
		t.Fatalf("server scrollback did not grow (before=%d after=%d)", beforeCount, afterCount)
	}

	// Re-enter scrollback; page up some; LATE_ should be visible.
	nxterm.Write([]byte{0x02, '['}).Sync("re-enter scrollback")
	nxterm.RequireTabBarContains("scrollback")
	for range 5 {
		nxterm.Write([]byte{0x15}) // ctrl+u
	}
	nxterm.Sync("5x page up")
	requireAnyLineContains(t, nxterm, "LATE_")

	// Scroll to top; LINE_1 should be visible on screen.
	for range 30 {
		nxterm.Write([]byte{0x15})
	}
	nxterm.Sync("30x page up to top")
	requireAnyFieldZeroEquals(t, nxterm, "LINE_1")

	nxterm.Write([]byte("q")).Sync("exit scrollback")
}

func requireAnyLineContains(t *testing.T, nxterm *nxtest.T, needle string) {
	t.Helper()
	screen := nxterm.ScreenLines()
	for _, line := range screen[1:] {
		if strings.Contains(line, needle) {
			return
		}
	}
	t.Fatalf("expected %q on screen, got:\n%s", needle, strings.Join(screen, "\n"))
}

// requireAnyFieldZeroEquals matches any row whose first whitespace-
// delimited field equals want. In scrollback mode the top row can
// interleave content and the status bar, so the tab-bar row (index 0)
// is included in the scan.
func requireAnyFieldZeroEquals(t *testing.T, nxterm *nxtest.T, want string) {
	t.Helper()
	screen := nxterm.ScreenLines()
	for _, line := range screen {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == want {
			return
		}
	}
	t.Fatalf("expected a row with first field %q, got:\n%s", want, strings.Join(screen, "\n"))
}

// TestScrollbackWheelClamp verifies that scrolling up with the mouse wheel
// past the top of scrollback clamps the offset instead of growing it
// unbounded. Without clamping, the user would have to scroll back down
// through a "phantom" distance before the view starts moving.
func TestScrollbackWheelClamp(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbclmp", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbclmp")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// ~100 lines of scrollback.
	var buf bytes.Buffer
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 100 lines")

	// Enter scrollback via wheel up, then scroll way past the top.
	// The original test used a 2ms sleep between events because
	// InputParser bundles closely-spaced events into one RawInputMsg,
	// and focus-input handling renders once per sequence — a single
	// bundled msg advances only one wheel step instead of 200. Send
	// in small batches with a sync between them so each batch is
	// its own RawInputMsg.
	nxterm.MouseWheelUp(5, 5).Sync("wheel up to enter")
	for range 20 {
		for range 10 {
			nxterm.MouseWheelUp(5, 5)
		}
		nxterm.Sync("batch of 10 wheelups")
	}
	requireOffsetClampedToTotal(t, nxterm)

	// Single wheel down from the top should move the view immediately
	// (no phantom distance to burn through).
	nxterm.MouseWheelDown(5, 5).Sync("wheel down from clamped top")
	requireOffsetBelowTotal(t, nxterm)

	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")
}

func parseScrollbackStatus(tabBar string) (offset, total int, ok bool) {
	if !strings.Contains(tabBar, "scrollback") {
		return 0, 0, false
	}
	for _, part := range strings.Fields(tabBar) {
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			fmt.Sscanf(part, "[%d/%d]", &offset, &total)
			return offset, total, true
		}
	}
	return 0, 0, false
}

func requireOffsetClampedToTotal(t *testing.T, nxterm *nxtest.T) {
	t.Helper()
	screen := nxterm.ScreenLines()
	offset, total, ok := parseScrollbackStatus(screen[0])
	if !ok || total == 0 || offset > total {
		t.Fatalf("expected offset clamped to total, got tab bar %q (offset=%d total=%d)",
			screen[0], offset, total)
	}
}

func requireOffsetBelowTotal(t *testing.T, nxterm *nxtest.T) {
	t.Helper()
	screen := nxterm.ScreenLines()
	offset, total, ok := parseScrollbackStatus(screen[0])
	if !ok || total == 0 || offset >= total {
		t.Fatalf("expected offset < total, got tab bar %q (offset=%d total=%d)",
			screen[0], offset, total)
	}
}

// TestScrollbackRapidEntryExit verifies that rapidly entering and exiting
// scrollback doesn't cause crashes or leave stale state.
func TestScrollbackRapidEntryExit(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbrpd", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbrpd")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	// Rapidly enter and exit scrollback 10 times.
	for i := range 10 {
		nxterm.Write([]byte{0x02, '['}).Sync(fmt.Sprintf("enter %d", i))
		nxterm.RequireTabBarContains("scrollback")
		nxterm.Write([]byte{0x15}).Sync(fmt.Sprintf("page up %d", i)) // ctrl+u
		nxterm.Write([]byte("q")).Sync(fmt.Sprintf("exit %d", i))
		nxterm.RequireTabBarDoesNotContain("scrollback")
	}

	// Verify the terminal is still responsive by feeding more output.
	region.Output([]byte("ALIVE\r\n")).Sync(nxterm, "post-cycle output")
	// Region is at the top of live screen; last non-blank row contains ALIVE.
	screen := nxterm.ScreenLines()
	foundAlive := false
	for _, line := range screen[1:] {
		if strings.Contains(line, "ALIVE") {
			foundAlive = true
			break
		}
	}
	if !foundAlive {
		t.Fatalf("expected ALIVE on screen after 10 cycles, got:\n%s", strings.Join(screen, "\n"))
	}
}

// TestScrollbackResize verifies that resizing the terminal while in
// scrollback mode doesn't corrupt the view or crash.
func TestScrollbackResize(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbrsz", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbrsz")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	// Enter scrollback and scroll up.
	nxterm.Write([]byte{0x02, '['}).Sync("enter scrollback")
	nxterm.RequireTabBarContains("scrollback")
	for range 10 {
		nxterm.Write([]byte{0x15}) // ctrl+u
	}
	nxterm.Sync("10x page up")

	// Resize the terminal while in scrollback.
	nxterm.Resize(120, 40)
	nxterm.Sync("resize to 120x40")
	nxterm.RequireTabBarContains("scrollback")

	// Resize back to original size.
	nxterm.Resize(80, 24)
	nxterm.Sync("resize to 80x24")

	// Exit scrollback.
	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")

	// Terminal should still work — feed more output and verify visible.
	region.Output([]byte("RESIZE_OK\r\n")).Sync(nxterm, "post-resize output")
	screen := nxterm.ScreenLines()
	foundOK := false
	for _, line := range screen[1:] {
		if strings.Contains(line, "RESIZE_OK") {
			foundOK = true
			break
		}
	}
	if !foundOK {
		t.Fatalf("expected RESIZE_OK on screen, got:\n%s", strings.Join(screen, "\n"))
	}
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
	nxt.Sync("render settle")

	// Kill the client to force reconnect.
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Sync("render settle")

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
	nxt.Write([]byte("g")).Sync("jump to top")

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
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbpdne", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbpdne")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	var buf bytes.Buffer
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buf, "%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed 200 lines")

	nxterm.Write([]byte("\x1b[6~")).Sync("pagedown from live (no-op)")
	nxterm.RequireTabBarDoesNotContain("scrollback")
}

// TestScrollbackOutputDuringScroll verifies that output arriving while
// the user is scrolled up in scrollback doesn't corrupt the buffer. A
// first batch of lines fills the screen; the user enters scrollback
// and scrolls up; a second batch arrives; then scrolling through the
// full history verifies lines appear without duplicates or gaps.
func TestScrollbackOutputDuringScroll(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbods", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbods")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// First batch: A1..A100.
	var buf bytes.Buffer
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&buf, "A%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed A1..A100")

	// Enter scrollback and page up 5 times.
	nxterm.Write([]byte{0x02, '['}).Sync("enter scrollback")
	nxterm.RequireTabBarContains("scrollback")
	for range 5 {
		nxterm.Write([]byte{0x15}) // ctrl+u
	}
	nxterm.Sync("5x page up")

	// Second batch arrives while scrolled up.
	buf.Reset()
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&buf, "B%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed B1..B100 during scroll")

	// Jump to top, then page down through everything collecting lines.
	nxterm.Write([]byte("g")).Sync("jump to top")

	allSeen := make(map[string]int)
	collect := func() {
		screen := nxterm.ScreenLines()
		for _, line := range screen[1:] {
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
	readOffset := func() int {
		screen := nxterm.ScreenLines()
		if offset, _, ok := parseScrollbackStatus(screen[0]); ok {
			return offset
		}
		return -1
	}

	collect()
	for range 30 {
		prev := readOffset()
		nxterm.Write([]byte{0x04}).Sync("page down") // ctrl+d
		collect()
		if readOffset() == prev || readOffset() <= 0 {
			break
		}
	}

	// Few lines should appear excessively. Some overlap is normal.
	var excessive []string
	for line, count := range allSeen {
		if count > 5 {
			excessive = append(excessive, fmt.Sprintf("%s×%d", line, count))
		}
	}
	if len(excessive) > 3 {
		t.Errorf("%d lines seen >5 times (likely duplicated): %v", len(excessive), excessive)
	}

	// Check reasonable coverage of A- and B-lines.
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
		t.Errorf("only found %d/100 A-lines (expected most)", aCount)
	}
	if bCount < 50 {
		t.Errorf("only found %d/100 B-lines (expected many)", bCount)
	}

	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")
}

// TestScrollbackScreenUpdateDuringScroll verifies that a screen_update
// (mode 2026 synchronized output) arriving while in scrollback doesn't
// duplicate lines at the history/screen boundary. The server includes
// its scrollback count so the client can trim its local history to
// avoid overlap with the new screen content.
func TestScrollbackScreenUpdateDuringScroll(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-sbsud", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-sbsud")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Initial scrollback: INIT_1..100.
	var buf bytes.Buffer
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&buf, "INIT_%d\r\n", i)
	}
	region.Output(buf.Bytes()).Sync(nxterm, "feed INIT_1..100")

	// Trigger a mode-2026 synchronized-output burst, which causes the
	// server to emit a screen_update (snapshot) on sync-mode exit
	// rather than incremental events.
	buf.Reset()
	buf.WriteString("\x1b[?2026h") // begin synchronized output
	for i := 101; i <= 150; i++ {
		fmt.Fprintf(&buf, "INIT_%d\r\n", i)
	}
	buf.WriteString("\x1b[?2026l") // end — triggers screen_update snapshot
	region.Output(buf.Bytes()).Sync(nxterm, "feed INIT_101..150 inside mode 2026")

	// Enter scrollback, go to bottom, scroll up one line to view the
	// history/screen boundary.
	nxterm.Write([]byte{0x02, '['}).Sync("enter scrollback")
	nxterm.RequireTabBarContains("scrollback")
	nxterm.Write([]byte("G")).Sync("jump to bottom")
	nxterm.Write([]byte("k")).Sync("up 1 line")

	// Collect visible INIT_ lines and check for duplicates at the boundary.
	screen := nxterm.ScreenLines()
	var visible []string
	for _, line := range screen[1:] {
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

	nxterm.Write([]byte("q")).Sync("exit scrollback")
	nxterm.RequireTabBarDoesNotContain("scrollback")
}
