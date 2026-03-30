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
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	te "termd/pkg/te"
	"termd/frontend/protocol"
)

type Snapshot struct {
	Lines     []string
	CursorRow uint16
	CursorCol uint16
	Cells     [][]protocol.ScreenCell
	Modes     map[int]bool
}

// scrollbackSize is the maximum number of lines kept in the scrollback buffer.
const scrollbackSize = 10000

type Region struct {
	id      string
	name    string
	cmd     string
	pid     int
	session string

	width  int
	height int

	ptmx    *os.File
	cmdObj  *exec.Cmd
	screen  *te.Screen
	hscreen *te.HistoryScreen
	proxy   *EventProxy
	stream  *te.Stream
	mu      sync.Mutex

	notify     chan struct{}
	readerDone chan struct{}
	stopRead   chan struct{} // closed to stop readLoop for live upgrade
}

func NewRegion(cmdStr string, args []string, env map[string]string, width, height int) (*Region, error) {
	id := generateUUID()
	name := extractName(cmdStr)

	cmdObj := exec.Command(cmdStr, args...)
	cmdObj.Env = append(os.Environ(), "TERM=xterm-256color", "PS1=termd$ ")
	for k, v := range env {
		cmdObj.Env = append(cmdObj.Env, k+"="+v)
	}

	ptmx, err := pty.StartWithSize(cmdObj, &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	})
	if err != nil {
		return nil, err
	}

	hscreen := te.NewHistoryScreen(width, height, scrollbackSize)
	proxy := NewEventProxy(hscreen)
	stream := te.NewStream(proxy, false)

	r := &Region{
		id:      id,
		name:    name,
		cmd:     cmdStr,
		pid:     cmdObj.Process.Pid,
		width:   width,
		height:  height,
		ptmx:    ptmx,
		cmdObj:  cmdObj,
		screen:  hscreen.Screen,
		hscreen: hscreen,
		proxy:   proxy,
		stream:  stream,
		notify:     make(chan struct{}, 1),
		readerDone: make(chan struct{}),
		stopRead:   make(chan struct{}),
	}

	slog.Debug("spawned child", "pid", r.pid, "cmd", cmdStr)

	go r.readLoop()
	go r.waitLoop()

	return r, nil
}

// maxCarry is the maximum number of bytes carried across reads. This must be
// large enough to hold any incomplete ANSI escape sequence or UTF-8 character
// that could span a read boundary.
const maxCarry = 256

func (r *Region) readLoop() {
	defer close(r.readerDone)
	buf := make([]byte, 4096)
	var carry [maxCarry]byte
	var carryN int
	for {
		n, err := r.ptmx.Read(buf)
		if n > 0 {
			data, cn := sequenceSafe(carry[:carryN], buf[:n], carry[:])
			carryN = cn

			r.mu.Lock()
			r.stream.FeedBytes(data)
			r.mu.Unlock()

			// Non-blocking send to coalesce multiple reads into one notification
			select {
			case r.notify <- struct{}{}:
			default:
			}
		}
		if err != nil {
			// If stopRead is closed, this is a controlled stop for upgrade.
			select {
			case <-r.stopRead:
			default:
				slog.Debug("readLoop exiting", "region_id", r.id, "err", err)
			}
			break
		}
	}
}

// sequenceSafe prepends any carried-over bytes from a previous read to chunk,
// then returns the longest prefix that ends on a complete sequence boundary.
// It uses charmbracelet's DecodeSequence to detect incomplete ANSI escape
// sequences, and additionally checks for incomplete UTF-8 at the tail (which
// DecodeSequence does not catch). Remaining bytes are copied into carry.
func sequenceSafe(carry, chunk, carryBuf []byte) (safe []byte, carryN int) {
	var buf []byte
	if len(carry) > 0 {
		buf = make([]byte, len(carry)+len(chunk))
		copy(buf, carry)
		copy(buf[len(carry):], chunk)
	} else {
		buf = chunk
	}

	if len(buf) == 0 {
		return nil, 0
	}

	// Walk through complete sequences using DecodeSequence.
	safeEnd := 0
	for safeEnd < len(buf) {
		_, _, n, newState := ansi.DecodeSequence(buf[safeEnd:], ansi.NormalState, nil)
		if n == 0 {
			break
		}
		if newState != ansi.NormalState {
			// Mid-escape-sequence — carry the rest.
			break
		}
		safeEnd += n
	}

	// DecodeSequence treats incomplete UTF-8 leader bytes (e.g. 0xC3 alone)
	// as valid single-byte sequences. Check whether the last consumed byte
	// starts an incomplete UTF-8 character and pull it back into carry.
	for safeEnd > 0 && !utf8.Valid(buf[:safeEnd]) {
		safeEnd--
	}

	remaining := buf[safeEnd:]
	if len(remaining) > len(carryBuf) {
		// Carry buffer overflow — feed everything to avoid unbounded growth.
		return buf, 0
	}
	return buf[:safeEnd], copy(carryBuf, remaining)
}

func (r *Region) waitLoop() {
	if r.cmdObj != nil {
		r.cmdObj.Wait()
	} else {
		// Inherited region: child is not our process. Detect exit via
		// PTY master EOF (readLoop closes readerDone).
		<-r.readerDone
	}
	close(r.notify)
}

func (r *Region) WriteInput(data []byte) {
	if _, err := r.ptmx.Write(data); err != nil {
		slog.Debug("write input error", "region_id", r.id, "err", err)
	}
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

	// Include cell-level color/attribute data.
	// Read Buffer directly to avoid go-te's LinesCells() which can panic
	// when Buffer has more rows than Lines after a resize.
	numRows := r.height
	if numRows > len(r.screen.Buffer) {
		numRows = len(r.screen.Buffer)
	}
	cells := make([][]protocol.ScreenCell, numRows)
	for row := 0; row < numRows; row++ {
		srcRow := r.screen.Buffer[row]
		cells[row] = make([]protocol.ScreenCell, len(srcRow))
		for col, c := range srcRow {
			cells[row][col] = cellToProtocol(c)
		}
	}

	var modes map[int]bool
	if len(r.screen.Mode) > 0 {
		modes = make(map[int]bool, len(r.screen.Mode))
		for k := range r.screen.Mode {
			modes[k] = true
		}
	}

	return Snapshot{
		Lines:     lines,
		CursorRow: uint16(r.screen.Cursor.Row),
		CursorCol: uint16(r.screen.Cursor.Col),
		Cells:     cells,
		Modes:     modes,
	}
}

func cellToProtocol(c te.Cell) protocol.ScreenCell {
	pc := protocol.ScreenCell{Char: c.Data}
	pc.Fg = colorToSpec(c.Attr.Fg)
	pc.Bg = colorToSpec(c.Attr.Bg)
	var a uint8
	if c.Attr.Bold {
		a |= 1
	}
	if c.Attr.Italics {
		a |= 2
	}
	if c.Attr.Underline {
		a |= 4
	}
	if c.Attr.Strikethrough {
		a |= 8
	}
	if c.Attr.Reverse {
		a |= 16
	}
	if c.Attr.Blink {
		a |= 32
	}
	if c.Attr.Conceal {
		a |= 64
	}
	pc.A = a
	return pc
}

func colorToSpec(c te.Color) string {
	switch c.Mode {
	case te.ColorDefault:
		return ""
	case te.ColorANSI16:
		return c.Name // e.g., "red", "brightgreen"
	case te.ColorANSI256:
		return fmt.Sprintf("5;%d", c.Index)
	case te.ColorTrueColor:
		return "2;" + c.Name // Name is hex like "ff8700"
	}
	return ""
}

// GetScrollback returns the scrollback history as cell data.
func (r *Region) GetScrollback() [][]protocol.ScreenCell {
	r.mu.Lock()
	defer r.mu.Unlock()

	history := r.hscreen.History()
	if len(history) == 0 {
		return nil
	}
	cells := make([][]protocol.ScreenCell, len(history))
	for i, row := range history {
		cells[i] = make([]protocol.ScreenCell, len(row))
		for j, c := range row {
			cells[i][j] = cellToProtocol(c)
		}
	}
	return cells
}

// FlushEvents returns accumulated events. If a synchronized output batch
// completed (mode 2026), needsSnapshot is true — the caller should send a
// screen_update snapshot, then send any trailing events that came after the
// sync ended.
func (r *Region) FlushEvents() (events []protocol.TerminalEvent, needsSnapshot bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proxy.Flush()
}

func (r *Region) Kill() {
	if r.cmdObj != nil {
		r.cmdObj.Process.Signal(syscall.SIGKILL)
	} else if r.pid > 0 {
		syscall.Kill(r.pid, syscall.SIGKILL)
	}
}

func (r *Region) Close() {
	r.ptmx.Close()
}

// StopReadLoop stops the readLoop goroutine and returns a dup'd PTY FD
// for handoff. The original FD is closed (unblocking readLoop's Read).
// The caller should use the returned file for the new process.
// StopReadLoop stops the readLoop by sending SIGWINCH to the child process
// (which causes the PTY to become readable) and signaling via stopRead.
// Returns a dup'd PTY FD for handoff. If readLoop doesn't exit quickly,
// proceeds without waiting (the FD is dup'd so the handoff still works).
func (r *Region) StopReadLoop() *os.File {
	newFD, err := syscall.Dup(int(r.ptmx.Fd()))
	if err != nil {
		slog.Error("StopReadLoop: dup failed", "err", err)
		return nil
	}
	dupFile := os.NewFile(uintptr(newFD), r.ptmx.Name())

	close(r.stopRead)
	// Send SIGWINCH to child to generate PTY activity, unblocking the Read.
	if r.pid > 0 {
		syscall.Kill(r.pid, syscall.SIGWINCH)
	}
	// Wait briefly for readLoop to notice and exit.
	select {
	case <-r.readerDone:
	case <-time.After(500 * time.Millisecond):
		slog.Debug("StopReadLoop: readLoop did not exit in time, proceeding", "region_id", r.id)
	}

	r.ptmx = dupFile
	return dupFile
}

// RestoreRegion reconstructs a Region from serialized state and a PTY FD.
// Used by the new process during live upgrade. The child process is already
// running (inherited from the old process); cmdObj is nil.
func RestoreRegion(id, name, cmd, session string, pid, width, height int, ptmxFile *os.File, histState *te.HistoryState) *Region {
	hscreen := te.NewHistoryScreen(width, height, scrollbackSize)
	hscreen.UnmarshalState(histState)
	hscreen.Screen.WriteProcessInput = func(data string) {
		ptmxFile.Write([]byte(data))
	}

	proxy := NewEventProxy(hscreen)
	stream := te.NewStream(proxy, false)

	r := &Region{
		id:      id,
		name:    name,
		cmd:     cmd,
		pid:     pid,
		session: session,
		width:   width,
		height:  height,
		ptmx:    ptmxFile,
		screen:  hscreen.Screen,
		hscreen: hscreen,
		proxy:   proxy,
		stream:  stream,
		notify:     make(chan struct{}, 1),
		readerDone: make(chan struct{}),
		stopRead:   make(chan struct{}),
	}

	go r.readLoop()
	go r.waitLoop()

	return r
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
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failure: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func extractName(cmd string) string {
	return filepath.Base(cmd)
}
