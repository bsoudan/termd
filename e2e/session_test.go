package e2e

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// startFrontendWithSession starts an nxterm with the given --session flag.
func startFrontendWithSession(t *testing.T, socketPath, session string) *frontend {
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

	fe.WaitFor(t, "$", 10*time.Second)

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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "work")
	defer fe.Kill()

	fe.WaitFor(t, "$", 10*time.Second)

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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe1 := startFrontendWithSession(t, socketPath, "")
	defer fe1.Kill()
	fe1.WaitFor(t, "$", 10*time.Second)

	fe2 := startFrontendWithSession(t, socketPath, "dev")
	defer fe2.Kill()
	fe2.WaitFor(t, "$", 10*time.Second)

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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "persist")
	defer fe.Kill()

	fe.WaitFor(t, "$", 10*time.Second)

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
	fe.Kill()

	// Session and region should still exist
	out = runNxtermctl(t, socketPath, "session", "list")
	if !strings.Contains(out, "persist") {
		t.Fatalf("session 'persist' disappeared after frontend disconnect:\n%s", out)
	}

	// Reconnect with same session name — should resume
	fe2 := startFrontendWithSession(t, socketPath, "persist")
	defer fe2.Kill()

	fe2.WaitFor(t, "$", 10*time.Second)

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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "")
	defer fe.Kill()
	fe.WaitFor(t, "$", 10*time.Second)

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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendWithSession(t, socketPath, "visible")
	defer fe.Kill()
	fe.WaitFor(t, "$", 10*time.Second)

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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio1, cleanup1 := startFrontend(t, socketPath)

	pio1.WaitFor(t, "nxterm$", 10*time.Second)

	// Output colored text before detaching
	pio1.Write([]byte("printf '" +
		shellSGR(ansi.AttrRedForegroundColor) + "COLOR_PERSIST" + shellResetStyle +
		`\n'` + "\r"))

	pio1.WaitForScreen(t, func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "COLOR_PERSIST") {
				return true
			}
		}
		return false
	}, "output line starting with 'COLOR_PERSIST'", 10*time.Second)
	pio1.WaitForSilence(200 * time.Millisecond)

	// Verify colors are present before detach
	cells1 := pio1.ScreenCells()
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
	pio1.Write([]byte{0x02, 'd'})

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			cleanup1()
			t.Fatal("timeout waiting for first frontend to exit")
		case _, ok := <-pio1.ch:
			if !ok {
				goto reconnect
			}
		}
	}

reconnect:
	cleanup1()

	// Reattach
	pio2, cleanup2 := startFrontend(t, socketPath)
	defer cleanup2()

	pio2.WaitFor(t, "COLOR_PERSIST", 10*time.Second)
	pio2.WaitForSilence(200 * time.Millisecond)

	// Verify colors survived reattach
	cells2 := pio2.ScreenCells()
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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Pre-create two regions via nxtermctl before the frontend connects.
	spawnRegion(t, socketPath, "shell")
	spawnRegion(t, socketPath, "shell")

	// Now start the frontend — it should enumerate both regions as tabs.
	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// First tab is active (label " 1 " — no colon), second is
	// inactive (label " 2:bash "). Either inactive label appearing
	// proves both tabs exist.
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "2:") || strings.Contains(lines[0], "1:")
	}, "tab bar with two pre-existing regions", 10*time.Second)
}

func TestReconnectRestoresTabs(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Spawn a second region. Tab 1 becomes inactive → "1:bash"
	// appears in the tab bar (the active tab renders as just " 2 ").
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Kill the client connection to force reconnect
	clientID := findFrontendClientID(t, socketPath)
	runNxtermctl(t, socketPath, "client", "kill", clientID)

	// Wait for reconnecting then reconnected
	pio.WaitFor(t, "reconnecting", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Both tabs should be restored after reconnect. Whichever tab
	// becomes active again, the other one's "<n>:bash" label should
	// be visible in the tab bar.
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") || strings.Contains(lines[0], "2:bash")
	}, "both tabs restored after reconnect", 10*time.Second)
}

func TestAllRegionsDestroyedShowsNoSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Exit the only shell.
	pio.Write([]byte("exit\r"))

	// Frontend should enter the no-session screen instead of exiting.
	pio.WaitFor(t, "no session", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Reconnect from the no-session screen using the connect overlay.
	connectViaUI(t, pio, socketPath)

	// Verify we're back in a live shell.
	pio.Write([]byte("echo ALIVE\r"))
	pio.WaitFor(t, "ALIVE", 10*time.Second)
}
