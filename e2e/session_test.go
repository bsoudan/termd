package e2e

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// startFrontendWithSession starts a termd-tui with the given --session flag.
func startFrontendWithSession(t *testing.T, socketPath, session string) *frontend {
	t.Helper()

	args := []string{"--socket", socketPath, "--command", "bash --norc"}
	if session != "" {
		args = append(args, "--session", session)
	}
	cmd := exec.Command("termd-tui", args...)
	cmd.Env = append(testEnv(t), "TERM=dumb")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend in pty: %v", err)
	}

	return &frontend{
		ptyIO: newPtyIO(ptmx, 80, 24),
		cmd:   cmd,
		ptmx:  ptmx,
	}
}

func TestSessionConnectDefault(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "")
	defer fe.Kill()

	fe.WaitFor(t, "bash", 10*time.Second)
	fe.WaitFor(t, "$", 10*time.Second)

	// Verify session was created
	out := runTermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "main") {
		t.Fatalf("session list missing 'main':\n%s", out)
	}

	// Verify client shows session
	out = runTermctl(t, socketPath, "client", "list")
	if !strings.Contains(out, "main") {
		t.Fatalf("client list missing session 'main':\n%s", out)
	}
}

func TestSessionConnectNamed(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "work")
	defer fe.Kill()

	fe.WaitFor(t, "bash", 10*time.Second)
	fe.WaitFor(t, "$", 10*time.Second)

	out := runTermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "work") {
		t.Fatalf("session list missing 'work':\n%s", out)
	}

	// Verify region belongs to "work" session
	out = runTermctl(t, socketPath, "region", "list", "--session", "work")
	if strings.Contains(out, "no regions") {
		t.Fatalf("expected regions in 'work' session, got:\n%s", out)
	}
	if !strings.Contains(out, "work") {
		t.Fatalf("region list missing session 'work':\n%s", out)
	}
}

func TestSessionMultiple(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe1 := startFrontendWithSession(t, socketPath, "")
	defer fe1.Kill()
	fe1.WaitFor(t, "bash", 10*time.Second)

	fe2 := startFrontendWithSession(t, socketPath, "dev")
	defer fe2.Kill()
	fe2.WaitFor(t, "bash", 10*time.Second)

	// Both sessions exist
	out := runTermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "main") {
		t.Fatalf("session list missing 'main':\n%s", out)
	}
	if !strings.Contains(out, "dev") {
		t.Fatalf("session list missing 'dev':\n%s", out)
	}

	// Filter by session
	outMain := runTermctl(t, socketPath, "region", "list", "--session", "main")
	outDev := runTermctl(t, socketPath, "region", "list", "--session", "dev")
	outAll := runTermctl(t, socketPath, "region", "list")

	// Each filtered list should have regions
	if strings.Contains(outMain, "no regions") {
		t.Fatalf("expected regions in 'main', got:\n%s", outMain)
	}
	if strings.Contains(outDev, "no regions") {
		t.Fatalf("expected regions in 'dev', got:\n%s", outDev)
	}

	// All list should have both
	mainRegionCount := strings.Count(outMain, "\n") - 1 // minus header
	devRegionCount := strings.Count(outDev, "\n") - 1
	allRegionCount := strings.Count(outAll, "\n") - 1
	if allRegionCount != mainRegionCount+devRegionCount {
		t.Fatalf("total regions (%d) != main (%d) + dev (%d)\nall:\n%s",
			allRegionCount, mainRegionCount, devRegionCount, outAll)
	}
}

func TestSessionReconnect(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "persist")
	defer fe.Kill()

	fe.WaitFor(t, "bash", 10*time.Second)
	fe.WaitFor(t, "$", 10*time.Second)

	// Get the region ID
	out := runTermctl(t, socketPath, "region", "list", "--session", "persist")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 1 region, got:\n%s", out)
	}
	regionID := strings.Fields(lines[1])[0]
	if len(regionID) != 36 {
		t.Fatalf("expected 36-char region ID, got %q", regionID)
	}

	// Kill the frontend
	fe.Kill()

	// Session and region should still exist
	out = runTermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "persist") {
		t.Fatalf("session 'persist' disappeared after frontend disconnect:\n%s", out)
	}

	// Reconnect with same session name — should resume
	fe2 := startFrontendWithSession(t, socketPath, "persist")
	defer fe2.Kill()

	fe2.WaitFor(t, "bash", 10*time.Second)

	// Verify no additional regions were spawned
	out = runTermctl(t, socketPath, "region", "list", "--session", "persist")
	regionLines := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, regionID) {
			regionLines++
		}
	}
	// Count total non-header lines
	totalLines := strings.Split(strings.TrimSpace(out), "\n")
	nonHeaderCount := len(totalLines) - 1
	if nonHeaderCount != 1 {
		t.Fatalf("expected 1 region after reconnect, got %d:\n%s", nonHeaderCount, out)
	}
}

func TestSessionCleanup(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Create a session by spawning a region directly
	shell := findShell(t)
	id := runTermctl(t, socketPath, "region", "spawn", "--session", "temp", "--", shell, "--norc")
	id = strings.TrimSpace(id)

	// Verify session exists
	out := runTermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "temp") {
		t.Fatalf("session 'temp' not created:\n%s", out)
	}

	// Kill the region
	runTermctl(t, socketPath, "region", "kill", id)

	// Wait for session to disappear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out = runTermctl(t, socketPath, "session", "list")
		if !strings.Contains(out, "temp") {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("session 'temp' still exists after killing its only region:\n%s", out)
}

func TestSessionSpawnIntoSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "")
	defer fe.Kill()
	fe.WaitFor(t, "bash", 10*time.Second)

	// Spawn another region into "main" session via termctl
	shell := findShell(t)
	id := runTermctl(t, socketPath, "region", "spawn", "--session", "main", "--", shell, "--norc")
	id = strings.TrimSpace(id)

	// Verify it's in "main"
	out := runTermctl(t, socketPath, "region", "list", "--session", "main")
	if !strings.Contains(out, id) {
		t.Fatalf("spawned region %s not in 'main' session:\n%s", id, out)
	}

	// Should be 2 regions now
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines)-1 != 2 {
		t.Fatalf("expected 2 regions in 'main', got %d:\n%s", len(lines)-1, out)
	}
}

func TestSessionClientListShowsSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "visible")
	defer fe.Kill()
	fe.WaitFor(t, "bash", 10*time.Second)

	out := runTermctl(t, socketPath, "client", "list")

	// Header should have SESSION
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 1 {
		t.Fatal("empty client list")
	}
	if !strings.Contains(lines[0], "SESSION") {
		t.Fatalf("client list header missing SESSION column:\n%s", out)
	}

	// Frontend client row should show "visible"
	found := false
	for _, line := range lines[1:] {
		if strings.Contains(line, "termd-tui") && strings.Contains(line, "visible") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("client list missing session 'visible' for termd-tui:\n%s", out)
	}
}
