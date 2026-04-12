package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestRegionKilledExternally(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Get the region ID
	out := runNxtermctl(t, socketPath, "region", "list")
	var regionID string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && len(fields[0]) == 36 {
			regionID = fields[0]
			break
		}
	}
	if regionID == "" {
		t.Fatal("could not find region ID")
	}

	// Kill the region externally
	runNxtermctl(t, socketPath, "region", "kill", regionID)

	// Frontend should enter the no-session screen instead of exiting.
	nxt.WaitFor("no session", 10*time.Second)
}

func TestExit(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Write([]byte("exit\r"))

	// Frontend should enter the no-session screen instead of exiting.
	nxt.WaitFor("no session", 10*time.Second)
}
