package e2e

import (
	"strings"
	"testing"
	"time"

	"nxtermd/internal/nxtest"
)

// TestPauseSession exercises the pause-session / resume-session TUI
// commands end-to-end: while paused, the client stops draining
// inbound messages and terminal_events emitted by the region do not
// reach the virtual screen. On resume, the backlog flushes.
//
// This is the user-invocable half of the slow-client test hook; it
// leaves the server-side drop detection (DroppedBroadcasts stat,
// ScrollbackDesync snapshot) to separate changes.
func TestPauseSession(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-pause", "r1", 80, 22)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-pause")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Baseline: events flow, no pause indicator anywhere.
	region.Output([]byte("BEFORE_PAUSE\r\n")).Sync(nxterm, "feed before")
	requireAnyLineContains(t, nxterm, "BEFORE_PAUSE")
	nxterm.RequireTabBarDoesNotContain("⏸")

	// Run pause-session via the command palette.
	nxterm.Write([]byte{0x02, ':'}).Sync("open palette")
	nxterm.Write([]byte("pause-session\r")).Sync("run pause-session")

	// Indicator appears. ⏸ shows in both the tab segment and the
	// session endpoint; a tab-bar substring match covers both.
	nxterm.WaitForScreen(func(lines []string) bool {
		return len(lines) > 0 && strings.Contains(lines[0], "⏸")
	}, "pause indicator appears", 2*time.Second)

	// Events fed during pause must not land on the virtual screen.
	// Use a bare Output (no Sync) because the TUI is not draining and
	// wouldn't ack a region.Sync while paused.
	region.Output([]byte("AFTER_PAUSE\r\n"))
	nxterm.AssertScreenStays(func(lines []string) bool {
		for _, l := range lines {
			if strings.Contains(l, "AFTER_PAUSE") {
				return false
			}
		}
		return true
	}, "AFTER_PAUSE must not appear while paused", 500*time.Millisecond)

	// Resume.
	nxterm.Write([]byte{0x02, ':'}).Sync("open palette")
	nxterm.Write([]byte("resume-session\r")).Sync("run resume-session")

	nxterm.WaitForScreen(func(lines []string) bool {
		return len(lines) > 0 && !strings.Contains(lines[0], "⏸")
	}, "pause indicator cleared", 2*time.Second)

	// Backlog drains; AFTER_PAUSE becomes visible. A fresh region.Sync
	// now works because the TUI is draining again.
	region.Sync(nxterm, "post-resume drain")
	requireAnyLineContains(t, nxterm, "AFTER_PAUSE")
}
