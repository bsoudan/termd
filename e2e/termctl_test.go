package e2e

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTermctlStatus(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	out := runNxtermctl(t, socketPath, "status")

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

	id := spawnRegion(t, socketPath, "shell")

	out := runNxtermctl(t, socketPath, "region", "list")
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

	id := spawnRegion(t, socketPath, "shell")

	// Send a command and wait a moment for bash to process it
	runNxtermctl(t, socketPath, "region", "send", "-e", id, `echo viewtest_marker\r`)
	// Poll region view until the marker appears
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runNxtermctl(t, socketPath, "region", "view", id)
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

	id := spawnRegion(t, socketPath, "shell")

	out := runNxtermctl(t, socketPath, "region", "kill", id)
	if !strings.Contains(out, "killed") {
		t.Fatalf("expected 'killed', got: %s", out)
	}

	// Give the server a moment to process the death
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out = runNxtermctl(t, socketPath, "region", "list")
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

	id := spawnRegion(t, socketPath, "shell")

	// -e interprets \n as newline (acts as Enter)
	runNxtermctl(t, socketPath, "region", "send", "-e", id, `echo sendtest_ok\r`)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runNxtermctl(t, socketPath, "region", "view", id)
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
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	out := runNxtermctl(t, socketPath, "client", "list")
	hasNxterm := false
	for _, line := range strings.Split(out, "\n") {
		for _, f := range strings.Fields(line) {
			if f == "nxterm" {
				hasNxterm = true
			}
		}
	}
	if !hasNxterm {
		t.Fatalf("client list missing 'nxterm':\n%s", out)
	}
	if !strings.Contains(out, "nxtermctl") {
		t.Fatalf("client list missing 'nxtermctl':\n%s", out)
	}
}

func TestTermctlClientKill(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Start a frontend
	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Find the frontend's client ID
	out := runNxtermctl(t, socketPath, "client", "list")
	var frontendClientID string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for _, f := range fields {
			if f == "nxterm" {
				frontendClientID = fields[0]
				break
			}
		}
		if frontendClientID != "" {
			break
		}
	}
	if frontendClientID == "" {
		t.Fatalf("could not find frontend client ID in:\n%s", out)
	}

	// Kill the frontend client
	out = runNxtermctl(t, socketPath, "client", "kill", frontendClientID)
	if !strings.Contains(out, "killed") {
		t.Fatalf("expected 'killed', got: %s", out)
	}

	// The killed client should be gone immediately on the next list.
	out = runNxtermctl(t, socketPath, "client", "list")
	for _, line := range strings.Split(out, "\n") {
		for _, f := range strings.Fields(line) {
			if f == "nxterm" {
				t.Fatalf("frontend client still listed after kill:\n%s", out)
			}
		}
	}
}
