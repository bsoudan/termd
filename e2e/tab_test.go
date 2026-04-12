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

	// Tab 1 content should be restored (subscribe sends screen snapshot)
	nxt.WaitFor("TAB1_MARKER", 10*time.Second)

	// TAB2_MARKER should NOT be on screen
	lines := nxt.ScreenLines()
	for _, line := range lines {
		if strings.Contains(line, "TAB2_MARKER") {
			t.Fatalf("TAB2_MARKER should not be visible on tab 1")
		}
	}
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
