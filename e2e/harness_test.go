package e2e

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"termd/pkg/te"
)

// shellSGR converts ansi.SGR() output to shell printf \e notation.
// e.g., "\x1b[31m" → `\e[31m`
func shellSGR(attrs ...ansi.Attr) string {
	return strings.ReplaceAll(ansi.SGR(attrs...), "\x1b", `\e`)
}

// shellResetStyle is ansi.ResetStyle in shell \e notation.
var shellResetStyle = strings.ReplaceAll(ansi.ResetStyle, "\x1b", `\e`)

// testEnv returns os.Environ with XDG_CONFIG_HOME set to a temp dir,
// isolating tests from the user's local configuration files.
func testEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir())
}

func startServer(t *testing.T) (string, func()) {
	socketPath, _, cleanup := startServerReturnEnv(t)
	return socketPath, cleanup
}

func startServerReturnEnv(t *testing.T) (string, []string, func()) {
	t.Helper()

	env := testEnv(t)
	writeTestServerConfig(t, env)

	socketPath := filepath.Join(t.TempDir(), "termd.sock")
	cmd := exec.Command("termd", "unix:"+socketPath)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v (is termd in PATH?)", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		runtime.Gosched()
	}
	if _, err := os.Stat(socketPath); err != nil {
		cmd.Process.Kill()
		t.Fatalf("server socket never appeared at %s", socketPath)
	}

	return socketPath, env, func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
}

// writeTestServerConfig creates a server.toml in the XDG config dir that
// configures a "shell" program (bash --norc) as the default.
func writeTestServerConfig(t *testing.T, env []string) {
	t.Helper()
	xdg := xdgFromEnv(env)
	if xdg == "" {
		return
	}
	shell, _ := exec.LookPath("bash")
	if shell == "" {
		shell = "bash"
	}
	cfgDir := filepath.Join(xdg, "termd")
	os.MkdirAll(cfgDir, 0o755)
	cfgContent := fmt.Sprintf("[[programs]]\nname = \"shell\"\ncmd = %q\nargs = [\"--norc\"]\n\n[sessions]\ndefault-programs = [\"shell\"]\n", shell)
	os.WriteFile(filepath.Join(cfgDir, "server.toml"), []byte(cfgContent), 0o644)
}

// writeTestServerConfigCustom creates a server.toml with the given raw TOML content.
func writeTestServerConfigCustom(t *testing.T, env []string, content string) {
	t.Helper()
	xdg := xdgFromEnv(env)
	if xdg == "" {
		return
	}
	cfgDir := filepath.Join(xdg, "termd")
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(cfgDir, "server.toml"), []byte(content), 0o644)
}

// writeTestKeybindConfig creates a keybindings.toml in the XDG config dir.
func writeTestKeybindConfig(t *testing.T, env []string, content string) {
	t.Helper()
	xdg := xdgFromEnv(env)
	if xdg == "" {
		return
	}
	cfgDir := filepath.Join(xdg, "termd-tui")
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(cfgDir, "keybindings.toml"), []byte(content), 0o644)
}

func xdgFromEnv(env []string) string {
	for _, e := range env {
		if strings.HasPrefix(e, "XDG_CONFIG_HOME=") {
			return e[len("XDG_CONFIG_HOME="):]
		}
	}
	return ""
}

// startServerCustom starts a server with custom config content.
func startServerCustom(t *testing.T, configContent string) (string, func()) {
	t.Helper()

	env := testEnv(t)
	writeTestServerConfigCustom(t, env, configContent)

	socketPath := filepath.Join(t.TempDir(), "termd.sock")
	cmd := exec.Command("termd", "unix:"+socketPath)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v (is termd in PATH?)", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		runtime.Gosched()
	}
	if _, err := os.Stat(socketPath); err != nil {
		cmd.Process.Kill()
		t.Fatalf("server socket never appeared at %s", socketPath)
	}

	return socketPath, func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
}

// startServerWithListeners starts a server with the Unix socket plus extra --listen specs.
// It parses the assigned addresses from server stderr.
// Returns the socket path, a map of scheme→address for each extra listener, and a cleanup func.
func startServerWithListeners(t *testing.T, extraListens ...string) (socketPath string, addrs map[string]string, cleanup func()) {
	t.Helper()

	env := testEnv(t)
	writeTestServerConfig(t, env)

	socketPath = filepath.Join(t.TempDir(), "termd.sock")
	args := []string{"unix:" + socketPath}
	args = append(args, extraListens...)
	cmd := exec.Command("termd", args...)
	cmd.Env = env

	// Capture stderr to extract listen addresses
	stderrR, stderrW, _ := os.Pipe()
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	stderrW.Close()

	// Read stderr lines in a goroutine, send them to a channel.
	addrs = make(map[string]string)
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
		stderrR.Close()
	}()

	// Wait for N+1 "listening" lines (1 unix + N extra)
	need := len(extraListens) + 1
	deadline := time.Now().Add(5 * time.Second)
	for need > 0 && time.Now().Before(deadline) {
		select {
		case line, ok := <-lines:
			if !ok {
				break
			}
			if idx := strings.Index(line, "addr="); idx >= 0 {
				addr := line[idx+len("addr="):]
				if sp := strings.IndexByte(addr, ' '); sp >= 0 {
					addr = addr[:sp]
				}
				if strings.Contains(addr, ":") {
					addrs[addr] = addr
				}
				need--
			}
		case <-time.After(5 * time.Second):
			break
		}
	}

	// Wait for Unix socket
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		runtime.Gosched()
	}

	return socketPath, addrs, func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
}

// startServerWithTCP is a convenience wrapper for startServerWithListeners.
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

// frontend holds the state of a running termd-tui process in a PTY.
type frontend struct {
	*ptyIO
	cmd  *exec.Cmd
	ptmx *os.File
}

// Kill forcibly terminates the frontend process.
func (f *frontend) Kill() {
	f.cmd.Process.Kill()
	f.cmd.Wait()
	f.ptmx.Close()
}

// Wait waits for the frontend process to exit and returns any error.
// The caller should close the PTY first or ensure the process will exit.
func (f *frontend) Wait(timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- f.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		f.cmd.Process.Kill()
		return fmt.Errorf("frontend did not exit within %v", timeout)
	}
}

func startFrontend(t *testing.T, socketPath string) (*ptyIO, func()) {
	t.Helper()
	fe := startFrontendFull(t, socketPath)
	return fe.ptyIO, fe.Kill
}

func startFrontendFull(t *testing.T, socketPath string) *frontend {
	return startFrontendWithEnv(t, socketPath, testEnv(t))
}

func startFrontendWithEnv(t *testing.T, socketPath string, env []string) *frontend {
	t.Helper()

	cmd := exec.Command("termd-tui", "--socket", socketPath)
	// TERM=dumb prevents bubbletea's package init() from sending an OSC
	// terminal query that times out after 5 seconds in a raw PTY with no
	// terminal emulator behind it.
	cmd.Env = append(env, "TERM=dumb")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend in pty: %v (is termd-tui in PATH?)", err)
	}

	return &frontend{
		ptyIO: newPtyIO(ptmx, 80, 24),
		cmd:   cmd,
		ptmx:  ptmx,
	}
}

// ptyIO reads from a PTY in a background goroutine and provides
// methods to wait for specific output and send input. It maintains
// a go-te virtual screen that interprets ANSI escape sequences.
type ptyIO struct {
	ptmx   *os.File
	ch     chan []byte
	buf    strings.Builder
	screen *te.Screen
	stream *te.Stream
	mu     sync.Mutex
}

func newPtyIO(ptmx *os.File, cols, rows int) *ptyIO {
	screen := te.NewScreen(cols, rows)
	stream := te.NewStream(screen, false)
	p := &ptyIO{
		ptmx:   ptmx,
		ch:     make(chan []byte, 256),
		screen: screen,
		stream: stream,
	}
	go p.readLoop()
	return p
}

func (p *ptyIO) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			p.mu.Lock()
			p.stream.FeedBytes(data)
			p.buf.Write(data)
			p.mu.Unlock()

			p.ch <- data
		}
		if err != nil {
			close(p.ch)
			return
		}
	}
}

// ScreenLines returns the current screen content as a slice of strings.
func (p *ptyIO) ScreenLines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.screen.Display()
}

// ScreenLine returns a single row from the screen.
func (p *ptyIO) ScreenLine(row int) string {
	lines := p.ScreenLines()
	if row < 0 || row >= len(lines) {
		return ""
	}
	return lines[row]
}

// ScreenCells returns the full cell data including attributes and colors.
func (p *ptyIO) ScreenCells() [][]te.Cell {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.screen.LinesCells()
}

// WaitForScreen polls the virtual screen until check returns true or timeout.
func (p *ptyIO) WaitForScreen(t *testing.T, check func([]string) bool, desc string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		lines := p.ScreenLines()
		if check(lines) {
			return lines
		}
		select {
		case <-deadline:
			t.Fatalf("timeout (%v) waiting for %s\nscreen:\n%s", timeout, desc, strings.Join(lines, "\n"))
			return nil
		case _, ok := <-p.ch:
			if !ok {
				lines = p.ScreenLines()
				if check(lines) {
					return lines
				}
				t.Fatalf("PTY closed while waiting for %s\nscreen:\n%s", desc, strings.Join(lines, "\n"))
				return nil
			}
		}
	}
}

// FindOnScreen returns the row and column where needle first appears, or (-1,-1).
func findOnScreen(lines []string, needle string) (row, col int) {
	for i, line := range lines {
		if j := strings.Index(line, needle); j >= 0 {
			return i, j
		}
	}
	return -1, -1
}

// WaitFor reads PTY output until needle appears on the virtual screen.
func (p *ptyIO) WaitFor(t *testing.T, needle string, timeout time.Duration) []string {
	t.Helper()
	return p.WaitForScreen(t, func(lines []string) bool {
		for _, line := range lines {
			if strings.Contains(line, needle) {
				return true
			}
		}
		return false
	}, "screen to contain "+needle, timeout)
}

// WaitForSilence drains output until no new data arrives for the given duration.
// This is not a fixed sleep — the timer resets on each new byte.
func (p *ptyIO) WaitForSilence(duration time.Duration) {
	for {
		select {
		case _, ok := <-p.ch:
			if !ok {
				return
			}
			// Reset: new data arrived, wait again.
		case <-time.After(duration):
			return // no new data for the duration — idle.
		}
	}
}

// runTermctl runs the termctl binary with the given args and returns stdout.
func runTermctl(t *testing.T, socketPath string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"--socket", socketPath}, args...)
	cmd := exec.Command("termctl", fullArgs...)
	cmd.Env = testEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("termctl %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

// spawnRegion uses termctl to spawn a region using the named program
// and returns the region ID.
func spawnRegion(t *testing.T, socketPath string, programName string) string {
	t.Helper()
	out := runTermctl(t, socketPath, "region", "spawn", programName)
	id := strings.TrimSpace(out)
	if len(id) != 36 {
		t.Fatalf("expected 36-char region ID, got %q", id)
	}
	return id
}

// Resize changes the PTY window size and updates the go-te screen to match.
func (p *ptyIO) Resize(cols, rows uint16) {
	pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	p.mu.Lock()
	p.screen.Resize(int(rows), int(cols))
	p.mu.Unlock()
}

// Write sends raw bytes to the PTY (simulating keyboard input).
func (p *ptyIO) Write(data []byte) {
	p.ptmx.Write(data)
}
