package e2e

import (
	"testing"
	"time"

	"nxtermd/internal/nxtest"
)

// connectViaUI sends ctrl+b S o to open the connect overlay, types the
// socket address, and presses enter. Waits for a shell prompt.
func connectViaUI(t *testing.T, nxt *nxtest.T, socketPath string) {
	t.Helper()
	nxt.Write([]byte("\x02So"))
	nxt.WaitFor("type a server address", 5*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)
	nxt.Write([]byte(socketPath))
	time.Sleep(100 * time.Millisecond)
	nxt.Write([]byte("\r"))
	nxt.WaitFor("$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)
}

func TestNewSession(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	connectViaUI(t, nxt, socketPath)

	nxt.Write([]byte("echo RECONNECTED\r"))
	nxt.WaitFor("RECONNECTED", 10*time.Second)
}

func TestKillSession(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte{0x02, 'S', 'c'})

	nxt.WaitFor("no session", 10*time.Second)
}

func TestConnectOverlayCancel(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte("\x02So"))
	nxt.WaitFor("type a server address", 5*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	nxt.Write([]byte{0x1b})

	nxt.WaitFor("nxterm$", 5*time.Second)
}
