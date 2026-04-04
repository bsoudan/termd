package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestRegionKilledExternally(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$",10*time.Second)

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
	pio.WaitFor(t, "no session", 10*time.Second)
}

func TestExit(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.WaitFor(t, "nxterm$",10*time.Second)
	pio.Write([]byte("exit\r"))

	// Frontend should enter the no-session screen instead of exiting.
	pio.WaitFor(t, "no session", 10*time.Second)
}
