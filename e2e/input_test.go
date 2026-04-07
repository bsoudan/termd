package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestInputRoundTrip(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// "aGVsbG8K" is base64 for "hello\n".
	pio.Write([]byte("echo aGVsbG8K | base64 -d\r"))

	// Wait for "hello" at col 0 — the decoded output on its own line.
	// Don't match "hello" embedded in a prompt or command echo.
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "hello") {
				return true
			}
		}
		return false
	}, "'hello' at col 0 on a content row", 10*time.Second)

	row, col := -1, -1
	for i := 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "hello") {
			row, col = i-1, 0
			break
		}
	}
	t.Logf("'hello' at content row %d, col %d", row, col)
	if col != 0 {
		t.Fatalf("expected 'hello' at col 0, found at col %d", col)
	}
}

func TestRawInputPassthrough(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)
	pio.Write([]byte("sleep 999\r"))
	pio.Write([]byte("\x03"))

	pio.Write([]byte("echo raw_input_works\r"))
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "raw_input_works")
		return row >= 0
	}, "'raw_input_works' on a content row", 10*time.Second)

	row, col := findOnScreen(lines[1:], "raw_input_works")
	t.Logf("'raw_input_works' at content row %d, col %d", row, col)
}

func TestMousePassthrough(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Run mousehelper which enables mouse tracking and prints mouse events
	pio.Write([]byte("mousehelper\r"))
	// Wait for mouse mode to be enabled — the helper prints nothing until
	// it receives a mouse event, but we need to give it time to start
	time.Sleep(500 * time.Millisecond)

	// waitForMouse checks the screen for a specific MOUSE line.
	waitForMouse := func(expected string) {
		t.Helper()
		pio.WaitForScreen(t, func(lines []string) bool {
			for _, line := range lines {
				if strings.Contains(line, expected) {
					return true
				}
			}
			return false
		}, expected, 5*time.Second)
	}

	// Coordinates sent are in outer terminal space (1-based SGR).
	// The tab bar occupies row 1, so the frontend adjusts row by -1
	// before forwarding to the child. mousehelper prints what it receives.

	// Left click at col 5, row 3 → child sees row 2
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;3M", ansi.ESC)))
	waitForMouse("MOUSE press 0 5 2")

	// Left release
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;3m", ansi.ESC)))
	waitForMouse("MOUSE release 0 5 2")

	// Right click (button 2) at row 4 → child sees row 3
	pio.Write([]byte(fmt.Sprintf("%c[<2;10;4M", ansi.ESC)))
	waitForMouse("MOUSE press 2 10 3")

	// Middle click (button 1) at row 6 → child sees row 5
	pio.Write([]byte(fmt.Sprintf("%c[<1;8;6M", ansi.ESC)))
	waitForMouse("MOUSE press 1 8 5")

	// Scroll wheel up at row 3 → child sees row 2
	pio.Write([]byte(fmt.Sprintf("%c[<64;5;3M", ansi.ESC)))
	waitForMouse("MOUSE wheelup 64 5 2")

	// Scroll wheel down at row 3 → child sees row 2
	pio.Write([]byte(fmt.Sprintf("%c[<65;5;3M", ansi.ESC)))
	waitForMouse("MOUSE wheeldown 65 5 2")

	// Motion event (button 32 = motion + left held) at row 7 → child sees row 6
	pio.Write([]byte(fmt.Sprintf("%c[<32;12;7M", ansi.ESC)))
	waitForMouse("MOUSE press 32 12 6")

	// Click on the tab bar (row 1) → clamped to child row 1
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;1M", ansi.ESC)))
	waitForMouse("MOUSE press 0 5 1")

	// Click on content row 1 (row 2 in outer) → child sees row 1
	pio.Write([]byte(fmt.Sprintf("%c[<0;20;2M", ansi.ESC)))
	waitForMouse("MOUSE press 0 20 1")

	// Quit the helper
	pio.Write([]byte("q"))
	pio.WaitFor(t, "nxterm$",10*time.Second)
}

func TestMouseAfterTabSwitch(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Run mousehelper in tab 1
	pio.Write([]byte("mousehelper\r"))
	time.Sleep(500 * time.Millisecond)

	// Verify mouse works initially
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;3M", ansi.ESC)))
	pio.WaitFor(t, "MOUSE press 0 5 2", 5*time.Second)

	// Spawn tab 2 (switches to it automatically). Tab 1 becomes
	// inactive so "1:bash" now appears in the tab bar.
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Switch back to tab 1 (mousehelper)
	pio.Write([]byte("\x021"))
	pio.WaitForSilence(200 * time.Millisecond)

	// Mouse should still work after switching back
	pio.Write([]byte(fmt.Sprintf("%c[<0;10;4M", ansi.ESC)))
	pio.WaitFor(t, "MOUSE press 0 10 3", 5*time.Second)

	// Quit the helper
	pio.Write([]byte("q"))
	pio.WaitFor(t, "nxterm$", 10*time.Second)
}

func TestInputIsolation(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Type a marker in tab 1 so we can identify its screen
	pio.Write([]byte("echo TAB1_HERE\r"))
	pio.WaitFor(t, "TAB1_HERE", 10*time.Second)

	// Spawn second region. Tab 1 becomes inactive → "1:bash" appears.
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Type in tab 2
	pio.Write([]byte("echo ONLY_IN_TAB2\r"))
	pio.WaitFor(t, "ONLY_IN_TAB2", 10*time.Second)

	// Switch to tab 1 and wait for tab 1's content to appear
	pio.Write([]byte("\x021"))

	// Wait for tab 1 screen: must have TAB1_HERE and must NOT have ONLY_IN_TAB2
	pio.WaitForScreen(t, func(lines []string) bool {
		hasTab1 := false
		for _, line := range lines {
			if strings.Contains(line, "TAB1_HERE") {
				hasTab1 = true
			}
			if strings.Contains(line, "ONLY_IN_TAB2") {
				return false
			}
		}
		return hasTab1
	}, "tab 1 screen with TAB1_HERE and without ONLY_IN_TAB2", 10*time.Second)

	// Switch back to tab 2 and verify content is there
	pio.Write([]byte("\x022"))
	pio.WaitFor(t, "ONLY_IN_TAB2", 10*time.Second)
}
