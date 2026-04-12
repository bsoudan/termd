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
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Run the overlay app from the shell.
	nxt.Write([]byte("nativeapp\r"))

	// The overlay should composite "NATIVE" on top of the shell.
	nxt.WaitFor("NATIVE", 10*time.Second)
}

func TestNativeOverlayInput(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte("nativeapp\r"))
	nxt.WaitFor("NATIVE", 10*time.Second)

	// Type some characters — nativeapp echoes them as "INPUT:hello".
	nxt.Write([]byte("hello"))
	nxt.WaitFor("INPUT:hello", 10*time.Second)
}

func TestNativeOverlayGetScreen(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Get the region ID.
	out := runNxtermctl(t, socketPath, "region", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected region in list, got: %s", out)
	}
	regionID := strings.Fields(lines[1])[0]

	// Run the overlay app.
	nxt.Write([]byte("nativeapp\r"))
	nxt.WaitFor("NATIVE", 10*time.Second)

	// Mouse click before refresh — row 3 in outer terminal = row 2 in child
	// (tab bar occupies row 0). Left click at col 10, row 3.
	nxt.Write([]byte(fmt.Sprintf("%c[<0;10;3M", ansi.ESC)))
	nxt.WaitFor("MOUSE:press:0:9:1", 10*time.Second)

	// nxtermctl region view uses get_screen_request — overlay must be included.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out = runNxtermctl(t, socketPath, "region", "view", regionID)
		if strings.Contains(out, "NATIVE") {
			break
		}
		runtime.Gosched()
	}
	if !strings.Contains(out, "NATIVE") {
		t.Fatal("get_screen_request did not include overlay (expected 'NATIVE')")
	}

	// After get_screen, keyboard input must still work (modes survived refresh).
	nxt.Write([]byte("world"))
	nxt.WaitFor("INPUT:world", 10*time.Second)

	// Mouse click after refresh — modes must still be active.
	nxt.Write([]byte(fmt.Sprintf("%c[<0;20;4M", ansi.ESC)))
	nxt.WaitFor("MOUSE:press:0:19:2", 10*time.Second)
}

func TestNativeOverlayExit(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte("nativeapp\r"))
	nxt.WaitFor("NATIVE", 10*time.Second)

	// Ctrl-C to kill nativeapp — overlay should be removed.
	nxt.Write([]byte{3}) // Ctrl-C
	nxt.WaitForScreen(func(screen []string) bool {
		for _, line := range screen {
			if strings.Contains(line, "NATIVE") {
				return false
			}
		}
		return true
	}, "NATIVE gone from screen after Ctrl-C", 10*time.Second)

	// Shell prompt should reappear.
	nxt.WaitFor("nxterm$", 10*time.Second)
}
