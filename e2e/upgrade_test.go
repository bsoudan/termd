package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"nxtermd/internal/protocol"
	"nxtermd/internal/nxtest"
)

// TestLiveUpgrade starts a server with Unix, TCP, WebSocket, and SSH
// listeners. Connects a frontend to each. Triggers SIGUSR2 and verifies
// all four frontends reconnect and their shells are still alive.
func TestLiveUpgrade(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testEnv(t)
	writeTestServerConfig(t, env)

	socketPath := filepath.Join(dir, "nxtermd.sock")
	hostKeyPath := filepath.Join(dir, "host_key")

	// Start server with all transport types.
	cmd := exec.Command("nxtermd",
		"--ssh-host-key", hostKeyPath,
		"--ssh-no-auth",
		"unix:"+socketPath,
		"tcp://127.0.0.1:0",
		"ws://127.0.0.1:0",
		"dssh://127.0.0.1:0",
	)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderrR, stderrW, _ := os.Pipe()
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	stderrW.Close()
	// Kill the entire process group so the new server spawned by
	// HandleUpgrade is also cleaned up.
	defer func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }()

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

	serverPID := cmd.Process.Pid
	t.Logf("server PID: %d", serverPID)
	t.Logf("transports: unix=%s tcp=%s ws=%s ssh=%s", socketPath, tcpAddr, wsAddr, sshAddr)

	// Start 4 frontends, one per transport.
	type feEntry struct {
		name string
		nxt  *nxtest.T
		kill func()
	}
	var frontends []feEntry

	// Unix
	{
		nxt := startFrontendWithEnv(t, socketPath, env)
		frontends = append(frontends, feEntry{"unix", nxt, nxt.Kill})
	}
	// TCP
	{
		nxt := startFrontendWithEnv(t, "tcp://"+tcpAddr, env)
		frontends = append(frontends, feEntry{"tcp", nxt, nxt.Kill})
	}
	// WebSocket
	{
		nxt := startFrontendWithEnv(t, "ws://"+wsAddr, env)
		frontends = append(frontends, feEntry{"ws", nxt, nxt.Kill})
	}
	// SSH — clear SSH_AUTH_SOCK so the client doesn't try to contact
	// a real agent (which adds latency and is unnecessary with --ssh-no-auth).
	{
		feCmd := exec.Command("nxterm", "--socket", "dssh://"+sshAddr)
		feCmd.Env = append(env, "TERM=dumb", "SSH_AUTH_SOCK=")
		ptmx, err := pty.StartWithSize(feCmd, &pty.Winsize{Rows: 24, Cols: 80})
		if err != nil {
			t.Fatalf("start SSH frontend: %v", err)
		}
		nxt := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
		frontends = append(frontends, feEntry{"ssh", nxt, func() {
			feCmd.Process.Kill(); feCmd.Wait(); ptmx.Close()
		}})
	}
	for _, fe := range frontends {
		defer fe.kill()
	}

	// Wait for all frontends to see the prompt.
	for _, fe := range frontends {
		fe.nxt.WaitFor("nxterm$", 10*time.Second)
	}

	// Type a unique marker in the Unix frontend's shell.
	frontends[0].nxt.Write([]byte("echo UPGRADE_MARKER_42\r"))
	frontends[0].nxt.WaitFor("UPGRADE_MARKER_42", 10*time.Second)

	// Let all frontends settle before triggering upgrade.
	for _, fe := range frontends {
		fe.nxt.WaitForSilence(200 * time.Millisecond)
	}

	// Trigger live upgrade.
	t.Log("sending SIGUSR2...")
	if err := syscall.Kill(serverPID, syscall.SIGUSR2); err != nil {
		t.Fatalf("kill -USR2: %v", err)
	}

	// All frontends should reconnect.
	for _, fe := range frontends {
		t.Logf("waiting for %s frontend to reconnect...", fe.name)
		fe.nxt.WaitFor("$", 20*time.Second)
	}

	// Let frontends settle after reconnection.
	for _, fe := range frontends {
		fe.nxt.WaitForSilence(200 * time.Millisecond)
	}

	// Type in each frontend to verify the shells are alive.
	for i, fe := range frontends {
		marker := fmt.Sprintf("ALIVE_%s_%d", fe.name, i)
		fe.nxt.Write([]byte("echo " + marker + "\r"))
		fe.nxt.WaitFor(marker, 15*time.Second)
		t.Logf("%s frontend: shell alive", fe.name)
	}
}

// TestLiveUpgradeSimple starts a server with a single Unix socket, connects
// one frontend, triggers SIGUSR2, and verifies the old process is gone and
// the frontend reconnects with its shell intact.
func TestLiveUpgradeSimple(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testEnv(t)
	writeTestServerConfig(t, env)

	socketPath := filepath.Join(dir, "nxtermd.sock")

	cmd := exec.Command("nxtermd", "unix:"+socketPath)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	// Kill the entire process group so the new server spawned by
	// HandleUpgrade is also cleaned up.
	defer func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }()

	// Wait for socket.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Start a frontend and wait for the shell prompt.
	nxt := startFrontendWithEnv(t, socketPath, env)
	defer nxt.Kill()
	nxt.WaitFor("nxterm$", 10*time.Second)

	oldPID := cmd.Process.Pid
	t.Logf("old server PID: %d", oldPID)

	// Spawn extra regions so we have 3 tabs total. Type a unique marker
	// in each to verify all shells survive the upgrade.
	markers := []string{"TAB1_PRE", "TAB2_PRE", "TAB3_PRE"}
	nxt.Write([]byte("echo " + markers[0] + "\r"))
	nxt.WaitFor(markers[0], 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	for i := 1; i < 3; i++ {
		nxt.Write([]byte("\x02c")) // ctrl+b c = new tab
		// Wait for the inactive tab label to confirm the spawn took effect.
		nxt.WaitFor(fmt.Sprintf("%d:bash", i), 10*time.Second)
		nxt.WaitFor("nxterm$", 10*time.Second)
		nxt.WaitForSilence(200 * time.Millisecond)
		nxt.Write([]byte("echo " + markers[i] + "\r"))
		nxt.WaitFor(markers[i], 10*time.Second)
		nxt.WaitForSilence(200 * time.Millisecond)
	}

	// Record which tab labels are visible before upgrade.
	screen := nxt.ScreenLines()
	t.Logf("tabs before upgrade: %s", strings.TrimSpace(screen[0]))

	// Trigger live upgrade.
	t.Log("sending SIGUSR2...")
	if err := syscall.Kill(oldPID, syscall.SIGUSR2); err != nil {
		t.Fatalf("kill -USR2: %v", err)
	}

	// Frontend should reconnect.
	nxt.WaitFor("$", 20*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Wait for the old server process to exit. Because we started it with
	// cmd.Start(), we must call cmd.Wait() to reap it — otherwise it stays
	// as a zombie and kill(pid, 0) still succeeds.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("old server process (PID %d) did not exit within 5s", oldPID)
	}
	t.Logf("old server PID %d has exited", oldPID)

	// Verify new server has a different PID via the status pane.
	newPID := getStatusPID(t, nxt)
	if newPID == oldPID {
		t.Fatalf("new server PID %d matches old PID", newPID)
	}
	t.Logf("new server PID: %d", newPID)

	// Verify all 3 tabs survived the upgrade.
	screen = nxt.ScreenLines()
	tabBar := screen[0]
	t.Logf("tabs after upgrade: %s", strings.TrimSpace(tabBar))

	// All 3 tabs should be present. The active tab renders as " N ",
	// inactive tabs as " N:bash ". Check for tab numbers 1, 2, 3.
	for _, n := range []string{"1", "2", "3"} {
		if !strings.Contains(tabBar, n) {
			t.Errorf("tab %s missing from tab bar after upgrade: %q", n, tabBar)
		}
	}

	// Switch to each tab and verify the shell is alive.
	for i, marker := range markers {
		tabNum := fmt.Sprintf("%d", i+1)
		nxt.Write([]byte("\x02" + tabNum)) // ctrl+b N = switch to tab N
		time.Sleep(200 * time.Millisecond)
		postMarker := fmt.Sprintf("TAB%d_POST", i+1)
		nxt.Write([]byte("echo " + postMarker + "\r"))
		nxt.WaitFor(postMarker, 15*time.Second)
		t.Logf("tab %d: shell alive (had %s, echoed %s)", i+1, marker, postMarker)
	}
}

// TestLiveUpgradeStatusBroadcast verifies that the server publishes
// upgrade phases on the tree's UpgradeNode at each step of a live
// upgrade, ending with Phase=shutting_down followed by the connection
// closing. This is the raw-wire test for the phase-tracking that the
// upgrade dialog relies on.
func TestLiveUpgradeStatusBroadcast(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testEnv(t)
	writeTestServerConfig(t, env)

	socketPath := filepath.Join(dir, "nxtermd.sock")
	cmd := exec.Command("nxtermd", "unix:"+socketPath)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c := dialClient(t, socketPath)
	defer c.Close()

	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGUSR2); err != nil {
		t.Fatalf("kill -USR2: %v", err)
	}

	// Collect upgrade phases from tree events until the server closes
	// the connection. Each phase is set on /upgrade in a tree_events
	// message.
	var phases []string
	timeout := time.After(10 * time.Second)
loop:
	for {
		select {
		case msg, ok := <-c.Recv():
			if !ok {
				break loop
			}
			events, ok := msg.Payload.(protocol.TreeEvents)
			if !ok {
				continue
			}
			for _, op := range events.Ops {
				if op.Path != "/upgrade" || op.Op != "set" {
					continue
				}
				var node protocol.UpgradeNode
				if json.Unmarshal(op.Value, &node) != nil {
					continue
				}
				phases = append(phases, node.Phase)
				t.Logf("status: %s — %s", node.Phase, node.Message)
			}
		case <-timeout:
			t.Fatalf("timeout waiting for status messages; got phases: %v", phases)
		}
	}

	want := []string{
		protocol.UpgradePhaseStarting,
		protocol.UpgradePhaseSpawned,
		protocol.UpgradePhaseSentListenerFDs,
		protocol.UpgradePhaseStoppedAccepting,
		protocol.UpgradePhaseDrained,
		protocol.UpgradePhaseStoppedReadLoops,
		protocol.UpgradePhaseSentState,
		protocol.UpgradePhaseSentPTYFDs,
		protocol.UpgradePhaseReady,
		protocol.UpgradePhaseShuttingDown,
	}
	if !slices.Equal(phases, want) {
		t.Fatalf("phase sequence mismatch:\n  got:  %v\n  want: %v", phases, want)
	}
}

// TestLiveUpgradeNoDataLoss verifies that PTY output flowing during a
// live upgrade is not lost. It runs a shell loop that prints an
// incrementing counter as fast as possible, triggers SIGUSR2 mid-stream,
// stops the loop, then walks the on-screen output and asserts the
// counter values are strictly contiguous (no gaps from a stale snapshot
// missing bytes that the old readLoop drained after the snapshot).
func TestLiveUpgradeNoDataLoss(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testEnv(t)
	writeTestServerConfig(t, env)

	socketPath := filepath.Join(dir, "nxtermd.sock")

	cmd := exec.Command("nxtermd", "unix:"+socketPath)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	nxt := startFrontendWithEnv(t, socketPath, env)
	defer nxt.Kill()
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Start a high-rate awk loop that continuously emits "i=NNN i=NNN
	// i=NNN..." tokens. The token-per-iteration rate is high enough
	// that the kernel PTY buffer is always full and the readLoop is
	// actively draining bytes throughout the entire upgrade window.
	// Without the StopReadLoop fix, the OLD readLoop drains bytes
	// after the snapshot was taken; those bytes never make it to the
	// new process and show up as a gap in the counter sequence.
	nxt.Write([]byte(`awk 'BEGIN { for (i=1; i<=20000; i++) printf "i=%d ", i; printf "\nDONE\n" }'` + "\r"))

	// Wait until output is actively flowing. "i=" matches any counter
	// token, so we don't race the loop past a specific value — the loop
	// can run extremely fast and "i=10" could scroll off the visible
	// screen before the test thread catches it.
	nxt.WaitFor("i=", 10*time.Second)

	// Trigger live upgrade mid-stream.
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGUSR2); err != nil {
		t.Fatalf("kill -USR2: %v", err)
	}

	// Wait for the loop to finish.
	nxt.WaitFor("DONE", 60*time.Second)
	nxt.WaitFor("nxterm$", 30*time.Second)
	nxt.WaitForSilence(500 * time.Millisecond)

	// Walk the visible screen and pull out every "i=NNN" token, then
	// check the sequence is contiguous. Concatenate all rows into one
	// string first, then strip the line-wrap whitespace, so tokens
	// that wrap across rows (e.g. "i=198" + "00") survive intact.
	screenLines := nxt.ScreenLines()
	var joined strings.Builder
	for i := 1; i < len(screenLines); i++ { // skip tab bar at row 0
		joined.WriteString(strings.TrimRight(screenLines[i], " "))
	}
	flat := joined.String()

	var seen []int
	for {
		idx := strings.Index(flat, "i=")
		if idx < 0 {
			break
		}
		flat = flat[idx+2:]
		j := 0
		for j < len(flat) && flat[j] >= '0' && flat[j] <= '9' {
			j++
		}
		if j == 0 {
			continue
		}
		n, err := strconv.Atoi(flat[:j])
		if err == nil {
			seen = append(seen, n)
		}
		flat = flat[j:]
	}

	if len(seen) < 5 {
		t.Fatalf("not enough counter values on screen: %v\nlines:\n%s",
			seen, strings.Join(screenLines, "\n"))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] != seen[i-1]+1 {
			t.Fatalf("counter discontinuity at index %d: %d → %d\nfull sequence: %v\nlines:\n%s",
				i, seen[i-1], seen[i], seen, strings.Join(screenLines, "\n"))
		}
	}
	t.Logf("counter contiguous over %d values: %d..%d", len(seen), seen[0], seen[len(seen)-1])
}

// getStatusPID opens the TUI status pane (ctrl+b s), extracts the server
// PID from the "PID:" line, closes the pane (q), and returns the PID.
func getStatusPID(t *testing.T, nxt *nxtest.T) int {
	t.Helper()
	// Let the TUI settle before sending keys.
	nxt.WaitForSilence(200 * time.Millisecond)
	// Open status pane: ctrl+b s
	nxt.Write([]byte{0x02, 's'})

	// Wait for the PID line to appear on screen. The line looks like
	// "│   PID:       12345                           │" with border chars.
	var pid int
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if idx := strings.Index(line, "PID:"); idx >= 0 {
				after := line[idx+len("PID:"):]
				// Extract digits only.
				for _, f := range strings.Fields(after) {
					if n, err := strconv.Atoi(f); err == nil && n > 0 {
						pid = n
						return true
					}
				}
			}
		}
		return false
	}, "status pane with PID", 10*time.Second)

	// Close the status pane.
	nxt.Write([]byte("q"))
	nxt.WaitForSilence(200 * time.Millisecond)
	return pid
}

// startFrontendWithSpec starts a frontend connected to any transport spec.
func startFrontendWithSpec(t *testing.T, spec string, env []string) *nxtest.T {
	t.Helper()
	cmd := exec.Command("nxterm", "--socket", spec)
	cmd.Env = append(env, "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend %s: %v", spec, err)
	}
	return nxtest.NewFromFrontend(t, &nxtest.Frontend{
		PtyIO: nxtest.NewPtyIO(ptmx, 80, 24),
		Cmd:   cmd,
		Ptmx:  ptmx,
	})
}
