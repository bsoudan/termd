package e2e

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"nxtermd/internal/nxtest"
)

// shellSGR converts ansi.SGR() output to shell printf \e notation.
func shellSGR(attrs ...ansi.Attr) string {
	return strings.ReplaceAll(ansi.SGR(attrs...), "\x1b", `\e`)
}

var shellResetStyle = strings.ReplaceAll(ansi.ResetStyle, "\x1b", `\e`)

func testEnv(t *testing.T) []string {
	t.Helper()
	return nxtest.TestEnv(t.TempDir())
}

// sharedServer returns the socket path of a lazily started server with
// default config. The first call spawns the server; subsequent calls
// reuse it. The server is stopped in TestMain after all tests complete.
//
// Tests that need custom server config (keybinds, programs, transports)
// should continue using startServer / startServerCustom.
var (
	sharedOnce   sync.Once
	sharedSocket string
	sharedStop   func()
	sharedErr    error
)

func sharedServer(t *testing.T) string {
	t.Helper()
	sharedOnce.Do(func() {
		tmpDir, err := os.MkdirTemp("", "e2e-shared-*")
		if err != nil {
			sharedErr = err
			return
		}
		env := nxtest.TestEnv(tmpDir)
		if err := nxtest.WriteServerConfig(env); err != nil {
			sharedErr = err
			return
		}
		srvDir, err := os.MkdirTemp("", "e2e-shared-srv-*")
		if err != nil {
			sharedErr = err
			return
		}
		srv, err := nxtest.StartServer(srvDir, env)
		if err != nil {
			sharedErr = err
			return
		}
		sharedSocket = srv.SocketPath
		sharedStop = func() {
			srv.Stop()
			os.RemoveAll(tmpDir)
			os.RemoveAll(srvDir)
		}
	})
	if sharedErr != nil {
		t.Fatalf("shared server failed to start: %v", sharedErr)
	}
	return sharedSocket
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedStop != nil {
		sharedStop()
	}
	os.Exit(code)
}

// uniqueSession returns a session name safe for use with the shared
// server. Each test gets its own session so they don't interfere.
var sessionCounter uint64
var sessionMu sync.Mutex

func uniqueSession() string {
	sessionMu.Lock()
	sessionCounter++
	n := sessionCounter
	sessionMu.Unlock()
	return fmt.Sprintf("s%d", n)
}

func startServer(t *testing.T) (string, func()) {
	socketPath, _, cleanup := startServerReturnEnv(t)
	return socketPath, cleanup
}

func startServerReturnEnv(t *testing.T) (string, []string, func()) {
	t.Helper()
	env := testEnv(t)
	if err := nxtest.WriteServerConfig(env); err != nil {
		t.Fatal(err)
	}
	srv, err := nxtest.StartServer(t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	return srv.SocketPath, env, srv.Stop
}

func startServerCustom(t *testing.T, configContent string) (string, func()) {
	t.Helper()
	env := testEnv(t)
	if err := nxtest.WriteServerConfigCustom(env, configContent); err != nil {
		t.Fatal(err)
	}
	srv, err := nxtest.StartServer(t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	return srv.SocketPath, srv.Stop
}

func writeTestServerConfig(t *testing.T, env []string) {
	t.Helper()
	if err := nxtest.WriteServerConfig(env); err != nil {
		t.Fatal(err)
	}
}

func writeTestServerConfigCustom(t *testing.T, env []string, content string) {
	t.Helper()
	if err := nxtest.WriteServerConfigCustom(env, content); err != nil {
		t.Fatal(err)
	}
}

func writeTestKeybindConfig(t *testing.T, env []string, content string) {
	t.Helper()
	if err := nxtest.WriteKeybindConfig(env, content); err != nil {
		t.Fatal(err)
	}
}

func startServerWithListeners(t *testing.T, extraListens ...string) (socketPath string, addrs map[string]string, cleanup func()) {
	t.Helper()
	env := testEnv(t)
	if err := nxtest.WriteServerConfig(env); err != nil {
		t.Fatal(err)
	}
	srv, addrs, err := nxtest.StartServerWithListeners(t.TempDir(), env, extraListens...)
	if err != nil {
		t.Fatal(err)
	}
	return srv.SocketPath, addrs, srv.Stop
}

func startServerWithTCP(t *testing.T) (socketPath, tcpAddr string, cleanup func()) {
	t.Helper()
	sock, addrs, cl := startServerWithListeners(t, "tcp://127.0.0.1:0")
	for _, a := range addrs {
		tcpAddr = a
	}
	if tcpAddr == "" {
		t.Fatal("could not find TCP listen address")
	}
	return sock, tcpAddr, cl
}

func startFrontend(t *testing.T, socketPath string) *nxtest.T {
	t.Helper()
	return startFrontendWithEnv(t, socketPath, testEnv(t))
}

// startFrontendForSession starts a frontend connected to socketPath and
// subscribed to sessionName. Used together with nxtest.DialDriver +
// SpawnNativeRegion so the frontend opens directly onto the
// driver-created native region, without spawning a shell.
func startFrontendForSession(t *testing.T, socketPath, sessionName string) *nxtest.T {
	t.Helper()
	return nxtest.MustStartFrontend(t, socketPath, testEnv(t), 80, 24, "--session", sessionName)
}

// startFrontendShared starts a frontend connected to the shared server
// using a unique session name so tests don't interfere with each other.
func startFrontendShared(t *testing.T) *nxtest.T {
	t.Helper()
	socketPath := sharedServer(t)
	return nxtest.MustStartFrontend(t, socketPath, testEnv(t), 80, 24, "--session", uniqueSession())
}

func startFrontendWithEnv(t *testing.T, socketPath string, env []string) *nxtest.T {
	t.Helper()
	return nxtest.MustStartFrontend(t, socketPath, env, 80, 24)
}

// runNxtermctl runs the nxtermctl binary with the given args and returns stdout.
func runNxtermctl(t *testing.T, socketPath string, args ...string) string {
	t.Helper()
	return nxtest.RunNxtermctl(t, socketPath, testEnv(t), args...)
}

// waitForRegionPrompt polls region view until a shell prompt ("$")
// appears, indicating bash has started.
func waitForRegionPrompt(t *testing.T, socketPath, regionID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runNxtermctl(t, socketPath, "region", "view", regionID)
		if strings.Contains(out, "$") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("region never showed shell prompt")
}

// regionSendAndWait waits for the shell prompt, sends input, and polls
// region view until marker appears.
func regionSendAndWait(t *testing.T, socketPath, regionID, input, marker string) {
	t.Helper()
	waitForRegionPrompt(t, socketPath, regionID)
	runNxtermctl(t, socketPath, "region", "send", "-e", regionID, input)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runNxtermctl(t, socketPath, "region", "view", regionID)
		if strings.Contains(out, marker) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("region view never showed %q", marker)
}

// spawnRegion uses nxtermctl to spawn a region using the named program
// and returns the region ID.
func spawnRegion(t *testing.T, socketPath string, programName string) string {
	t.Helper()
	return nxtest.SpawnRegion(t, socketPath, testEnv(t), programName)
}
