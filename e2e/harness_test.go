package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
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

	cmd := exec.Command("termd-frontend", "--socket", socketPath)
	// TERM=dumb prevents bubbletea's package init() from sending an OSC
	// terminal query that times out after 5 seconds in a raw PTY with no
	// terminal emulator behind it.
	cmd.Env = append(os.Environ(), "TERM=dumb")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend in pty: %v (is termd-frontend in PATH?)", err)
	}

	io := newPtyIO(ptmx)

	return io, func() {
		cmd.Process.Kill()
		cmd.Wait()
		ptmx.Close()
	}
}

// ptyIO reads from a PTY in a background goroutine and provides
// methods to wait for specific output and send input.
type ptyIO struct {
	ptmx *os.File
	ch   chan []byte
	buf  strings.Builder
}

func newPtyIO(ptmx *os.File) *ptyIO {
	p := &ptyIO{
		ptmx: ptmx,
		ch:   make(chan []byte, 256),
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
			p.ch <- data
		}
		if err != nil {
			close(p.ch)
			return
		}
	}
}

// WaitFor reads PTY output until needle appears or timeout elapses.
// Searches stripped (no ANSI escapes) accumulated output.
// Returns the stripped output on success.
func (p *ptyIO) WaitFor(t *testing.T, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		stripped := stripAnsi(p.buf.String())
		if strings.Contains(stripped, needle) {
			return stripped
		}
		select {
		case <-deadline:
			t.Fatalf("timeout (%v) waiting for %q in output:\n%s", timeout, needle, stripped)
			return ""
		case data, ok := <-p.ch:
			if !ok {
				t.Fatalf("PTY closed while waiting for %q\noutput:\n%s", needle, stripped)
				return ""
			}
			p.buf.Write(data)
		}
	}
}

// WaitForRaw is like WaitFor but searches the raw (non-stripped) output.
func (p *ptyIO) WaitForRaw(t *testing.T, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		raw := p.buf.String()
		if strings.Contains(raw, needle) {
			return raw
		}
		select {
		case <-deadline:
			t.Fatalf("timeout (%v) waiting for raw %q in output (len=%d)", timeout, needle, len(raw))
			return ""
		case data, ok := <-p.ch:
			if !ok {
				t.Fatalf("PTY closed while waiting for raw %q", needle)
				return ""
			}
			p.buf.Write(data)
		}
	}
}

// WaitForSilence drains output until no new data arrives for the given duration.
// This is not a fixed sleep — the timer resets on each new byte.
func (p *ptyIO) WaitForSilence(duration time.Duration) {
	for {
		select {
		case data, ok := <-p.ch:
			if !ok {
				return
			}
			p.buf.Write(data)
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
func spawnRegion(t *testing.T, socketPath string, shellCmd string) string {
	t.Helper()
	out := runTermctl(t, socketPath, "region", "spawn", shellCmd)
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

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI sequence: ESC [ ... final_byte
				i++
				for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
					i++ // parameter bytes and intermediate bytes
				}
				if i < len(s) {
					i++ // final byte
				}
			case ']': // OSC sequence: ESC ] ... BEL/ST
				i++
				for i < len(s) && s[i] != '\x07' {
					if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				if i < len(s) && s[i] == '\x07' {
					i++
				}
			case '(', ')': // Charset designation
				i++
				if i < len(s) {
					i++
				}
			default: // Two-character escape
				i++
			}
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}
