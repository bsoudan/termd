package e2e

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTermctlStatus(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	out := runTermctl(t, socketPath, "status")

	for _, want := range []string{"Hostname:", "Version:", "PID:", "Uptime:", "Listeners:", "Clients:", "Regions:"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, socketPath) {
		t.Errorf("status output missing socket path %q:\n%s", socketPath, out)
	}
}

func TestTermctlRegionSpawnAndList(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	shell := findShell(t)
	id := spawnRegion(t, socketPath, shell)

	out := runTermctl(t, socketPath, "region", "list")
	if !strings.Contains(out, id) {
		t.Fatalf("region list missing spawned region %s:\n%s", id, out)
	}
	if !strings.Contains(out, "bash") {
		t.Fatalf("region list missing 'bash' name:\n%s", out)
	}
}

func TestTermctlRegionView(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	shell := findShell(t)
	id := spawnRegion(t, socketPath, shell)

	// Send a command and wait a moment for bash to process it
	runTermctl(t, socketPath, "region", "send", "-e", id, `echo viewtest_marker\r`)
	// Poll region view until the marker appears
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runTermctl(t, socketPath, "region", "view", id)
		if strings.Contains(out, "viewtest_marker") {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("region view never showed 'viewtest_marker'")
}

func TestTermctlRegionKill(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	shell := findShell(t)
	id := spawnRegion(t, socketPath, shell)

	out := runTermctl(t, socketPath, "region", "kill", id)
	if !strings.Contains(out, "killed") {
		t.Fatalf("expected 'killed', got: %s", out)
	}

	// Give the server a moment to process the death
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out = runTermctl(t, socketPath, "region", "list")
		if strings.Contains(out, "no regions") {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("region still listed after kill:\n%s", out)
}

func TestTermctlRegionSend(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	shell := findShell(t)
	id := spawnRegion(t, socketPath, shell)

	// -e interprets \n as newline (acts as Enter)
	runTermctl(t, socketPath, "region", "send", "-e", id, `echo sendtest_ok\r`)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runTermctl(t, socketPath, "region", "view", id)
		if strings.Contains(out, "sendtest_ok") {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("region view never showed 'sendtest_ok'")
}

func TestTermctlClientList(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Start a frontend so there's a connected client to see
	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()
	pio.WaitFor(t, "bash", 10*time.Second)

	out := runTermctl(t, socketPath, "client", "list")
	if !strings.Contains(out, "termd-tui") {
		t.Fatalf("client list missing 'termd-tui':\n%s", out)
	}
	if !strings.Contains(out, "termctl") {
		t.Fatalf("client list missing 'termctl':\n%s", out)
	}
}

func TestTermctlClientKill(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Start a frontend
	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()
	pio.WaitFor(t, "bash", 10*time.Second)

	// Find the frontend's client ID
	out := runTermctl(t, socketPath, "client", "list")
	var frontendClientID string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "termd-tui") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				frontendClientID = fields[0]
				break
			}
		}
	}
	if frontendClientID == "" {
		t.Fatalf("could not find frontend client ID in:\n%s", out)
	}

	// Kill the frontend client
	out = runTermctl(t, socketPath, "client", "kill", frontendClientID)
	if !strings.Contains(out, "killed") {
		t.Fatalf("expected 'killed', got: %s", out)
	}

	// The killed client should be gone immediately on the next list.
	out = runTermctl(t, socketPath, "client", "list")
	if strings.Contains(out, "termd-tui") {
		t.Fatalf("frontend client still listed after kill:\n%s", out)
	}
}

func findShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash not found: %v", err)
	}
	return shell
}
