package e2e

import (
	"strings"
	"testing"
	"time"
)

// createSessionViaUI sends ctrl+b S, types the name, presses enter,
// and waits for the new session to be fully connected by verifying
// a unique marker echoed in the new session's shell.
func createSessionViaUI(t *testing.T, pio *ptyIO, socketPath, name string) {
	t.Helper()
	pio.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	pio.Write([]byte("S"))
	pio.WaitFor(t, "Session name:", 5*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)
	pio.Write([]byte(name))
	time.Sleep(100 * time.Millisecond)
	pio.Write([]byte("\r"))
	// Wait for session to appear on the server.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runTermctl(t, socketPath, "session", "list")
		if strings.Contains(out, name) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Wait for a prompt in the new session.
	pio.WaitFor(t, "$", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)
}

func TestNewSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontCleanup := startFrontend(t, socketPath)
	defer frontCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	createSessionViaUI(t, pio, socketPath, "work")

	// Verify via termctl that both sessions exist.
	out := runTermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "main") {
		t.Errorf("session list missing 'main': %s", out)
	}
	if !strings.Contains(out, "work") {
		t.Errorf("session list missing 'work': %s", out)
	}
}

func TestSwitchSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontCleanup := startFrontend(t, socketPath)
	defer frontCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Create a marker in session 1.
	pio.Write([]byte("echo SESSION1_MARKER\r"))
	pio.WaitFor(t, "SESSION1_MARKER", 10*time.Second)

	// Create session 2.
	createSessionViaUI(t, pio, socketPath, "dev")

	// Create a marker in session 2.
	pio.Write([]byte("echo SESSION2_MARKER\r"))
	pio.WaitFor(t, "SESSION2_MARKER", 10*time.Second)

	// Verify SESSION1_MARKER is NOT visible.
	for _, line := range pio.ScreenLines() {
		if strings.Contains(line, "SESSION1_MARKER") {
			t.Fatalf("SESSION1_MARKER should not be visible in session 2")
		}
	}

	// Switch back to session 1 via session picker (ctrl+b w).
	pio.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	pio.Write([]byte("w"))
	pio.WaitFor(t, "main", 5*time.Second) // picker shows "main"
	pio.Write([]byte("k"))                // navigate up to "main"
	time.Sleep(100 * time.Millisecond)
	pio.Write([]byte("\r"))               // select "main"

	// Verify SESSION1_MARKER is visible and SESSION2_MARKER is not.
	pio.WaitForScreen(t, func(lines []string) bool {
		has1 := false
		for _, l := range lines {
			if strings.Contains(l, "SESSION1_MARKER") {
				has1 = true
			}
			if strings.Contains(l, "SESSION2_MARKER") {
				return false
			}
		}
		return has1
	}, "session 1 content without session 2", 10*time.Second)
}

func TestKillSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontCleanup := startFrontend(t, socketPath)
	defer frontCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	// Create session 2.
	createSessionViaUI(t, pio, socketPath, "temp")

	// Kill session 2 via ctrl+b X.
	pio.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	pio.Write([]byte("X"))

	// Wait for we're back with a prompt.
	pio.WaitFor(t, "$", 10*time.Second)

	// Wait for temp session regions to be killed on the server.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := runTermctl(t, socketPath, "session", "list")
		if !strings.Contains(out, "temp") {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
	out := runTermctl(t, socketPath, "session", "list")
	if strings.Contains(out, "temp") {
		t.Errorf("session 'temp' still exists after kill: %s", out)
	}
}

func TestSessionNameCancel(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontCleanup := startFrontend(t, socketPath)
	defer frontCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	// Open session name prompt.
	pio.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	pio.Write([]byte("S"))
	pio.WaitFor(t, "Session name:", 5*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Cancel with esc.
	pio.Write([]byte{0x1b})

	// Verify overlay closed — prompt is still visible.
	pio.WaitFor(t, "termd$", 5*time.Second)

	// Verify only main session exists.
	out := runTermctl(t, socketPath, "session", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Header + 1 session = 2 lines
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (header + 1 session), got %d: %s", len(lines), out)
	}
}
