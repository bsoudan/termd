package nxtest

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var termctlSplitRE = regexp.MustCompile(`\s{2,}`)

// SessionInfo is one row from `nxtermctl session list`.
type SessionInfo struct {
	Name        string
	RegionCount int
}

// RegionInfo is one row from `nxtermctl region list`.
type RegionInfo struct {
	ID      string
	Session string
	Name    string
	Cmd     string
	PID     int
}

// ClientInfo is one row from `nxtermctl client list`.
type ClientInfo struct {
	ID       uint32
	Hostname string
	Username string
	PID      int
	Process  string
	Session  string
	Region   string
}

// RunNxtermctl runs nxtermctl and returns stdout.
func RunNxtermctl(t *testing.T, socketPath string, env []string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"--socket", socketPath}, args...)
	cmd := exec.Command("nxtermctl", fullArgs...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nxtermctl %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

// SpawnRegion creates a region with the given program and returns its UUID.
func SpawnRegion(t *testing.T, socketPath string, env []string, programName string, extraArgs ...string) string {
	t.Helper()
	args := append([]string{"region", "spawn"}, extraArgs...)
	args = append(args, programName)
	id := strings.TrimSpace(RunNxtermctl(t, socketPath, env, args...))
	if len(id) != 36 {
		t.Fatalf("expected 36-char region ID, got %q", id)
	}
	return id
}

// ListSessions parses `nxtermctl session list`.
func ListSessions(t *testing.T, socketPath string, env []string) []SessionInfo {
	t.Helper()
	out := strings.TrimSpace(RunNxtermctl(t, socketPath, env, "session", "list"))
	if out == "" || out == "no sessions" {
		return nil
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 1 {
		return nil
	}
	var sessions []SessionInfo
	for _, line := range lines[1:] {
		cols := splitColumns(line)
		if len(cols) != 2 {
			t.Fatalf("unexpected session list row %q", line)
		}
		n, err := strconv.Atoi(cols[1])
		if err != nil {
			t.Fatalf("parse session region count from %q: %v", line, err)
		}
		sessions = append(sessions, SessionInfo{Name: cols[0], RegionCount: n})
	}
	return sessions
}

// ListRegions parses `nxtermctl region list`.
func ListRegions(t *testing.T, socketPath string, env []string, extraArgs ...string) []RegionInfo {
	t.Helper()
	args := append([]string{"region", "list"}, extraArgs...)
	out := strings.TrimSpace(RunNxtermctl(t, socketPath, env, args...))
	if out == "" || out == "no regions" {
		return nil
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 1 {
		return nil
	}
	var regions []RegionInfo
	for _, line := range lines[1:] {
		cols := splitColumns(line)
		if len(cols) != 5 {
			t.Fatalf("unexpected region list row %q", line)
		}
		pid, err := strconv.Atoi(cols[4])
		if err != nil {
			t.Fatalf("parse region pid from %q: %v", line, err)
		}
		regions = append(regions, RegionInfo{
			ID: cols[0], Session: cols[1], Name: cols[2], Cmd: cols[3], PID: pid,
		})
	}
	return regions
}

// ListClients parses `nxtermctl client list`.
func ListClients(t *testing.T, socketPath string, env []string) []ClientInfo {
	t.Helper()
	out := strings.TrimSpace(RunNxtermctl(t, socketPath, env, "client", "list"))
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 1 {
		return nil
	}
	var clients []ClientInfo
	for _, line := range lines[1:] {
		cols := splitColumns(line)
		if len(cols) != 7 {
			t.Fatalf("unexpected client list row %q", line)
		}
		id, err := strconv.ParseUint(cols[0], 10, 32)
		if err != nil {
			t.Fatalf("parse client id from %q: %v", line, err)
		}
		pid, err := strconv.Atoi(cols[3])
		if err != nil {
			t.Fatalf("parse client pid from %q: %v", line, err)
		}
		clients = append(clients, ClientInfo{
			ID:       uint32(id),
			Hostname: cols[1],
			Username: cols[2],
			PID:      pid,
			Process:  cols[4],
			Session:  cols[5],
			Region:   cols[6],
		})
	}
	return clients
}

func splitColumns(line string) []string {
	return termctlSplitRE.Split(strings.TrimSpace(line), -1)
}

// FindClient returns the first client for which match returns true.
func FindClient(clients []ClientInfo, match func(ClientInfo) bool) (ClientInfo, bool) {
	for _, cl := range clients {
		if match(cl) {
			return cl, true
		}
	}
	return ClientInfo{}, false
}

// FindRegion returns the first region for which match returns true.
func FindRegion(regions []RegionInfo, match func(RegionInfo) bool) (RegionInfo, bool) {
	for _, r := range regions {
		if match(r) {
			return r, true
		}
	}
	return RegionInfo{}, false
}

// FormatClientID is a small helper for CLI calls that still need a string ID.
func FormatClientID(id uint32) string {
	return fmt.Sprintf("%d", id)
}
