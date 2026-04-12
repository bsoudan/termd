package e2e

import (
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
	"nxtermd/internal/nxtest"
)

func TestReconnectUnix(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Type a marker so we can verify content persists
	nxt.Write([]byte("echo reconnect_marker\r"))
	nxt.WaitFor("reconnect_marker", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Find the frontend's client ID
	clientID := findFrontendClientID(t, socketPath)

	// Kill the client connection
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	// Should see "reconnecting..." in the tab bar
	nxt.WaitFor("reconnecting", 10*time.Second)

	// Should reconnect and show the prompt again
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Verify typing still works after reconnect
	nxt.Write([]byte("echo after_reconnect\r"))
	nxt.WaitFor("after_reconnect", 10*time.Second)
}

func TestReconnectTCP(t *testing.T) {
	t.Parallel()
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Connect frontend via TCP
	cmd := exec.Command("nxterm", "--socket", "tcp:"+tcpAddr)
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via TCP: %v", err)
	}
	nxt := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Find the frontend's client ID (use Unix socket for termctl)
	clientID := findFrontendClientID(t, socketPath)

	// Kill the client connection
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	// Should reconnect
	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Verify typing works
	nxt.Write([]byte("echo tcp_reconnected\r"))
	nxt.WaitFor("tcp_reconnected", 10*time.Second)
}
