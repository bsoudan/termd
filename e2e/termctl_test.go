package e2e

import (
	"strings"
	"testing"
	"time"

	"nxtermd/internal/nxtest"
)

func TestTermctlStatus(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	id := spawnRegion(t, socketPath, "shell")

	regions := nxtest.ListRegions(t, socketPath, testEnv(t))
	if _, ok := nxtest.FindRegion(regions, func(r nxtest.RegionInfo) bool { return r.ID == id }); !ok {
		t.Fatalf("region list missing spawned region %s:\n%v", id, regions)
	}
	if _, ok := nxtest.FindRegion(regions, func(r nxtest.RegionInfo) bool { return r.Name == "bash" }); !ok {
		t.Fatalf("region list missing 'bash' name:\n%v", regions)
	}
}

func TestTermctlRegionView(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	id := spawnRegion(t, socketPath, "shell")

	// Wait for shell prompt before sending input — under parallel load
	// bash startup can be slow and input sent before readline is ready
	// gets lost.
	regionSendAndWait(t, socketPath, id, `echo viewtest_marker\r`, "viewtest_marker")
}

func TestTermctlRegionKill(t *testing.T) {
	t.Parallel()
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
		if len(nxtest.ListRegions(t, socketPath, testEnv(t))) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("region still listed after kill:\n%s", out)
}

func TestTermctlRegionSend(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	id := spawnRegion(t, socketPath, "shell")
	regionSendAndWait(t, socketPath, id, `echo sendtest_ok\r`, "sendtest_ok")
}

func TestTermctlClientList(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Start a frontend so there's a connected client to see
	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()
	nxt.WaitFor("nxterm$", 10*time.Second)

	clients := nxtest.ListClients(t, socketPath, testEnv(t))
	if _, ok := nxtest.FindClient(clients, func(cl nxtest.ClientInfo) bool { return cl.Process == "nxterm" }); !ok {
		t.Fatalf("client list missing 'nxterm':\n%v", clients)
	}
	out := runNxtermctl(t, socketPath, "client", "list")
	if !strings.Contains(out, "nxtermctl") {
		t.Fatalf("client list missing 'nxtermctl':\n%s", out)
	}
}

func TestTermctlClientKill(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Start a frontend
	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()
	nxt.WaitFor("nxterm$", 10*time.Second)

	// Find the frontend's client ID
	clients := nxtest.ListClients(t, socketPath, testEnv(t))
	frontendClient, ok := nxtest.FindClient(clients, func(cl nxtest.ClientInfo) bool { return cl.Process == "nxterm" })
	if !ok {
		t.Fatalf("could not find frontend client ID in:\n%v", clients)
	}

	// Kill the frontend client
	out := runNxtermctl(t, socketPath, "client", "kill", nxtest.FormatClientID(frontendClient.ID))
	if !strings.Contains(out, "killed") {
		t.Fatalf("expected 'killed', got: %s", out)
	}

	// The killed client should be gone immediately on the next list.
	clients = nxtest.ListClients(t, socketPath, testEnv(t))
	if _, ok := nxtest.FindClient(clients, func(cl nxtest.ClientInfo) bool { return cl.ID == frontendClient.ID }); ok {
		t.Fatalf("frontend client still listed after kill:\n%v", clients)
	}
}
