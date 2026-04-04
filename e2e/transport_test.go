package e2e

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestTCPTransport(t *testing.T) {
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Spawn a region via the Unix socket (termctl)
	_ = runNxtermctl(t, socketPath, "region", "spawn", "shell")

	// Connect frontend via TCP
	cmd := exec.Command("nxterm", "--socket", "tcp:"+tcpAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via TCP: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Verify the tab bar shows the TCP endpoint
	lines := pio.ScreenLines()
	row0 := lines[0]
	if !strings.Contains(row0, tcpAddr) {
		t.Errorf("tab bar should show TCP addr %q, got: %q", tcpAddr, row0)
	}

	// Type a command and verify round-trip works
	pio.Write([]byte("echo tcp_works\r"))
	pio.WaitFor(t, "tcp_works", 10*time.Second)
}

func TestWebSocketTransport(t *testing.T) {
	socketPath, addrs, serverCleanup := startServerWithListeners(t, "ws://127.0.0.1:0")
	defer serverCleanup()

	var wsAddr string
	for _, a := range addrs {
		wsAddr = a
	}
	if wsAddr == "" {
		t.Fatal("could not find WS listen address")
	}

	// Spawn a region via Unix socket
	_ = runNxtermctl(t, socketPath, "region", "spawn", "shell")

	// Connect frontend via WebSocket
	cmd := exec.Command("nxterm", "--socket", "ws://"+wsAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via WS: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	pio.Write([]byte("echo ws_works\r"))
	pio.WaitFor(t, "ws_works", 10*time.Second)
}

func TestSSHTransport(t *testing.T) {
	dir := t.TempDir()
	hostKeyPath := filepath.Join(dir, "host_key")

	env := testEnv(t)
	writeTestServerConfig(t, env)

	// Start server with Unix + SSH (no auth keys = accept all for test)
	socketPath := filepath.Join(dir, "nxtermd.sock")
	cmd := exec.Command("nxtermd",
		"--ssh-host-key", hostKeyPath,
		"--ssh-no-auth",
		"unix:"+socketPath, "ssh://127.0.0.1:0")
	cmd.Env = env
	stderrR, stderrW, _ := os.Pipe()
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	stderrW.Close()

	// Parse SSH listen address from stderr
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
		stderrR.Close()
	}()

	var sshAddr string
	deadline := time.Now().Add(5 * time.Second)
	found := 0
	for found < 2 && time.Now().Before(deadline) {
		select {
		case line := <-lines:
			if idx := strings.Index(line, "addr="); idx >= 0 {
				addr := line[idx+len("addr="):]
				if sp := strings.IndexByte(addr, ' '); sp >= 0 {
					addr = addr[:sp]
				}
				if strings.Contains(addr, ":") {
					sshAddr = addr
				}
				found++
			}
		case <-time.After(5 * time.Second):
		}
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	if sshAddr == "" {
		t.Fatal("could not find SSH listen address")
	}

	// Spawn a region via Unix socket
	_ = runNxtermctl(t, socketPath, "region", "spawn", "shell")

	// Connect frontend via SSH
	feCmd := exec.Command("nxterm", "--socket", "ssh://"+sshAddr, )
	feCmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(feCmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via SSH: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { feCmd.Process.Kill(); feCmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	pio.Write([]byte("echo ssh_works\r"))
	pio.WaitFor(t, "ssh_works", 10*time.Second)
}

func TestMultiTransportSharedRegion(t *testing.T) {
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Start frontend 1 via Unix socket
	pio1, cleanup1 := startFrontend(t, socketPath)
	defer cleanup1()

	pio1.WaitFor(t, "nxterm$",10*time.Second)

	// Type a marker in frontend 1
	pio1.Write([]byte("echo multi_transport_marker\r"))
	pio1.WaitFor(t, "multi_transport_marker", 10*time.Second)

	// Start frontend 2 via TCP (subscribes to the same region)
	cmd := exec.Command("nxterm", "--socket", "tcp:"+tcpAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend 2 via TCP: %v", err)
	}
	pio2 := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	// Frontend 2 should see the marker (it gets the screen snapshot on subscribe)
	pio2.WaitFor(t, "multi_transport_marker", 10*time.Second)

	// Type in frontend 2, verify frontend 1 sees it
	pio2.WaitFor(t, "nxterm$",10*time.Second)
	pio2.Write([]byte("echo from_tcp_client\r"))
	pio1.WaitFor(t, "from_tcp_client", 10*time.Second)
}

// findFrontendClientID returns the client ID of the nxterm process.
func findFrontendClientID(t *testing.T, socketPath string) string {
	t.Helper()
	out := runNxtermctl(t, socketPath, "client", "list")
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for _, f := range fields {
			if f == "nxterm" && len(fields) > 0 {
				return fields[0]
			}
		}
	}
	t.Fatal("could not find nxterm client ID")
	return ""
}
