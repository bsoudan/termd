package nxtest

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"nxtermd/pkg/te"
)

// PtyIO reads from a PTY in a background goroutine and provides methods to
// wait for specific output and send input. It maintains a go-te virtual
// screen that interprets ANSI escape sequences.
type PtyIO struct {
	ptmx   *os.File
	ch     chan []byte
	screen *te.Screen
	stream *te.Stream
	mu     sync.Mutex

	// ackMu protects ack state for sync markers (OSC 2459;nx;ack;<id>).
	ackMu      sync.Mutex
	ackSeen    map[string]bool
	ackWaiters map[string]chan struct{}
	ackPending []byte // carry-over for OSCs split across reads
}

// NewPtyIO creates a PtyIO that reads from ptmx and maintains a virtual
// screen of the given dimensions.
func NewPtyIO(ptmx *os.File, cols, rows int) *PtyIO {
	screen := te.NewScreen(cols, rows)
	stream := te.NewStream(screen, false)
	p := &PtyIO{
		ptmx:   ptmx,
		ch:     make(chan []byte, 256),
		screen: screen,
		stream: stream,
	}
	go p.readLoop()
	return p
}

func (p *PtyIO) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			p.extractSyncAcks(data)

			p.mu.Lock()
			p.stream.FeedBytes(data)
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
func (p *PtyIO) ScreenLines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.screen.Display()
}

// ScreenLine returns a single row from the screen.
func (p *PtyIO) ScreenLine(row int) string {
	lines := p.ScreenLines()
	if row < 0 || row >= len(lines) {
		return ""
	}
	return lines[row]
}

// ScreenCells returns the full cell data including attributes and colors.
func (p *PtyIO) ScreenCells() [][]te.Cell {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.screen.LinesCells()
}

// WaitForScreen polls the virtual screen until check returns true or timeout.
func (p *PtyIO) WaitForScreen(check func([]string) bool, desc string, timeout time.Duration) ([]string, error) {
	deadline := time.After(timeout)
	for {
		lines := p.ScreenLines()
		if check(lines) {
			return lines, nil
		}
		select {
		case <-deadline:
			return lines, fmt.Errorf("timeout (%v) waiting for %s\nscreen:\n%s", timeout, desc, strings.Join(lines, "\n"))
		case _, ok := <-p.ch:
			if !ok {
				lines = p.ScreenLines()
				if check(lines) {
					return lines, nil
				}
				return lines, fmt.Errorf("PTY closed while waiting for %s\nscreen:\n%s", desc, strings.Join(lines, "\n"))
			}
		}
	}
}

// WaitFor reads PTY output until needle appears on the virtual screen.
func (p *PtyIO) WaitFor(needle string, timeout time.Duration) ([]string, error) {
	return p.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.Contains(line, needle) {
				return true
			}
		}
		return false
	}, "screen to contain "+needle, timeout)
}

// WaitForSilence drains output until no new data arrives for the given duration.
func (p *PtyIO) WaitForSilence(duration time.Duration) {
	for {
		select {
		case _, ok := <-p.ch:
			if !ok {
				return
			}
		case <-time.After(duration):
			return
		}
	}
}

// FindOnScreen returns the row and column where needle first appears, or (-1,-1).
func FindOnScreen(lines []string, needle string) (row, col int) {
	for i, line := range lines {
		if j := strings.Index(line, needle); j >= 0 {
			return i, j
		}
	}
	return -1, -1
}

// Resize changes the PTY window size and updates the virtual screen to match.
func (p *PtyIO) Resize(cols, rows uint16) {
	pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	p.mu.Lock()
	p.screen.Resize(int(rows), int(cols))
	p.mu.Unlock()
}

// Write sends raw bytes to the PTY (simulating keyboard input).
func (p *PtyIO) Write(data []byte) {
	p.ptmx.Write(data)
}

// Ch returns the channel that receives PTY output chunks.
// The channel is closed when the PTY read loop exits (e.g. process exited).
func (p *PtyIO) Ch() <-chan []byte {
	return p.ch
}

// WriteSync writes an OSC 2459;nx;sync;<id> marker to the PTY input.
// rawio in the TUI will strip it and emit a SyncMsg. Pair with
// WaitSync (on the same id) to block until the TUI has processed all
// prior stdin input.
func (p *PtyIO) WriteSync(id string) {
	p.Write(syncPayload(id))
}

// WaitSync blocks until the TUI has emitted the ack for id (OSC
// 2459;nx;ack;<id>) on stdout. Returns an error on timeout.
func (p *PtyIO) WaitSync(id string, timeout time.Duration) error {
	p.ackMu.Lock()
	if p.ackSeen == nil {
		p.ackSeen = make(map[string]bool)
	}
	if p.ackSeen[id] {
		delete(p.ackSeen, id)
		p.ackMu.Unlock()
		return nil
	}
	w := make(chan struct{})
	if p.ackWaiters == nil {
		p.ackWaiters = make(map[string]chan struct{})
	}
	p.ackWaiters[id] = w
	p.ackMu.Unlock()
	select {
	case <-w:
		return nil
	case <-time.After(timeout):
		p.ackMu.Lock()
		delete(p.ackWaiters, id)
		p.ackMu.Unlock()
		return fmt.Errorf("timeout (%v) waiting for sync ack %q", timeout, id)
	}
}

// extractSyncAcks scans chunk for OSC 2459;nx;ack;<id>(BEL|ST) markers
// and releases any matching WaitSync waiters. Handles sequences split
// across reads by carrying over the tail of unmatched bytes.
func (p *PtyIO) extractSyncAcks(chunk []byte) {
	const prefix = "\x1b]2459;nx;ack;"
	prefixB := []byte(prefix)

	p.ackMu.Lock()
	combined := append(p.ackPending, chunk...)
	p.ackPending = nil
	p.ackMu.Unlock()

	i := 0
	for i < len(combined) {
		pi := bytes.Index(combined[i:], prefixB)
		if pi < 0 {
			// No more markers — but retain the tail that could start
			// the prefix next time (up to len(prefix)-1 bytes).
			if len(combined)-i > len(prefix)-1 {
				i = len(combined) - (len(prefix) - 1)
			}
			p.ackMu.Lock()
			p.ackPending = append(p.ackPending[:0], combined[i:]...)
			p.ackMu.Unlock()
			return
		}
		start := i + pi + len(prefixB)
		end := start
		for end < len(combined) {
			if combined[end] == 0x07 {
				break
			}
			if combined[end] == 0x1b && end+1 < len(combined) && combined[end+1] == '\\' {
				break
			}
			end++
		}
		if end >= len(combined) {
			// Truncated — save from prefix start for next read.
			p.ackMu.Lock()
			p.ackPending = append(p.ackPending[:0], combined[i+pi:]...)
			p.ackMu.Unlock()
			return
		}
		id := string(combined[start:end])
		p.ackMu.Lock()
		if p.ackSeen == nil {
			p.ackSeen = make(map[string]bool)
		}
		if w, ok := p.ackWaiters[id]; ok {
			close(w)
			delete(p.ackWaiters, id)
		} else {
			p.ackSeen[id] = true
		}
		p.ackMu.Unlock()
		if combined[end] == 0x07 {
			i = end + 1
		} else {
			i = end + 2
		}
	}
}

