package e2e

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"nxtermd/internal/nxtest"
)

// startFrontendWithSession starts an nxterm with the given --session flag.
func startFrontendWithSession(t *testing.T, socketPath, session string) *nxtest.T {
	t.Helper()

	args := []string{"--socket", socketPath}
	if session != "" {
		args = append(args, "--session", session)
	}
	cmd := exec.Command("nxterm", args...)
	cmd.Env = append(testEnv(t), "TERM=dumb")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend in pty: %v", err)
	}

	return nxtest.NewFromFrontend(t, &nxtest.Frontend{
		PtyIO: nxtest.NewPtyIO(ptmx, 80, 24),
		Cmd:   cmd,
		Ptmx:  ptmx,
	})
}

func TestSessionConnectDefault(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "")
	defer nxt.Kill()

	nxt.WaitFor("$", 10*time.Second)

	// Verify session was created
	out := runNxtermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "main") {
		t.Fatalf("session list missing 'main':\n%s", out)
	}

	// Verify client shows session
	out = runNxtermctl(t, socketPath, "client", "list")
	if !strings.Contains(out, "main") {
		t.Fatalf("client list missing session 'main':\n%s", out)
	}
}

func TestSessionConnectNamed(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "work")
	defer nxt.Kill()

	nxt.WaitFor("$", 10*time.Second)

	out := runNxtermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "work") {
		t.Fatalf("session list missing 'work':\n%s", out)
	}

	// Verify region belongs to "work" session
	out = runNxtermctl(t, socketPath, "region", "list", "--session", "work")
	if strings.Contains(out, "no regions") {
		t.Fatalf("expected regions in 'work' session, got:\n%s", out)
	}
	if !strings.Contains(out, "work") {
		t.Fatalf("region list missing session 'work':\n%s", out)
	}
}

func TestSessionMultiple(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt1 := startFrontendWithSession(t, socketPath, "")
	defer nxt1.Kill()
	nxt1.WaitFor("$", 10*time.Second)

	nxt2 := startFrontendWithSession(t, socketPath, "dev")
	defer nxt2.Kill()
	nxt2.WaitFor("$", 10*time.Second)

	// Both sessions exist
	out := runNxtermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "main") {
		t.Fatalf("session list missing 'main':\n%s", out)
	}
	if !strings.Contains(out, "dev") {
		t.Fatalf("session list missing 'dev':\n%s", out)
	}

	// Filter by session
	outMain := runNxtermctl(t, socketPath, "region", "list", "--session", "main")
	outDev := runNxtermctl(t, socketPath, "region", "list", "--session", "dev")
	outAll := runNxtermctl(t, socketPath, "region", "list")

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
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "persist")
	defer nxt.Kill()

	nxt.WaitFor("$", 10*time.Second)

	// Get the region ID
	out := runNxtermctl(t, socketPath, "region", "list", "--session", "persist")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 1 region, got:\n%s", out)
	}
	regionID := strings.Fields(lines[1])[0]
	if len(regionID) != 36 {
		t.Fatalf("expected 36-char region ID, got %q", regionID)
	}

	// Kill the frontend
	nxt.Kill()

	// Session and region should still exist
	out = runNxtermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "persist") {
		t.Fatalf("session 'persist' disappeared after frontend disconnect:\n%s", out)
	}

	// Reconnect with same session name — should resume
	nxt2 := startFrontendWithSession(t, socketPath, "persist")
	defer nxt2.Kill()

	nxt2.WaitFor("$", 10*time.Second)

	// Verify no additional regions were spawned
	out = runNxtermctl(t, socketPath, "region", "list", "--session", "persist")
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
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Create a session by spawning a region directly
	id := runNxtermctl(t, socketPath, "region", "spawn", "--session", "temp", "shell")
	id = strings.TrimSpace(id)

	// Verify session exists
	out := runNxtermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "temp") {
		t.Fatalf("session 'temp' not created:\n%s", out)
	}

	// Kill the region
	runNxtermctl(t, socketPath, "region", "kill", id)

	// Wait for session to disappear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out = runNxtermctl(t, socketPath, "session", "list")
		if !strings.Contains(out, "temp") {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("session 'temp' still exists after killing its only region:\n%s", out)
}

func TestSessionSpawnIntoSession(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "")
	defer nxt.Kill()
	nxt.WaitFor("$", 10*time.Second)

	// Spawn another region into "main" session via termctl
	id := runNxtermctl(t, socketPath, "region", "spawn", "--session", "main", "shell")
	id = strings.TrimSpace(id)

	// Verify it's in "main"
	out := runNxtermctl(t, socketPath, "region", "list", "--session", "main")
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
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "visible")
	defer nxt.Kill()
	nxt.WaitFor("$", 10*time.Second)

	out := runNxtermctl(t, socketPath, "client", "list")

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
		if strings.Contains(line, "nxterm") && strings.Contains(line, "visible") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("client list missing session 'visible' for nxterm:\n%s", out)
	}
}

func TestSessionPersistence(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt1 := startFrontend(t, socketPath)

	nxt1.WaitFor("nxterm$", 10*time.Second)

	// Output colored text before detaching
	nxt1.Write([]byte("printf '" +
		shellSGR(ansi.AttrRedForegroundColor) + "COLOR_PERSIST" + shellResetStyle +
		`\n'` + "\r"))

	nxt1.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "COLOR_PERSIST") {
				return true
			}
		}
		return false
	}, "output line starting with 'COLOR_PERSIST'", 10*time.Second)
	nxt1.WaitForSilence(200 * time.Millisecond)

	// Verify colors are present before detach
	cells1 := nxt1.ScreenCells()
	for row, line := range cells1 {
		if len(line) > 0 && line[0].Data == "C" &&
			len(line) > 12 && line[12].Data == "T" {
			fg := line[0].Attr.Fg
			t.Logf("before detach: 'COLOR_PERSIST' at row %d, fg=%d/%q", row, fg.Mode, fg.Name)
			if fg.Name != "red" {
				t.Fatalf("before detach: expected red fg, got %q", fg.Name)
			}
			break
		}
	}

	// Detach
	nxt1.Write([]byte{0x02, 'd'})

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			nxt1.Kill()
			t.Fatal("timeout waiting for first frontend to exit")
		case _, ok := <-nxt1.Ch():
			if !ok {
				goto reconnect
			}
		}
	}

reconnect:
	nxt1.Kill()

	// Reattach
	nxt2 := startFrontend(t, socketPath)
	defer nxt2.Kill()

	nxt2.WaitFor("COLOR_PERSIST", 10*time.Second)
	nxt2.WaitForSilence(200 * time.Millisecond)

	// Verify colors survived reattach
	cells2 := nxt2.ScreenCells()
	found := false
	for row, line := range cells2 {
		if len(line) > 0 && line[0].Data == "C" &&
			len(line) > 12 && line[12].Data == "T" {
			fg := line[0].Attr.Fg
			t.Logf("after reattach: 'COLOR_PERSIST' at row %d, fg=%d/%q", row, fg.Mode, fg.Name)
			if fg.Name != "red" {
				t.Errorf("after reattach: expected red fg, got mode=%d name=%q", fg.Mode, fg.Name)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("could not find 'COLOR_PERSIST' output line after reattach")
	}
}

func TestConnectPicksUpExistingRegions(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Pre-create two regions via nxtermctl before the frontend connects.
	spawnRegion(t, socketPath, "shell")
	spawnRegion(t, socketPath, "shell")

	// Now start the frontend — it should enumerate both regions as tabs.
	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	// First tab is active (label " 1 " — no colon), second is
	// inactive (label " 2:bash "). Either inactive label appearing
	// proves both tabs exist.
	nxt.WaitForScreen(func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "2:") || strings.Contains(lines[0], "1:")
	}, "tab bar with two pre-existing regions", 10*time.Second)
}

func TestReconnectRestoresTabs(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Spawn a second region. Tab 1 becomes inactive → "1:bash"
	// appears in the tab bar (the active tab renders as just " 2 ").
	nxt.Write([]byte("\x02c"))
	nxt.WaitFor("1:bash", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Kill the client connection to force reconnect
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	// Wait for reconnecting then reconnected
	nxt.WaitFor("reconnecting", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Both tabs should be restored after reconnect. Whichever tab
	// becomes active again, the other one's "<n>:bash" label should
	// be visible in the tab bar.
	nxt.WaitForScreen(func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") || strings.Contains(lines[0], "2:bash")
	}, "both tabs restored after reconnect", 10*time.Second)
}

func TestAllRegionsDestroyedShowsNoSession(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Exit the only shell.
	nxt.Write([]byte("exit\r"))

	// Frontend should enter the no-session screen instead of exiting.
	nxt.WaitFor("no session", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Reconnect from the no-session screen using the connect overlay.
	connectViaUI(t, nxt, socketPath)

	// Verify we're back in a live shell.
	nxt.Write([]byte("echo ALIVE\r"))
	nxt.WaitFor("ALIVE", 10*time.Second)
}
