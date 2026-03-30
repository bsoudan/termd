package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"termd/transport"
)

// TestLiveUpgrade starts a server with Unix, TCP, WebSocket, and SSH
// listeners. Connects a frontend to each. Triggers SIGUSR2 and verifies
// all four frontends reconnect and their shells are still alive.
func TestLiveUpgrade(t *testing.T) {
	dir := t.TempDir()
	env := testEnv(t)
	writeTestServerConfig(t, env)

	socketPath := filepath.Join(dir, "termd.sock")
	hostKeyPath := filepath.Join(dir, "host_key")

	// Start server with all transport types.
	cmd := exec.Command("termd",
		"--ssh-host-key", hostKeyPath,
		"--ssh-no-auth",
		"unix:"+socketPath,
		"tcp://127.0.0.1:0",
		"ws://127.0.0.1:0",
		"ssh://127.0.0.1:0",
	)
	cmd.Env = env

	stderrR, stderrW, _ := os.Pipe()
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	stderrW.Close()
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	// Parse listen addresses from stderr.
	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
		stderrR.Close()
	}()

	// Wait for 4 "listening" lines (unix + tcp + ws + ssh).
	addrs := make(map[string]string) // "tcp" → "127.0.0.1:PORT"
	deadline := time.Now().Add(5 * time.Second)
	found := 0
	for found < 4 && time.Now().Before(deadline) {
		select {
		case line, ok := <-lines:
			if !ok {
				break
			}
			if idx := strings.Index(line, "addr="); idx >= 0 {
				addr := line[idx+len("addr="):]
				if sp := strings.IndexByte(addr, ' '); sp >= 0 {
					addr = addr[:sp]
				}
				if strings.Contains(addr, ":") {
					// Last TCP-like addr seen; keep all of them.
					addrs[fmt.Sprintf("addr%d", found)] = addr
				}
				found++
			}
		case <-time.After(5 * time.Second):
			break
		}
	}

	// Wait for Unix socket to appear.
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// We need to figure out which addr is TCP, WS, SSH.
	// Connect to each and read the identify message to get the PID.
	// Also use the order: the server logs them in the order they were passed.
	// Order: unix, tcp, ws, ssh → addrs: addr0=unix, addr1=tcp, addr2=ws, addr3=ssh
	tcpAddr := addrs["addr1"]
	wsAddr := addrs["addr2"]
	sshAddr := addrs["addr3"]

	if tcpAddr == "" || wsAddr == "" || sshAddr == "" {
		t.Fatalf("missing addrs: tcp=%q ws=%q ssh=%q (found %d total)", tcpAddr, wsAddr, sshAddr, found)
	}

	// Get server PID via the protocol.
	serverPID := getServerPID(t, socketPath)
	t.Logf("server PID: %d", serverPID)
	t.Logf("transports: unix=%s tcp=%s ws=%s ssh=%s", socketPath, tcpAddr, wsAddr, sshAddr)

	// Start 4 frontends, one per transport.
	type feEntry struct {
		name string
		pio  *ptyIO
		kill func()
	}
	var frontends []feEntry

	// Unix
	{
		fe := startFrontendWithEnv(t, socketPath, env)
		frontends = append(frontends, feEntry{"unix", fe.ptyIO, fe.Kill})
	}
	// TCP
	{
		fe := startFrontendWithEnv(t, "tcp://"+tcpAddr, env)
		frontends = append(frontends, feEntry{"tcp", fe.ptyIO, fe.Kill})
	}
	// WebSocket
	{
		fe := startFrontendWithEnv(t, "ws://"+wsAddr, env)
		frontends = append(frontends, feEntry{"ws", fe.ptyIO, fe.Kill})
	}
	// SSH
	{
		feCmd := exec.Command("termd-tui", "--socket", "ssh://"+sshAddr)
		feCmd.Env = append(env, "TERM=dumb")
		ptmx, err := pty.StartWithSize(feCmd, &pty.Winsize{Rows: 24, Cols: 80})
		if err != nil {
			t.Fatalf("start SSH frontend: %v", err)
		}
		pio := newPtyIO(ptmx, 80, 24)
		frontends = append(frontends, feEntry{"ssh", pio, func() {
			feCmd.Process.Kill(); feCmd.Wait(); ptmx.Close()
		}})
	}
	for _, fe := range frontends {
		defer fe.kill()
	}

	// Wait for all frontends to connect and show a prompt.
	for _, fe := range frontends {
		fe.pio.WaitFor(t, "bash", 10*time.Second)
		fe.pio.WaitFor(t, "termd$", 10*time.Second)
	}

	// Type a unique marker in the Unix frontend's shell.
	frontends[0].pio.Write([]byte("echo UPGRADE_MARKER_42\r"))
	frontends[0].pio.WaitFor(t, "UPGRADE_MARKER_42", 10*time.Second)

	// Trigger live upgrade.
	t.Log("sending SIGUSR2...")
	if err := syscall.Kill(serverPID, syscall.SIGUSR2); err != nil {
		t.Fatalf("kill -USR2: %v", err)
	}

	// All frontends should reconnect.
	for _, fe := range frontends {
		t.Logf("waiting for %s frontend to reconnect...", fe.name)
		fe.pio.WaitFor(t, "bash", 15*time.Second)
	}

	// Type in each frontend to verify the shells are alive.
	for i, fe := range frontends {
		marker := fmt.Sprintf("ALIVE_%s_%d", fe.name, i)
		fe.pio.Write([]byte("echo " + marker + "\r"))
		fe.pio.WaitFor(t, marker, 10*time.Second)
		t.Logf("%s frontend: shell alive", fe.name)
	}
}

// getServerPID connects to the server, reads the Identify message, and
// returns the server PID.
func getServerPID(t *testing.T, socketPath string) int {
	t.Helper()
	conn, err := transport.Dial(socketPath)
	if err != nil {
		t.Fatalf("connect for PID: %v", err)
	}
	defer conn.Close()

	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read identify: %v", err)
	}
	var ident struct {
		Pid int `json:"pid"`
	}
	if err := json.Unmarshal(buf[:n], &ident); err != nil {
		t.Fatalf("parse identify: %v", err)
	}
	if ident.Pid == 0 {
		t.Fatal("server PID is 0")
	}
	return ident.Pid
}

// startFrontendWithSpec starts a frontend connected to any transport spec.
func startFrontendWithSpec(t *testing.T, spec string, env []string) *frontend {
	t.Helper()
	cmd := exec.Command("termd-tui", "--socket", spec)
	cmd.Env = append(env, "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend %s: %v", spec, err)
	}
	return &frontend{
		ptyIO: newPtyIO(ptmx, 80, 24),
		cmd:   cmd,
		ptmx:  ptmx,
	}
}
