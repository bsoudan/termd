package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestSpawnSecondRegion(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	// Wait for initial tab and prompt. The active tab renders as
	// just " 1 " (commit 98da964 dropped the program name from the
	// active tab) so we wait for the bash prompt instead of "1:bash".
	nxt.WaitFor("nxterm$", 10*time.Second)

	// ctrl+b c to spawn a second region
	nxt.Write([]byte("\x02c"))

	// After spawn, tab 2 becomes active and tab 1 becomes inactive.
	// The inactive tab DOES render its program name, so "1:bash"
	// appearing in the tab bar is the cleanest signal that the
	// spawn took effect.
	nxt.WaitFor("1:bash", 10*time.Second)

	// New tab should have a prompt
	nxt.WaitFor("nxterm$", 10*time.Second)
}

func TestSwitchTabs(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Type a marker in tab 1
	nxt.Write([]byte("echo TAB1_MARKER\r"))
	nxt.WaitFor("TAB1_MARKER", 10*time.Second)

	// Spawn second region. Tab 2 becomes active, tab 1 becomes
	// inactive — its label flips from " 1 " to " 1:bash ", so
	// "1:bash" appearing tells us the spawn took effect.
	nxt.Write([]byte("\x02c"))
	nxt.WaitFor("1:bash", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Type a marker in tab 2
	nxt.Write([]byte("echo TAB2_MARKER\r"))
	nxt.WaitFor("TAB2_MARKER", 10*time.Second)

	// Switch to tab 1
	nxt.Write([]byte("\x021"))

	// Tab 1 content should be restored with TAB1_MARKER visible
	// and TAB2_MARKER NOT visible (they're in separate sessions).
	nxt.WaitForScreen(func(lines []string) bool {
		hasTAB1 := false
		for _, line := range lines {
			if strings.Contains(line, "TAB2_MARKER") {
				return false
			}
			if strings.Contains(line, "TAB1_MARKER") {
				hasTAB1 = true
			}
		}
		return hasTAB1
	}, "tab 1 with TAB1_MARKER and without TAB2_MARKER", 10*time.Second)
}

func TestRegionDestroyedRemovesTab(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Spawn second region (tab 1 becomes inactive → "1:bash" appears).
	nxt.Write([]byte("\x02c"))
	nxt.WaitFor("1:bash", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Exit the shell in tab 2
	nxt.Write([]byte("exit\r"))

	// Wait for tab bar to drop tab 2. Tab 1 becomes the sole active
	// tab again (label " 1 " with no program name), so "1:bash"
	// disappears from the tab bar.
	nxt.WaitForScreen(func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return !strings.Contains(lines[0], "1:bash") && !strings.Contains(lines[0], "2:bash")
	}, "tab bar with only the active tab 1", 10*time.Second)

	// Verify terminal is still functional
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Write([]byte("echo ALIVE\r"))
	nxt.WaitFor("ALIVE", 10*time.Second)
}

func TestCloseTab(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Spawn second region (tab 1 becomes inactive → "1:bash" appears).
	nxt.Write([]byte("\x02c"))
	nxt.WaitFor("1:bash", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Close tab 2 with ctrl+b x
	nxt.Write([]byte("\x02x"))

	// Tab 2 closed → tab 1 alone, active, so neither "1:bash" nor
	// "2:bash" remains in the tab bar.
	nxt.WaitForScreen(func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return !strings.Contains(lines[0], "1:bash") && !strings.Contains(lines[0], "2:bash")
	}, "tab bar with only the active tab 1", 10*time.Second)

	// Verify terminal is still functional
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Write([]byte("echo STILL_ALIVE\r"))
	nxt.WaitFor("STILL_ALIVE", 10*time.Second)
}

// TestSpawnNoGhostTab verifies that spawning a new region creates exactly
// one tab, not two. A ghost tab would appear if both the SpawnResponse
// and tree sync each create a tab for the same region.
func TestSpawnNoGhostTab(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Spawn a second region.
	nxt.Write([]byte("\x02c"))
	nxt.WaitFor("1:bash", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Wait a moment for any delayed tree events to be processed.
	time.Sleep(500 * time.Millisecond)

	// The tab bar should show exactly 2 tabs: "1:bash" (inactive) and
	// " 2 " (active). A ghost tab would show "3:bash" or "3:" as a
	// third tab label.
	screen := nxt.ScreenLines()
	tabBar := screen[0]
	if strings.Contains(tabBar, "3:") {
		t.Errorf("ghost tab detected: tab bar shows a third tab: %q", tabBar)
	}
	// Tab 2 is active, so it renders as " 2 " (no program name).
	// If it shows "2:bash" that means tab 2 is inactive and something
	// else (a ghost tab) is active.
	if strings.Contains(tabBar, "2:bash") {
		t.Errorf("tab 2 appears inactive (ghost tab is active): %q", tabBar)
	}
}
