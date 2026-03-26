package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/rcarmo/go-te/pkg/te"
)

func startServer(t *testing.T) (string, func()) {
	t.Helper()

	socketPath := filepath.Join(t.TempDir(), "termd.sock")
	cmd := exec.Command("termd", "--socket", socketPath)
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

func startFrontend(t *testing.T, socketPath string) (*ptyIO, func()) {
	t.Helper()

	cmd := exec.Command("termd-frontend", "--socket", socketPath, "--command", "bash --norc")
	// TERM=dumb prevents bubbletea's package init() from sending an OSC
	// terminal query that times out after 5 seconds in a raw PTY with no
	// terminal emulator behind it.
	cmd.Env = append(os.Environ(), "TERM=dumb")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend in pty: %v (is termd-frontend in PATH?)", err)
	}

	io := newPtyIO(ptmx, 80, 24)

	return io, func() {
		cmd.Process.Kill()
		cmd.Wait()
		ptmx.Close()
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("termctl %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

// spawnRegion uses termctl to spawn a region and returns the region ID.
// Passes --norc so bash doesn't source .bashrc (which would override PS1).
func spawnRegion(t *testing.T, socketPath string, shellCmd string) string {
	t.Helper()
	out := runTermctl(t, socketPath, "region", "spawn", shellCmd, "--norc")
	id := strings.TrimSpace(out)
	if len(id) != 36 {
		t.Fatalf("expected 36-char region ID, got %q", id)
	}
	return id
}

// Write sends raw bytes to the PTY (simulating keyboard input).
func (p *ptyIO) Write(data []byte) {
	p.ptmx.Write(data)
}
