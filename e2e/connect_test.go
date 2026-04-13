package e2e

import (
	"strings"
	"testing"
	"time"

	"nxtermd/internal/nxtest"
)

// TestConnectLayerInput verifies that the connect layer accepts keyboard
// input when nxterm starts without a server connection (--browse mode).
// This catches a bug where drainUntil didn't process the focus buffer,
// leaving all input stuck.
func TestConnectLayerInput(t *testing.T) {
	t.Parallel()

	// Start nxterm in disconnected mode (--browse). We pass a dummy
	// socket that doesn't exist so it starts with the connect overlay.
	env := testEnv(t)
	fe, err := nxtest.StartFrontend("/nonexistent/nxtermd.sock", env, 80, 24, "--browse")
	if err != nil {
		t.Fatal(err)
	}
	nxt := nxtest.NewFromFrontend(t, fe)
	defer nxt.Kill()

	// The connect layer should be visible.
	nxt.WaitFor("connect to server", 5*time.Second)

	// Type something into the address input.
	nxt.Write([]byte("unix:/tmp/test.sock"))
	time.Sleep(500 * time.Millisecond)

	// The typed text should appear on screen.
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.Contains(line, "unix:/tmp/test.sock") {
				return true
			}
		}
		return false
	}, "typed address visible in connect layer", 5*time.Second)

	// Pressing Esc should quit (ConnectLayer returns QuitLayerMsg on Esc).
	nxt.Write([]byte{0x1b}) // Esc
	nxt.Wait(5 * time.Second)
}
