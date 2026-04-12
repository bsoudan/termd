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

	"nxtermd/internal/nxtest"
)

func TestTCPTransport(t *testing.T) {
	t.Parallel()
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Spawn a region via the Unix socket (termctl)
	_ = runNxtermctl(t, socketPath, "region", "spawn", "shell")

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

	// Verify the tab bar shows the TCP endpoint
	lines := nxt.ScreenLines()
	row0 := lines[0]
	if !strings.Contains(row0, tcpAddr) {
		t.Errorf("tab bar should show TCP addr %q, got: %q", tcpAddr, row0)
	}

	// Type a command and verify round-trip works
	nxt.Write([]byte("echo tcp_works\r"))
	nxt.WaitFor("tcp_works", 10*time.Second)
}

func TestWebSocketTransport(t *testing.T) {
	t.Parallel()
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
	cmd := exec.Command("nxterm", "--socket", "ws://"+wsAddr)
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via WS: %v", err)
	}
	nxt := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte("echo ws_works\r"))
	nxt.WaitFor("ws_works", 10*time.Second)
}

func TestSSHTransport(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hostKeyPath := filepath.Join(dir, "host_key")

	env := testEnv(t)
	writeTestServerConfig(t, env)

	// Start server with Unix + SSH (no auth keys = accept all for test)
	socketPath := filepath.Join(dir, "nxtermd.sock")
	cmd := exec.Command("nxtermd",
		"--ssh-host-key", hostKeyPath,
		"--ssh-no-auth",
		"unix:"+socketPath, "dssh://127.0.0.1:0")
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
	feCmd := exec.Command("nxterm", "--socket", "dssh://"+sshAddr)
	feCmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(feCmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via SSH: %v", err)
	}
	nxt := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
	defer func() { feCmd.Process.Kill(); feCmd.Wait(); ptmx.Close() }()

	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte("echo ssh_works\r"))
	nxt.WaitFor("ssh_works", 10*time.Second)
}

// TestSSHExecTransport exercises the ssh:// transport (system ssh
// binary spawned in a PTY → nxtermctl proxy on the remote). Real ssh
// is replaced with a fake-ssh wrapper script that strips the ssh
// argv prefix and exec's `nxtermctl proxy` directly, so the test
// covers everything except the actual SSH protocol exchange.
func TestSSHExecTransport(t *testing.T) {
	t.Parallel()
	socketPath, _, serverCleanup := startServerWithListeners(t)
	defer serverCleanup()

	dir := t.TempDir()

	// Spawn a region via the unix socket so the frontend has
	// something to subscribe to.
	_ = runNxtermctl(t, socketPath, "region", "spawn", "shell")

	// Build a fake-ssh wrapper. The transport invokes
	//   ssh -T <host> -- 'bash -ic <quoted-command>'
	// The entire bash -ic invocation is a single argument. Strip
	// the 3 leading args (-T host --) and eval the rest.
	fakeSSH := filepath.Join(dir, "ssh")
	wrapper := `#!/bin/sh
# args: -T host -- 'bash -ic ...'
shift 3
eval "$1"
`
	if err := os.WriteFile(fakeSSH, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	// Prepend the fake-ssh dir to PATH so transport.Dial("ssh://...")
	// picks up the wrapper instead of the system ssh (which probably
	// isn't even installed in the test environment).
	env := append(testEnv(t), "TERM=dumb")
	envWithFakeSSH := make([]string, 0, len(env))
	added := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			envWithFakeSSH = append(envWithFakeSSH, "PATH="+dir+":"+strings.TrimPrefix(kv, "PATH="))
			added = true
		} else {
			envWithFakeSSH = append(envWithFakeSSH, kv)
		}
	}
	if !added {
		envWithFakeSSH = append(envWithFakeSSH, "PATH="+dir+":"+os.Getenv("PATH"))
	}

	// Connect via ssh:// — the host portion is irrelevant since the
	// fake wrapper ignores it; the path portion is the explicit
	// remote socket the proxy will dial.
	cmd := exec.Command("nxterm", "--socket", "ssh://anyhost"+socketPath)
	cmd.Env = envWithFakeSSH

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via ssh-exec: %v", err)
	}
	nxt := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	// The active tab no longer renders the program name (commit
	// 98da964) so the historical WaitFor("bash") here can't be used;
	// wait for the bash prompt directly.
	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte("echo ssh_exec_works\r"))
	nxt.WaitFor("ssh_exec_works", 10*time.Second)
}

func TestMultiTransportSharedRegion(t *testing.T) {
	t.Parallel()
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Start frontend 1 via Unix socket
	nxt1 := startFrontend(t, socketPath)
	defer nxt1.Kill()

	nxt1.WaitFor("nxterm$", 10*time.Second)

	// Type a marker in frontend 1
	nxt1.Write([]byte("echo multi_transport_marker\r"))
	nxt1.WaitFor("multi_transport_marker", 10*time.Second)

	// Start frontend 2 via TCP (subscribes to the same region)
	cmd := exec.Command("nxterm", "--socket", "tcp:"+tcpAddr)
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend 2 via TCP: %v", err)
	}
	nxt2 := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	// Frontend 2 should see the marker (it gets the screen snapshot on subscribe)
	nxt2.WaitFor("multi_transport_marker", 10*time.Second)

	// Type in frontend 2, verify frontend 1 sees it
	nxt2.WaitFor("nxterm$", 10*time.Second)
	nxt2.Write([]byte("echo from_tcp_client\r"))
	nxt1.WaitFor("from_tcp_client", 10*time.Second)
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
