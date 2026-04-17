package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"nxtermd/internal/nxtest"
)

// startFrontendWithSession starts an nxterm with the given --session flag.
func startFrontendWithSession(t *testing.T, socketPath, session string) *nxtest.T {
	t.Helper()
	if session == "" {
		return nxtest.MustStartFrontend(t, socketPath, testEnv(t), 80, 24)
	}
	return nxtest.MustStartFrontend(t, socketPath, testEnv(t), 80, 24, "--session", session)
}

func TestSessionConnectDefault(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "")
	defer nxt.Kill()

	nxt.WaitFor("$", 10*time.Second)

	// Verify session was created
	sessions := nxtest.ListSessions(t, socketPath, testEnv(t))
	if _, ok := findSession(sessions, "main"); !ok {
		t.Fatalf("session list missing 'main':\n%v", sessions)
	}

	// Verify client shows session
	clients := nxtest.ListClients(t, socketPath, testEnv(t))
	if _, ok := nxtest.FindClient(clients, func(cl nxtest.ClientInfo) bool { return cl.Session == "main" }); !ok {
		t.Fatalf("client list missing session 'main':\n%v", clients)
	}
}

func TestSessionConnectNamed(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "work")
	defer nxt.Kill()

	nxt.WaitFor("$", 10*time.Second)

	sessions := nxtest.ListSessions(t, socketPath, testEnv(t))
	if _, ok := findSession(sessions, "work"); !ok {
		t.Fatalf("session list missing 'work':\n%v", sessions)
	}

	// Verify region belongs to "work" session
	regions := nxtest.ListRegions(t, socketPath, testEnv(t), "--session", "work")
	if len(regions) == 0 {
		t.Fatal("expected regions in 'work' session, got none")
	}
	if _, ok := nxtest.FindRegion(regions, func(r nxtest.RegionInfo) bool { return r.Session == "work" }); !ok {
		t.Fatalf("region list missing session 'work':\n%v", regions)
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
	sessions := nxtest.ListSessions(t, socketPath, testEnv(t))
	if _, ok := findSession(sessions, "main"); !ok {
		t.Fatalf("session list missing 'main':\n%v", sessions)
	}
	if _, ok := findSession(sessions, "dev"); !ok {
		t.Fatalf("session list missing 'dev':\n%v", sessions)
	}

	// Filter by session
	mainRegions := nxtest.ListRegions(t, socketPath, testEnv(t), "--session", "main")
	devRegions := nxtest.ListRegions(t, socketPath, testEnv(t), "--session", "dev")
	allRegions := nxtest.ListRegions(t, socketPath, testEnv(t))

	// Each filtered list should have regions
	if len(mainRegions) == 0 {
		t.Fatal("expected regions in 'main', got none")
	}
	if len(devRegions) == 0 {
		t.Fatal("expected regions in 'dev', got none")
	}

	// All list should have both
	if len(allRegions) != len(mainRegions)+len(devRegions) {
		t.Fatalf("total regions (%d) != main (%d) + dev (%d)\nall:\n%v",
			len(allRegions), len(mainRegions), len(devRegions), allRegions)
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
	regions := nxtest.ListRegions(t, socketPath, testEnv(t), "--session", "persist")
	if len(regions) == 0 {
		t.Fatal("expected at least 1 region in persist session")
	}
	regionID := regions[0].ID

	// Kill the frontend
	nxt.Kill()

	// Session and region should still exist
	sessions := nxtest.ListSessions(t, socketPath, testEnv(t))
	if _, ok := findSession(sessions, "persist"); !ok {
		t.Fatalf("session 'persist' disappeared after frontend disconnect:\n%v", sessions)
	}

	// Reconnect with same session name — should resume
	nxt2 := startFrontendWithSession(t, socketPath, "persist")
	defer nxt2.Kill()

	nxt2.WaitFor("$", 10*time.Second)

	// Verify no additional regions were spawned
	regions = nxtest.ListRegions(t, socketPath, testEnv(t), "--session", "persist")
	if len(regions) != 1 {
		t.Fatalf("expected 1 region after reconnect, got %d:\n%v", len(regions), regions)
	}
	if regions[0].ID != regionID {
		t.Fatalf("expected region %s after reconnect, got %v", regionID, regions)
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
	sessions := nxtest.ListSessions(t, socketPath, testEnv(t))
	if _, ok := findSession(sessions, "temp"); !ok {
		t.Fatalf("session 'temp' not created:\n%v", sessions)
	}

	// Kill the region
	runNxtermctl(t, socketPath, "region", "kill", id)

	// Wait for session to disappear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sessions = nxtest.ListSessions(t, socketPath, testEnv(t))
		if _, ok := findSession(sessions, "temp"); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session 'temp' still exists after killing its only region:\n%v", sessions)
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
	regions := nxtest.ListRegions(t, socketPath, testEnv(t), "--session", "main")
	if _, ok := nxtest.FindRegion(regions, func(r nxtest.RegionInfo) bool { return r.ID == id }); !ok {
		t.Fatalf("spawned region %s not in 'main' session:\n%v", id, regions)
	}

	// Should be 2 regions now
	if len(regions) != 2 {
		t.Fatalf("expected 2 regions in 'main', got %d:\n%v", len(regions), regions)
	}
}

func TestSessionClientListShowsSession(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontendWithSession(t, socketPath, "visible")
	defer nxt.Kill()
	nxt.WaitFor("$", 10*time.Second)

	clients := nxtest.ListClients(t, socketPath, testEnv(t))
	if _, ok := nxtest.FindClient(clients, func(cl nxtest.ClientInfo) bool {
		return cl.Process == "nxterm" && cl.Session == "visible"
	}); !ok {
		t.Fatalf("client list missing session 'visible' for nxterm:\n%v", clients)
	}
}

func findSession(sessions []nxtest.SessionInfo, name string) (nxtest.SessionInfo, bool) {
	for _, s := range sessions {
		if s.Name == name {
			return s, true
		}
	}
	return nxtest.SessionInfo{}, false
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
	nxt1.Sync("render settle")

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
	nxt2.Sync("render settle")

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

// TestDetachSeparateKeys verifies that detach works when ctrl+b and 'd'
// arrive as separate key events (which is normal with a real keyboard).
func TestDetachSeparateKeys(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Send ctrl+b and 'd' separately with a small delay between them.
	nxt.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	nxt.Write([]byte("d"))

	// The frontend should exit within a few seconds.
	if err := nxt.Wait(5 * time.Second); err != nil {
		nxt.Kill()
		t.Fatalf("frontend did not exit after detach: %v", err)
	}
}

// TestDetachCommandPalette verifies that detach works from the command
// palette (ctrl+b : detach).
func TestDetachCommandPalette(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Open command palette and type "detach".
	nxt.Write([]byte{0x02, ':'})
	nxt.WaitFor("detach", 5*time.Second)
	nxt.Write([]byte("detach\r"))

	if err := nxt.Wait(5 * time.Second); err != nil {
		nxt.Kill()
		t.Fatalf("frontend did not exit after detach via command palette: %v", err)
	}
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
	nxt.Sync("render settle")

	// Reconnect from the no-session screen using the connect overlay.
	connectViaUI(t, nxt, socketPath)

	// Verify we're back in a live shell.
	nxt.Write([]byte("echo ALIVE\r"))
	nxt.WaitFor("ALIVE", 10*time.Second)
}
