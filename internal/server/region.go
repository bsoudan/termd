package server

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	te "nxtermd/pkg/te"
	"nxtermd/internal/protocol"
)

// Region is the interface that all region types implement.
type Region interface {
	ID() string
	Name() string
	Cmd() string
	Pid() int
	Session() string
	SetSession(string)
	Width() int
	Height() int

	Snapshot() Snapshot
	GetScrollback() ScrollbackResult
	WriteInput([]byte)
	Resize(width, height uint16) error
	Kill()
	Close()

	ScrollbackLen() int
	IsNative() bool
	Stats() protocol.RegionStats

	// Subscriber management — backed by the region actor.
	AddSubscriber(c *Client) Snapshot
	RemoveSubscriber(clientID uint32)

	// Overlay — registration/clearing go through the event loop for
	// bookkeeping, which delegates to these methods on the region.
	RegisterOverlay(client *Client) overlayRegisterResult
	RenderOverlay(clientID uint32, cells [][]protocol.ScreenCell, cursorRow, cursorCol uint16, modes map[int]bool)
	ClearOverlay(clientID uint32)
}

type Snapshot struct {
	Lines           []string
	CursorRow       uint16
	CursorCol       uint16
	Cells           [][]protocol.ScreenCell
	Modes           map[int]bool
	Title           string
	IconName        string
	ScrollbackLen   int
	ScrollbackTotal uint64
}

// DefaultScrollbackSize is the default maximum number of lines kept in
// each region's scrollback buffer when no override is configured via
// ServerConfig.Scrollback.Size.
const DefaultScrollbackSize = 10000

// PTYRegion wraps a PTY region backed by an actor goroutine. The actor
// owns all mutable state (screen, subscribers, overlay). PTYRegion is a
// thin wrapper that sends messages to the actor.
type PTYRegion struct {
	id      string
	name    string
	cmd     string
	pid     int
	session string

	actor *regionActor

	// width and height are updated atomically by Resize so callers
	// outside the actor can read them without a round trip.
	width  atomic.Int32
	height atomic.Int32
}

func (r *PTYRegion) ID() string          { return r.id }
func (r *PTYRegion) Name() string        { return r.name }
func (r *PTYRegion) Cmd() string         { return r.cmd }
func (r *PTYRegion) Pid() int            { return r.pid }
func (r *PTYRegion) Session() string     { return r.session }
func (r *PTYRegion) SetSession(s string) { r.session = s }
func (r *PTYRegion) Width() int          { return int(r.width.Load()) }
func (r *PTYRegion) Height() int         { return int(r.height.Load()) }
func (r *PTYRegion) IsNative() bool      { return false }

// Snapshot returns the current screen state, composited with the overlay
// if one is active.
func (r *PTYRegion) Snapshot() Snapshot {
	resp := make(chan Snapshot, 1)
	select {
	case r.actor.msgs <- snapshotMsg{resp: resp}:
	case <-r.actor.actorDone:
		return Snapshot{}
	}
	select {
	case snap := <-resp:
		return snap
	case <-r.actor.actorDone:
		return Snapshot{}
	}
}

func (r *PTYRegion) GetScrollback() ScrollbackResult {
	resp := make(chan ScrollbackResult, 1)
	select {
	case r.actor.msgs <- scrollbackMsg{resp: resp}:
	case <-r.actor.actorDone:
		return ScrollbackResult{}
	}
	select {
	case sb := <-resp:
		return sb
	case <-r.actor.actorDone:
		return ScrollbackResult{}
	}
}

func (r *PTYRegion) Stats() protocol.RegionStats {
	return readRegionStats(r.actor)
}

func (r *PTYRegion) ScrollbackLen() int {
	resp := make(chan int, 1)
	select {
	case r.actor.msgs <- scrollbackLenMsg{resp: resp}:
	case <-r.actor.actorDone:
		return 0
	}
	select {
	case n := <-resp:
		return n
	case <-r.actor.actorDone:
		return 0
	}
}

func (r *PTYRegion) Resize(width, height uint16) error {
	resp := make(chan error, 1)
	select {
	case r.actor.msgs <- resizeMsg{width: width, height: height, resp: resp}:
	case <-r.actor.actorDone:
		return fmt.Errorf("region stopped")
	}
	select {
	case err := <-resp:
		if err == nil {
			r.width.Store(int32(width))
			r.height.Store(int32(height))
		}
		return err
	case <-r.actor.actorDone:
		return fmt.Errorf("region stopped")
	}
}

// WriteInput writes directly to the backend. For PTY regions this is a
// thread-safe kernel write and does not go through the actor.
func (r *PTYRegion) WriteInput(data []byte) {
	r.actor.backend.WriteInput(data)
}

func (r *PTYRegion) Kill() {
	r.actor.backend.Kill()
}

func (r *PTYRegion) Close() {
	r.actor.backend.Close()
}

// AddSubscriber adds a client to this region's subscriber set and
// returns the initial composited snapshot. The screen_update is sent
// to the client inside the actor before the subscriber is added,
// guaranteeing ordering relative to subsequent terminal_events.
func (r *PTYRegion) AddSubscriber(c *Client) Snapshot {
	resp := make(chan Snapshot, 1)
	select {
	case r.actor.msgs <- addSubscriberMsg{client: c, resp: resp}:
	case <-r.actor.actorDone:
		return Snapshot{}
	}
	select {
	case snap := <-resp:
		return snap
	case <-r.actor.actorDone:
		return Snapshot{}
	}
}

// RemoveSubscriber removes a client from the subscriber set. If the
// client owned the overlay, it is cleared. Fire-and-forget.
func (r *PTYRegion) RemoveSubscriber(clientID uint32) {
	select {
	case r.actor.msgs <- removeSubscriberMsg{clientID: clientID}:
	case <-r.actor.actorDone:
	}
}

func (r *PTYRegion) RegisterOverlay(client *Client) overlayRegisterResult {
	resp := make(chan overlayRegisterResult, 1)
	select {
	case r.actor.msgs <- overlayRegisterMsg{client: client, resp: resp}:
	case <-r.actor.actorDone:
		return overlayRegisterResult{err: "region stopped"}
	}
	select {
	case result := <-resp:
		return result
	case <-r.actor.actorDone:
		return overlayRegisterResult{err: "region stopped"}
	}
}

func (r *PTYRegion) RenderOverlay(clientID uint32, cells [][]protocol.ScreenCell, cursorRow, cursorCol uint16, modes map[int]bool) {
	select {
	case r.actor.msgs <- overlayRenderMsg{
		clientID: clientID, cells: cells,
		cursorRow: cursorRow, cursorCol: cursorCol, modes: modes,
	}:
	case <-r.actor.actorDone:
	}
}

func (r *PTYRegion) ClearOverlay(clientID uint32) {
	select {
	case r.actor.msgs <- overlayClearMsg{clientID: clientID}:
	case <-r.actor.actorDone:
	}
}

// DetachPTY dups the PTY master FD for handoff to a new process. The
// caller must call StopActor first; the dup'd FD goes to the new
// process, while the original is left in place for Close() to clean up.
func (r *PTYRegion) DetachPTY() *os.File {
	f, err := r.actor.backend.DetachForUpgrade()
	if err != nil {
		slog.Error("DetachPTY: dup failed", "region_id", r.id, "err", err)
		return nil
	}
	return f
}

// StopActor stops the actor goroutine and its readLoop. Used during
// live upgrade to freeze terminal state for consistent snapshotting.
func (r *PTYRegion) StopActor() error {
	resp := make(chan struct{}, 1)
	select {
	case r.actor.msgs <- stopActorMsg{resp: resp}:
	case <-r.actor.actorDone:
		return nil
	}
	select {
	case <-resp:
		return nil
	case <-r.actor.actorDone:
		return nil
	}
}

// ResumeActor restarts the actor after a failed upgrade rollback.
// The caller must have called StopActor first.
func (r *PTYRegion) ResumeActor(destroyFn func(string)) error {
	if err := r.actor.backend.ResumeReader(); err != nil {
		return fmt.Errorf("resume reader: %w", err)
	}
	a := r.actor
	a.destroyFn = destroyFn
	a.actorDone = make(chan struct{})
	a.msgs = make(chan regionMsg, actorChanSize)
	a.stopped = false
	a.start()
	return nil
}

// ActorDone returns a channel that is closed when the actor exits.
func (r *PTYRegion) ActorDone() <-chan struct{} {
	return r.actor.actorDone
}

// ── Construction ─────────────────────────────────────────────────────────────

func NewRegion(cmdStr string, args []string, env map[string]string, width, height, scrollbackSize int, socketAddr, version string, destroyFn func(string)) (Region, error) {
	id := generateUUID()
	name := extractName(cmdStr)

	cmdObj := exec.Command(cmdStr, args...)
	cmdObj.Env = append(os.Environ(), "TERM=xterm-256color", "PS1=nxterm$ ")
	if socketAddr != "" {
		cmdObj.Env = append(cmdObj.Env, "NXTERMD_SOCKET="+socketAddr, "NXTERMD_REGIONID="+id)
	}
	for k, v := range env {
		cmdObj.Env = append(cmdObj.Env, k+"="+v)
	}
	ptmx, err := pty.Start(cmdObj)
	if err != nil {
		return nil, err
	}
	if err := setNonblockPollable(ptmx); err != nil {
		ptmx.Close()
		return nil, fmt.Errorf("set ptmx nonblock: %w", err)
	}
	if err := setWinsize(ptmx, uint16(height), uint16(width)); err != nil {
		ptmx.Close()
		return nil, fmt.Errorf("set ptmx winsize: %w", err)
	}

	backend := newPTYBackend(id, ptmx, cmdObj, cmdObj.Process.Pid)

	hscreen := te.NewHistoryScreen(width, height, scrollbackSize)
	hscreen.Screen.TerminalName = "nxterm(" + version + ")"
	hscreen.Screen.WriteProcessInput = func(data string) {
		backend.WriteInput([]byte(data))
	}

	actor := newRegionActor(id, backend, width, height, hscreen, destroyFn)

	r := &PTYRegion{
		id:    id,
		name:  name,
		cmd:   cmdStr,
		pid:   cmdObj.Process.Pid,
		actor: actor,
	}
	r.width.Store(int32(width))
	r.height.Store(int32(height))

	slog.Debug("spawned child", "pid", r.pid, "cmd", cmdStr)
	actor.start()

	return r, nil
}

// RestoreRegion reconstructs a PTYRegion from serialized state and a PTY FD.
// Used by the new process during live upgrade.
func RestoreRegion(node protocol.RegionNode, ptmxFile *os.File, histState *te.HistoryState, version string, destroyFn func(string)) Region {
	backend := newPTYBackend(node.ID, ptmxFile, nil, node.Pid)

	// Capacity is overwritten by UnmarshalState (preserves the serialized
	// state's capacity across upgrade), so the value passed here is a
	// placeholder.
	hscreen := te.NewHistoryScreen(node.Width, node.Height, DefaultScrollbackSize)
	hscreen.UnmarshalState(histState)
	hscreen.Screen.TerminalName = "nxterm(" + version + ")"
	hscreen.Screen.WriteProcessInput = func(data string) {
		backend.WriteInput([]byte(data))
	}

	if err := setNonblockPollable(ptmxFile); err != nil {
		slog.Warn("restore: setNonblockPollable failed", "region_id", node.ID, "err", err)
	}

	actor := newRegionActor(node.ID, backend, node.Width, node.Height, hscreen, destroyFn)

	r := &PTYRegion{
		id:      node.ID,
		name:    node.Name,
		cmd:     node.Cmd,
		pid:     node.Pid,
		session: node.Session,
		actor:   actor,
	}
	r.width.Store(int32(node.Width))
	r.height.Store(int32(node.Height))

	actor.start()
	return r
}

// ── PTY FD helpers ───────────────────────────────────────────────────────────

// withPTYFd runs fn with the PTY master's raw file descriptor without
// going through *os.File.Fd(). SyscallConn().Control keeps the runtime
// poller registration intact.
func withPTYFd(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := rc.Control(func(fd uintptr) {
		inner = fn(int(fd))
	}); err != nil {
		return err
	}
	return inner
}

// setNonblockPollable puts a *os.File in non-blocking mode without
// disturbing Go's runtime poller registration.
func setNonblockPollable(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := rc.Control(func(fd uintptr) {
		inner = unix.SetNonblock(int(fd), true)
	}); err != nil {
		return err
	}
	return inner
}

// setWinsize is a SyscallConn-friendly equivalent of pty.Setsize.
func setWinsize(f *os.File, rows, cols uint16) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	ws := pty.Winsize{Rows: rows, Cols: cols}
	var inner error
	if err := rc.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
		if errno != 0 {
			inner = errno
		}
	}); err != nil {
		return err
	}
	return inner
}

// ── Shared helpers ───────────────────────────────────────────────────────────

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
	if c.Attr.Faint {
		a |= 128
	}
	pc.A = a
	return pc
}

func colorToSpec(c te.Color) string {
	switch c.Mode {
	case te.ColorDefault:
		return ""
	case te.ColorANSI16:
		return c.Name
	case te.ColorANSI256:
		return fmt.Sprintf("5;%d", c.Index)
	case te.ColorTrueColor:
		return "2;" + c.Name
	}
	return ""
}

// maxCarry is the maximum number of bytes carried across reads.
const maxCarry = 256

// sequenceSafe prepends any carried-over bytes from a previous read to chunk,
// then returns the longest prefix that ends on a complete sequence boundary.
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
	safeEnd := 0
	for safeEnd < len(buf) {
		_, _, n, newState := ansi.DecodeSequence(buf[safeEnd:], ansi.NormalState, nil)
		if n == 0 {
			break
		}
		if newState != ansi.NormalState {
			break
		}
		safeEnd += n
	}
	for safeEnd > 0 && !utf8.Valid(buf[:safeEnd]) {
		safeEnd--
	}
	remaining := buf[safeEnd:]
	if len(remaining) > len(carryBuf) {
		return buf, 0
	}
	return buf[:safeEnd], copy(carryBuf, remaining)
}

func padLine(line string, width int) string {
	runeCount := utf8.RuneCountInString(line)
	if runeCount == width {
		return line
	}
	if runeCount > width {
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
	return line + strings.Repeat(" ", width-runeCount)
}

func blankLine(width int) string {
	return strings.Repeat(" ", width)
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
