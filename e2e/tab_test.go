package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestSpawnSecondRegion(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// Wait for initial tab and prompt
	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// ctrl+b c to spawn a second region
	pio.Write([]byte("\x02c"))

	// Wait for tab bar to show both tabs
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && strings.Contains(lines[0], "2:bash")
	}, "tab bar with '1:bash' and '2:bash'", 10*time.Second)

	// New tab should have a prompt
	pio.WaitFor(t, "nxterm$", 10*time.Second)
}

func TestSwitchTabs(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Type a marker in tab 1
	pio.Write([]byte("echo TAB1_MARKER\r"))
	pio.WaitFor(t, "TAB1_MARKER", 10*time.Second)

	// Spawn second region
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Type a marker in tab 2
	pio.Write([]byte("echo TAB2_MARKER\r"))
	pio.WaitFor(t, "TAB2_MARKER", 10*time.Second)

	// Switch to tab 1
	pio.Write([]byte("\x021"))

	// Tab 1 content should be restored (subscribe sends screen snapshot)
	pio.WaitFor(t, "TAB1_MARKER", 10*time.Second)

	// TAB2_MARKER should NOT be on screen
	lines := pio.ScreenLines()
	for _, line := range lines {
		if strings.Contains(line, "TAB2_MARKER") {
			t.Fatalf("TAB2_MARKER should not be visible on tab 1")
		}
	}
}

func TestRegionDestroyedRemovesTab(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Spawn second region
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Exit the shell in tab 2
	pio.Write([]byte("exit\r"))

	// Wait for tab bar to show only tab 1 (tab 2 removed)
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && !strings.Contains(lines[0], "2:bash")
	}, "tab bar with only '1:bash'", 10*time.Second)

	// Verify terminal is still functional
	pio.WaitFor(t, "nxterm$", 10*time.Second)
	pio.Write([]byte("echo ALIVE\r"))
	pio.WaitFor(t, "ALIVE", 10*time.Second)
}

func TestCloseTab(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Spawn second region
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Close tab 2 with ctrl+b x
	pio.Write([]byte("\x02x"))

	// Wait for tab bar to show only tab 1
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && !strings.Contains(lines[0], "2:bash")
	}, "tab bar with only '1:bash'", 10*time.Second)

	// Verify terminal is still functional
	pio.WaitFor(t, "nxterm$", 10*time.Second)
	pio.Write([]byte("echo STILL_ALIVE\r"))
	pio.WaitFor(t, "STILL_ALIVE", 10*time.Second)
}
