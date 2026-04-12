package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"nxtermd/internal/client"
	"nxtermd/internal/protocol"
	"nxtermd/internal/nxtest"
	"nxtermd/internal/transport"
)

// upgradeBinariesDir returns the path to the pre-built upgrade test binaries.
// The Makefile's build-upgrade-test-binaries target populates this directory.
func upgradeBinariesDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("UPGRADE_BINARIES_DIR")
	if dir == "" {
		t.Skip("UPGRADE_BINARIES_DIR not set; run 'make test-e2e' to include upgrade tests")
	}
	// Verify the expected binaries exist.
	serverBin := filepath.Join(dir, fmt.Sprintf("nxtermd-%s-%s", runtime.GOOS, runtime.GOARCH))
	clientBin := filepath.Join(dir, fmt.Sprintf("nxterm-%s-%s", runtime.GOOS, runtime.GOARCH))
	if _, err := os.Stat(serverBin); err != nil {
		t.Fatalf("server upgrade binary not found: %s", serverBin)
	}
	if _, err := os.Stat(clientBin); err != nil {
		t.Fatalf("client upgrade binary not found: %s", clientBin)
	}
	return dir
}

// upgradeServerConfig returns TOML config content with the given binaries dir.
func upgradeServerConfig(t *testing.T, binDir string) string {
	t.Helper()
	shell, _ := exec.LookPath("bash")
	if shell == "" {
		shell = "bash"
	}
	return fmt.Sprintf(
		"[[programs]]\nname = \"shell\"\ncmd = %q\nargs = [\"--norc\"]\n\n[sessions]\ndefault-programs = [\"shell\"]\n\n[upgrade]\nbinaries-dir = %q\n",
		shell, binDir,
	)
}

// startServerWithUpgradeDir starts a server with binaries-dir configured.
func startServerWithUpgradeDir(t *testing.T, binDir string) (socketPath string, env []string, cmd *exec.Cmd) {
	t.Helper()
	env = testEnv(t)
	writeTestServerConfigCustom(t, env, upgradeServerConfig(t, binDir))

	socketPath = filepath.Join(t.TempDir(), "nxtermd.sock")
	cmd = exec.Command("nxtermd", "unix:"+socketPath)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return
		}
		runtime.Gosched()
	}
	cmd.Process.Kill()
	t.Fatalf("server socket never appeared at %s", socketPath)
	return
}

// copyBinary copies a binary file preserving executable permission.
func copyBinary(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
	out.Close()
}

// dialClient connects to the server and returns a protocol client.
func dialClient(t *testing.T, socketPath string) *client.Client {
	t.Helper()
	conn, err := transport.Dial(socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := client.New(transport.WrapCompression(conn))
	c.SendIdentify("test")
	// Drain the server's identify message.
	select {
	case msg := <-c.Recv():
		if _, ok := msg.Payload.(protocol.Identify); !ok {
			t.Fatalf("expected identify, got %T", msg.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for identify")
	}
	return c
}

// recvPayload waits for a message of the given type.
func recvPayload[T any](t *testing.T, c *client.Client, timeout time.Duration) T {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-c.Recv():
			if !ok {
				t.Fatal("connection closed while waiting for message")
			}
			if p, ok := msg.Payload.(T); ok {
				return p
			}
			// skip other messages
		case <-deadline:
			var zero T
			t.Fatalf("timeout waiting for %T", zero)
			return zero
		}
	}
}

// TestUpgradeCheck verifies the upgrade check protocol: with upgrade
// binaries available, the server reports both server and client upgrades.
func TestUpgradeCheck(t *testing.T) {
	binDir := upgradeBinariesDir(t)
	socketPath, _, cmd := startServerWithUpgradeDir(t, binDir)
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	c := dialClient(t, socketPath)
	defer c.Close()

	if err := c.SendWithReqID(protocol.UpgradeCheckRequest{
		ClientVersion: "old-version",
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}, 1); err != nil {
		t.Fatalf("send upgrade check: %v", err)
	}

	resp := recvPayload[protocol.UpgradeCheckResponse](t, c, 10*time.Second)
	if resp.Error {
		t.Fatalf("upgrade check error: %s", resp.Message)
	}
	if !resp.ServerAvailable {
		t.Error("expected server upgrade available")
	}
	if resp.ServerVersion == "" {
		t.Error("expected non-empty server version")
	}
	if !resp.ClientAvailable {
		t.Error("expected client upgrade available")
	}
	if resp.ClientVersion == "" {
		t.Error("expected non-empty client version")
	}
	t.Logf("upgrade check: server=%s client=%s", resp.ServerVersion, resp.ClientVersion)
}

// TestUpgradeCheckNoDir verifies that when the configured binaries
// directory is empty, the server reports no upgrades available. We
// must set an explicit empty dir because resolveBinariesDir falls
// back to the running nxtermd's executable directory, which during
// `make test` happens to be .local/bin and contains the upgrade
// binaries.
func TestUpgradeCheckNoDir(t *testing.T) {
	emptyDir := t.TempDir()
	cfg := fmt.Sprintf("[upgrade]\nbinaries-dir = %q\n", emptyDir)
	socketPath, cleanup := startServerCustom(t, cfg)
	defer cleanup()

	c := dialClient(t, socketPath)
	defer c.Close()

	if err := c.SendWithReqID(protocol.UpgradeCheckRequest{
		ClientVersion: "old-version",
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}, 1); err != nil {
		t.Fatalf("send upgrade check: %v", err)
	}

	resp := recvPayload[protocol.UpgradeCheckResponse](t, c, 10*time.Second)
	if resp.Error {
		t.Fatalf("upgrade check error: %s", resp.Message)
	}
	if resp.ServerAvailable {
		t.Error("expected no server upgrade available")
	}
	if resp.ClientAvailable {
		t.Error("expected no client upgrade available")
	}
}

// TestClientBinaryDownload verifies that the server can stream a client
// binary in chunks and the SHA-256 hash matches.
func TestClientBinaryDownload(t *testing.T) {
	binDir := upgradeBinariesDir(t)
	socketPath, _, cmd := startServerWithUpgradeDir(t, binDir)
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	c := dialClient(t, socketPath)
	defer c.Close()

	if err := c.SendWithReqID(protocol.ClientBinaryRequest{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}, 1); err != nil {
		t.Fatalf("send client binary request: %v", err)
	}

	// Collect chunks.
	hasher := sha256.New()
	var totalBytes int64
	deadline := time.After(30 * time.Second)

	for {
		select {
		case msg, ok := <-c.Recv():
			if !ok {
				t.Fatal("connection closed during download")
			}
			switch p := msg.Payload.(type) {
			case protocol.ClientBinaryChunk:
				data, err := base64.StdEncoding.DecodeString(p.Data)
				if err != nil {
					t.Fatalf("decode chunk: %v", err)
				}
				hasher.Write(data)
				totalBytes += int64(len(data))
				if p.Final {
					t.Logf("received final chunk, total %d bytes", totalBytes)
				}
			case protocol.ClientBinaryResponse:
				if p.Error {
					t.Fatalf("download error: %s", p.Message)
				}
				gotHash := fmt.Sprintf("%x", hasher.Sum(nil))
				if gotHash != p.SHA256 {
					t.Fatalf("sha256 mismatch: got %s, want %s", gotHash, p.SHA256)
				}
				if totalBytes != p.Size {
					t.Fatalf("size mismatch: got %d, want %d", totalBytes, p.Size)
				}
				t.Logf("download verified: %d bytes, sha256=%s", p.Size, p.SHA256[:16])
				return
			}
		case <-deadline:
			t.Fatalf("timeout during download (got %d bytes so far)", totalBytes)
		}
	}
}

// TestTUIUpgradeE2E simulates a user upgrading both server and client
// from the TUI. It verifies the upgrade notification, the interactive
// upgrade dialog, status updates during the process, and that both
// binaries end up at the new version.
func TestTUIUpgradeE2E(t *testing.T) {
	binDir := upgradeBinariesDir(t)

	// Copy server and client binaries to a temp dir so the upgrade
	// replaces these copies, not the shared .local/bin/ originals.
	tmpBin := t.TempDir()
	serverBinSrc, err := exec.LookPath("nxtermd")
	if err != nil {
		t.Fatalf("lookup nxtermd: %v", err)
	}
	tuiBinSrc, err := exec.LookPath("nxterm")
	if err != nil {
		t.Fatalf("lookup nxterm: %v", err)
	}
	serverBin := filepath.Join(tmpBin, "nxtermd")
	tuiBin := filepath.Join(tmpBin, "nxterm")
	copyBinary(t, serverBinSrc, serverBin)
	copyBinary(t, tuiBinSrc, tuiBin)

	// Start server from the temp copy with upgrade dir configured.
	env := testEnv(t)
	writeTestServerConfigCustom(t, env, upgradeServerConfig(t, binDir))

	socketPath := filepath.Join(t.TempDir(), "nxtermd.sock")
	serverCmd := exec.Command(serverBin, "unix:"+socketPath)
	serverCmd.Env = env
	serverCmd.Stderr = os.Stderr
	serverCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	oldServerPID := serverCmd.Process.Pid
	// Kill the entire process group so the new server spawned by
	// HandleUpgrade is also cleaned up.
	defer func() {
		syscall.Kill(-serverCmd.Process.Pid, syscall.SIGKILL)
		serverCmd.Wait()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		runtime.Gosched()
	}

	// Start TUI frontend from the temp copy.
	feStderr, _ := os.Create("/tmp/tui-upgrade-stderr.log")
	defer feStderr.Close()
	t.Logf("TUI stderr log: %s", feStderr.Name())

	feCmd := exec.Command(tuiBin, "--socket", socketPath)
	feCmd.Env = append(env, "TERM=dumb")
	feCmd.Stderr = feStderr
	ptmx, err := pty.StartWithSize(feCmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend: %v", err)
	}
	nxt := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
	defer func() { feCmd.Process.Kill(); feCmd.Wait(); ptmx.Close() }()

	// ── Step 1: Wait for shell prompt ──────────────────────────────────
	nxt.WaitFor("nxterm$", 10*time.Second)
	t.Log("shell prompt visible")

	// ── Step 2: Verify upgrade notification in status bar ──────────────
	nxt.WaitFor("update available", 10*time.Second)
	t.Log("upgrade notification visible in status bar")

	// ── Step 3: Open upgrade dialog (ctrl+b u) ────────────────────────
	nxt.WaitForSilence(200 * time.Millisecond)
	nxt.Write([]byte{0x02, 'u'})

	nxt.WaitFor("Press enter to upgrade", 5*time.Second)
	t.Log("upgrade dialog visible")

	// Verify version info shown in dialog.
	nxt.WaitFor("upgrade-test-v2", 5*time.Second)

	// Verify status bar shows "upgrade ready".
	nxt.WaitFor("upgrade ready", 5*time.Second)
	t.Log("dialog shows version info and ready status")

	// ── Step 4: Press enter to start upgrade ──────────────────────────
	nxt.Write([]byte("\r"))

	// ── Step 5: Monitor server upgrade status ─────────────────────────
	nxt.WaitFor("upgrading server", 10*time.Second)
	t.Log("server upgrade in progress")

	// Wait for old server process to exit (it gets replaced by SIGUSR2).
	done := make(chan error, 1)
	go func() { done <- serverCmd.Wait() }()
	select {
	case <-done:
		t.Logf("old server PID %d exited", oldServerPID)
	case <-time.After(15 * time.Second):
		t.Fatal("old server did not exit within 15s")
	}

	// ── Step 6: Monitor client download status ────────────────────────
	nxt.WaitFor("downloading client", 30*time.Second)
	t.Log("client binary download in progress")

	// ── Step 7: Wait for new TUI to start after exec ──────────────────
	// The exec replaces the process; the new TUI starts fresh without
	// the UpgradeLayer dialog. Wait for the dialog to disappear AND
	// the prompt to appear — this prevents false matches against the
	// old prompt still visible behind the upgrade dialog.
	nxt.WaitForScreen(func(lines []string) bool {
		hasPrompt := false
		for _, line := range lines {
			if strings.Contains(line, "Upgrade") || strings.Contains(line, "Downloading") {
				return false // dialog still visible → old client
			}
			if strings.Contains(line, "nxterm$") {
				hasPrompt = true
			}
		}
		return hasPrompt
	}, "new client (prompt without upgrade dialog)", 30*time.Second)
	t.Log("new client connected with shell prompt")

	// ── Step 8: Verify both versions via the status pane ──────────────
	nxt.WaitForSilence(500 * time.Millisecond)
	nxt.Write([]byte{0x02, 's'})

	// The status pane shows both client and server versions.
	// Both should be upgrade-test-v2. We check the "Version:" label
	// lines to avoid matching the upgrade dialog text.
	nxt.WaitForScreen(func(lines []string) bool {
		count := 0
		for _, line := range lines {
			if strings.Contains(line, "Version:") && strings.Contains(line, "upgrade-test-v2") {
				count++
			}
		}
		return count >= 2 // one for client, one for server
	}, "status pane showing Version: upgrade-test-v2 for both client and server", 10*time.Second)
	t.Log("status pane confirms both versions are upgrade-test-v2")

	// Close status pane.
	nxt.Write([]byte("q"))
	nxt.WaitForSilence(200 * time.Millisecond)

	// ── Step 9: Verify shell is still alive ───────────────────────────
	nxt.Write([]byte("echo POST_UPGRADE_ALIVE\r"))
	nxt.WaitFor("POST_UPGRADE_ALIVE", 10*time.Second)
	t.Log("shell alive after upgrade")
}
