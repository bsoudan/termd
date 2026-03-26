package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/creack/pty"
	te "github.com/rcarmo/go-te/pkg/te"
)

type Snapshot struct {
	Lines     []string
	CursorRow uint16
	CursorCol uint16
}

type Region struct {
	id   string
	name string
	cmd  string
	pid  int

	width  int
	height int

	ptmx   *os.File
	cmdObj *exec.Cmd
	screen *te.Screen
	stream *te.Stream
	mu     sync.Mutex

	notify chan struct{}
	done   chan struct{}
}

func NewRegion(cmdStr string, args []string, width, height int) (*Region, error) {
	id := generateUUID()
	name := extractName(cmdStr)

	cmdObj := exec.Command(cmdStr, args...)
	cmdObj.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmdObj, &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	})
	if err != nil {
		return nil, err
	}

	screen := te.NewScreen(width, height)
	stream := te.NewStream(screen, false)

	r := &Region{
		id:     id,
		name:   name,
		cmd:    cmdStr,
		pid:    cmdObj.Process.Pid,
		width:  width,
		height: height,
		ptmx:   ptmx,
		cmdObj: cmdObj,
		screen: screen,
		stream: stream,
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}

	slog.Debug("spawned child", "pid", r.pid, "cmd", cmdStr)

	go r.readLoop()
	go r.waitLoop()

	return r, nil
}

func (r *Region) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := r.ptmx.Read(buf)
		if n > 0 {
			r.mu.Lock()
			r.stream.FeedBytes(buf[:n])
			r.mu.Unlock()

			// Non-blocking send to coalesce multiple reads into one notification
			select {
			case r.notify <- struct{}{}:
			default:
			}
		}
		if err != nil {
			break
		}
	}
}

func (r *Region) waitLoop() {
	r.cmdObj.Wait()
	close(r.notify)
}

func (r *Region) WriteInput(data []byte) {
	r.ptmx.Write(data)
}

func (r *Region) Resize(width, height uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := pty.Setsize(r.ptmx, &pty.Winsize{
		Rows: height,
		Cols: width,
	}); err != nil {
		return err
	}

	r.screen.Resize(int(height), int(width))
	r.width = int(width)
	r.height = int(height)

	slog.Debug("region resized", "region_id", r.id, "width", width, "height", height)
	return nil
}

func (r *Region) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	display := r.screen.Display()
	lines := make([]string, r.height)

	for i := 0; i < r.height; i++ {
		if i < len(display) {
			lines[i] = padLine(display[i], r.width)
		} else {
			lines[i] = strings.Repeat(" ", r.width)
		}
	}

	return Snapshot{
		Lines:     lines,
		CursorRow: uint16(r.screen.Cursor.Row),
		CursorCol: uint16(r.screen.Cursor.Col),
	}
}

func (r *Region) Kill() {
	r.cmdObj.Process.Signal(syscall.SIGKILL)
}

func (r *Region) Close() {
	r.ptmx.Close()
}

// padLine pads or truncates a line to exactly width characters (by rune count).
func padLine(line string, width int) string {
	runeCount := utf8.RuneCountInString(line)
	if runeCount == width {
		return line
	}
	if runeCount > width {
		// Truncate to width runes
		var b strings.Builder
		n := 0
		for _, r := range line {
			if n >= width {
				break
			}
			b.WriteRune(r)
			n++
		}
		return b.String()
	}
	// Pad with spaces
	return line + strings.Repeat(" ", width-runeCount)
}

func generateUUID() string {
	var b [16]byte
	io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func extractName(cmd string) string {
	return filepath.Base(cmd)
}
