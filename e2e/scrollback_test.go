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
	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()
	pio.WaitFor(t, "termd$",10*time.Second)

	// Output 200 lines — in a 24-row terminal, early lines scroll off
	pio.Write([]byte("seq 1 200\r"))
	pio.WaitFor(t, "termd$",10*time.Second)

	// Poll scrollback via termctl until early numbers are present.
	// The server's terminal emulator may still be processing output
	// even after the frontend shows the prompt.
	want := []string{"1", "2", "3", "10", "50"}
	deadline := time.After(10 * time.Second)
	for {
		out := runTermctl(t, socketPath, "region", "scrollback", regionID)
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

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Generate enough output to fill scrollback
	pio.Write([]byte("seq 1 200\r"))
	pio.WaitFor(t, "termd$",10*time.Second)

	// Enter scrollback mode with ctrl+b [
	pio.Write([]byte{0x02, '['})

	// Tab bar should show "scrollback"
	pio.WaitForScreen(t, func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback indicator in tab bar", 5*time.Second)

	// Page up several times to reach early numbers
	for range 20 {
		pio.Write([]byte{0x15}) // ctrl+u = page up
		time.Sleep(30 * time.Millisecond)
	}

	// Verify early numbers appear on screen.
	// Use Fields[0] to ignore the scrollbar column at the right edge.
	pio.WaitForScreen(t, func(lines []string) bool {
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
	pio.Write([]byte("q"))

	// Tab bar should no longer show "scrollback" and prompt should be visible
	pio.WaitForScreen(t, func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "termd$") {
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

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	// Generate enough output to fill scrollback
	pio.Write([]byte("seq 1 200\r"))
	pio.WaitFor(t, "termd$", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Send PageUp (\x1b[5~) — should activate scrollback
	pio.Write([]byte("\x1b[5~"))

	// Tab bar should show "scrollback"
	pio.WaitForScreen(t, func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback activated by PageUp", 5*time.Second)

	// Send more PageUp keys to scroll further back
	for range 20 {
		pio.Write([]byte("\x1b[5~"))
		time.Sleep(30 * time.Millisecond)
	}

	// Verify early numbers appear on screen.
	pio.WaitForScreen(t, func(lines []string) bool {
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
	pio.Write([]byte("q"))

	pio.WaitForScreen(t, func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "termd$") {
				return true
			}
		}
		return false
	}, "prompt visible after scrollback exit", 5*time.Second)

	// Now test PageDown — should also activate scrollback (at offset 0)
	pio.Write([]byte("\x1b[6~"))

	pio.WaitForScreen(t, func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback activated by PageDown", 5*time.Second)

	// Exit with q
	pio.Write([]byte("q"))

	pio.WaitForScreen(t, func(lines []string) bool {
		return !strings.Contains(lines[0], "scrollback")
	}, "scrollback exited after PageDown test", 5*time.Second)
}

func TestScrollbackScrollWheel(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Generate output that scrolls off screen
	pio.Write([]byte("seq 1 200\r"))
	pio.WaitFor(t, "termd$",10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Send a scroll wheel up event to activate scrollback
	pio.Write([]byte(fmt.Sprintf("%c[<64;5;5M", ansi.ESC)))

	// Wait for scrollback data to arrive (not just mode activation)
	pio.WaitForScreen(t, func(lines []string) bool {
		// Tab bar should show scrollback with non-zero total
		return strings.Contains(lines[0], "scrollback") &&
			!strings.Contains(lines[0], "/0]")
	}, "scrollback data loaded", 5*time.Second)

	// Send more scroll wheel up events to scroll to the top
	for range 70 {
		pio.Write([]byte(fmt.Sprintf("%c[<64;5;5M", ansi.ESC)))
		time.Sleep(20 * time.Millisecond)
	}

	// Verify early numbers appear on screen.
	pio.WaitForScreen(t, func(lines []string) bool {
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
		pio.Write([]byte(fmt.Sprintf("%c[<65;5;5M", ansi.ESC)))
		time.Sleep(20 * time.Millisecond)
	}

	// Verify scrollback exited and prompt is visible
	pio.WaitForScreen(t, func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "termd$") {
				return true
			}
		}
		return false
	}, "prompt visible after scroll down exit", 5*time.Second)
}
