package e2e

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeOverlayRender(t *testing.T) {
	socketPath, cleanup := startServer(t)
	defer cleanup()

	pio, feCleanup := startFrontend(t, socketPath)
	defer feCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	// Run the overlay app from the shell.
	pio.Write([]byte("nativeapp\r"))

	// The overlay should composite "NATIVE" on top of the shell.
	pio.WaitFor(t, "NATIVE", 10*time.Second)
}

func TestNativeOverlayInput(t *testing.T) {
	socketPath, cleanup := startServer(t)
	defer cleanup()

	pio, feCleanup := startFrontend(t, socketPath)
	defer feCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	pio.Write([]byte("nativeapp\r"))
	pio.WaitFor(t, "NATIVE", 10*time.Second)

	// Type some characters — nativeapp echoes them as "INPUT:hello".
	pio.Write([]byte("hello"))
	pio.WaitFor(t, "INPUT:hello", 10*time.Second)
}

func TestNativeOverlayGetScreen(t *testing.T) {
	socketPath, cleanup := startServer(t)
	defer cleanup()

	pio, feCleanup := startFrontend(t, socketPath)
	defer feCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	// Get the region ID.
	out := runTermctl(t, socketPath, "region", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected region in list, got: %s", out)
	}
	regionID := strings.Fields(lines[1])[0]

	// Run the overlay app.
	pio.Write([]byte("nativeapp\r"))
	pio.WaitFor(t, "NATIVE", 10*time.Second)

	// Mouse click before refresh — row 3 in outer terminal = row 2 in child
	// (tab bar occupies row 0). Left click at col 10, row 3.
	pio.Write([]byte(fmt.Sprintf("%c[<0;10;3M", ansi.ESC)))
	pio.WaitFor(t, "MOUSE:press:0:9:1", 10*time.Second)

	// termctl region view uses get_screen_request — overlay must be included.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out = runTermctl(t, socketPath, "region", "view", regionID)
		if strings.Contains(out, "NATIVE") {
			break
		}
		runtime.Gosched()
	}
	if !strings.Contains(out, "NATIVE") {
		t.Fatal("get_screen_request did not include overlay (expected 'NATIVE')")
	}

	// After get_screen, keyboard input must still work (modes survived refresh).
	pio.Write([]byte("world"))
	pio.WaitFor(t, "INPUT:world", 10*time.Second)

	// Mouse click after refresh — modes must still be active.
	pio.Write([]byte(fmt.Sprintf("%c[<0;20;4M", ansi.ESC)))
	pio.WaitFor(t, "MOUSE:press:0:19:2", 10*time.Second)
}

func TestNativeOverlayExit(t *testing.T) {
	socketPath, cleanup := startServer(t)
	defer cleanup()

	pio, feCleanup := startFrontend(t, socketPath)
	defer feCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	pio.Write([]byte("nativeapp\r"))
	pio.WaitFor(t, "NATIVE", 10*time.Second)

	// Ctrl-C to kill nativeapp — overlay should be removed.
	pio.Write([]byte{3}) // Ctrl-C
	pio.WaitForScreen(t, func(screen []string) bool {
		for _, line := range screen {
			if strings.Contains(line, "NATIVE") {
				return false
			}
		}
		return true
	}, "NATIVE gone from screen after Ctrl-C", 10*time.Second)

	// Shell prompt should reappear.
	pio.WaitFor(t, "termd$", 10*time.Second)
}
