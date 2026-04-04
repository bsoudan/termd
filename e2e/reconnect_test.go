package e2e

import (
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestReconnectUnix(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Type a marker so we can verify content persists
	pio.Write([]byte("echo reconnect_marker\r"))
	pio.WaitFor(t, "reconnect_marker", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Find the frontend's client ID
	clientID := findFrontendClientID(t, socketPath)

	// Kill the client connection
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	// Should see "reconnecting..." in the tab bar
	pio.WaitFor(t, "reconnecting", 10*time.Second)

	// Should reconnect and show the prompt again
	pio.WaitFor(t, "nxterm$",10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Verify typing still works after reconnect
	pio.Write([]byte("echo after_reconnect\r"))
	pio.WaitFor(t, "after_reconnect", 10*time.Second)
}

func TestReconnectTCP(t *testing.T) {
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Connect frontend via TCP
	cmd := exec.Command("nxterm", "--socket", "tcp:"+tcpAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via TCP: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Find the frontend's client ID (use Unix socket for termctl)
	clientID := findFrontendClientID(t, socketPath)

	// Kill the client connection
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	// Should reconnect
	pio.WaitFor(t, "reconnecting", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Verify typing works
	pio.Write([]byte("echo tcp_reconnected\r"))
	pio.WaitFor(t, "tcp_reconnected", 10*time.Second)
}
