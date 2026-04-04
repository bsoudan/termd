package e2e

import (
	"testing"
	"time"
)

// connectViaUI sends ctrl+b S o to open the connect overlay, types the
// socket address, and presses enter. Waits for a shell prompt.
func connectViaUI(t *testing.T, pio *ptyIO, socketPath string) {
	t.Helper()
	pio.Write([]byte("\x02So"))
	pio.WaitFor(t, "type a server address", 5*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)
	pio.Write([]byte(socketPath))
	time.Sleep(100 * time.Millisecond)
	pio.Write([]byte("\r"))
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

	connectViaUI(t, pio, socketPath)

	pio.Write([]byte("echo RECONNECTED\r"))
	pio.WaitFor(t, "RECONNECTED", 10*time.Second)
}

func TestKillSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontCleanup := startFrontend(t, socketPath)
	defer frontCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	pio.Write([]byte{0x02, 'S', 'c'})

	pio.WaitFor(t, "no session", 10*time.Second)
}

func TestConnectOverlayCancel(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontCleanup := startFrontend(t, socketPath)
	defer frontCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	pio.Write([]byte("\x02So"))
	pio.WaitFor(t, "type a server address", 5*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	pio.Write([]byte{0x1b})

	pio.WaitFor(t, "termd$", 5*time.Second)
}
