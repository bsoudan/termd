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

const (
	serverBin   = "../server/zig-out/bin/termd"
	frontendBin = "../frontend/termd-frontend"
)

// startServer starts the termd server on a temp socket.
// Returns the socket path and a cleanup function.
func startServer(t *testing.T) (string, func()) {
	t.Helper()

	bin, err := filepath.Abs(serverBin)
	if err != nil {
		t.Fatalf("resolve server binary: %v", err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("server binary not found at %s (run make build-server)", bin)
	}

	socketPath := filepath.Join(t.TempDir(), "termd.sock")
	cmd := exec.Command(bin, socketPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	// Wait for socket to appear (no sleep — tight poll with yield)
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

// startFrontend starts the termd frontend in a PTY connected to the given server socket.
// Returns a ptyIO for observing output and writing input, plus a cleanup function.
func startFrontend(t *testing.T, socketPath string) (*ptyIO, func()) {
	t.Helper()

	bin, err := filepath.Abs(frontendBin)
	if err != nil {
		t.Fatalf("resolve frontend binary: %v", err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("frontend binary not found at %s (run make build-frontend)", bin)
	}

	cmd := exec.Command(bin, socketPath)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend in pty: %v", err)
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
