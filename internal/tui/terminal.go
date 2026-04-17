package tui

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"nxtermd/internal/protocol"
	te "nxtermd/pkg/te"
)

// modeAltScreenLegacy is the original xterm alternate screen mode (DEC private 47).
const modeAltScreenLegacy = 47

// privateModeKey converts a DEC private mode number to the key used
// by go-te's Screen.Mode map, which shifts private modes left by 5 bits.
func privateModeKey(mode int) int {
	return mode << 5
}

// TerminalLayer owns screen state, capabilities, and server communication
// for a single terminal region. Scrollback is handled by pushing a
// ScrollbackLayer onto the main layer stack via NewScrollbackLayer.
type TerminalLayer struct {
	hscreen      *te.HistoryScreen
	lines        []string
	cursorRow    int
	cursorCol    int
	pendingClear bool
	disconnected bool
	inScrollback bool // true while a ScrollbackLayer is on the stack

	server          *Server
	regionID        string
	regionName      string
	termWidth       int
	termHeight      int
	statusBarMargin int

	termEnv       map[string]string
	keyboardFlags int
	bgDark        *bool
}

// NewTerminalLayer creates a terminal layer for a region.
func NewTerminalLayer(server *Server, regionID, regionName string, width, height, statusBarMargin int) *TerminalLayer {
	return &TerminalLayer{
		server:          server,
		regionID:        regionID,
		regionName:      regionName,
		termWidth:       width,
		termHeight:      height,
		statusBarMargin: statusBarMargin,
	}
}

// Activate subscribes to this terminal's region and sends a resize.
func (t *TerminalLayer) Activate() tea.Cmd {
	t.server.Send(protocol.SubscribeRequest{RegionID: t.regionID})
	if t.termWidth > 0 && t.termHeight > 2 {
		t.server.Send(protocol.ResizeRequest{
			RegionID: t.regionID,
			Width:    uint16(t.termWidth),
			Height:   uint16(t.contentHeight()),
		})
	}
	return nil
}

// Deactivate unsubscribes from this terminal's region.
func (t *TerminalLayer) Deactivate() {
	t.server.Send(protocol.UnsubscribeRequest{RegionID: t.regionID})
}

func (t *TerminalLayer) contentHeight() int {
	h := t.termHeight - 1 - t.statusBarMargin // tab bar + status-bar margin
	if h < 1 {
		h = 1
	}
	return h
}

// Update implements the Layer interface.
func (t *TerminalLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case protocol.ScreenUpdate:
		return nil, t.handleScreenUpdate(msg.Lines, msg.Cells, msg.CursorRow, msg.CursorCol, msg.Modes, msg.Title, msg.IconName, msg.ScrollbackLen), true
	case protocol.GetScreenResponse:
		return nil, t.handleScreenUpdate(msg.Lines, msg.Cells, msg.CursorRow, msg.CursorCol, msg.Modes, msg.Title, msg.IconName, msg.ScrollbackLen), true
	case protocol.TerminalEvents:
		return nil, t.handleTerminalEvents(msg.Events), true
	case protocol.ResizeResponse:
		return nil, nil, true
	case tea.WindowSizeMsg:
		t.termWidth = msg.Width
		t.termHeight = msg.Height
		t.server.Send(protocol.ResizeRequest{
			RegionID: t.regionID,
			Width:    uint16(msg.Width),
			Height:   uint16(t.contentHeight()),
		})
		return nil, nil, true
	case tea.KeyboardEnhancementsMsg:
		t.keyboardFlags = msg.Flags
		return nil, nil, true
	case tea.BackgroundColorMsg:
		dark := msg.IsDark()
		t.bgDark = &dark
		return nil, nil, true
	case tea.EnvMsg:
		t.termEnv = make(map[string]string)
		for _, key := range []string{"TERM", "COLORTERM", "TERM_PROGRAM"} {
			if v := msg.Getenv(key); v != "" {
				t.termEnv[key] = v
			}
		}
		return nil, nil, true
	case SessionCmd:
		switch msg.Name {
		case "scroll-up":
			if t.inScrollback {
				return nil, nil, true
			}
			halfPage := t.contentHeight() / 2
			if halfPage < 1 {
				halfPage = 1
			}
			sl := t.NewScrollbackLayer(halfPage)
			return nil, func() tea.Msg { return PushLayerMsg{Layer: sl} }, true
		case "scroll-down":
			// Only meaningful when already in scrollback (handled by ScrollbackLayer).
			return nil, nil, true
		}
		return nil, nil, false
	default:
		return nil, nil, false
	}
}

func (t *TerminalLayer) handleScreenUpdate(lines []string, cells [][]protocol.ScreenCell, cursorRow, cursorCol uint16, modes map[int]bool, title, iconName string, serverScrollback int) tea.Cmd {
	height := t.contentHeight()
	if t.termHeight <= 0 {
		height = 23
	}
	width := t.termWidth
	if width <= 0 {
		width = 80
	}
	t.lines = lines
	t.cursorRow = int(cursorRow)
	t.cursorCol = int(cursorCol)
	// Preserve scrollback history across screen resets. A screen_update
	// (e.g., from synchronized output mode 2026) replaces the screen but
	// the scrollback lines accumulated by the client — including any
	// prepended from a server sync — should survive.
	//
	// The client's history may have grown past the server's scrollback
	// count because terminal_events replayed before this snapshot pushed
	// lines into history that are now part of the screen content in the
	// snapshot. Trim to the server's count to avoid duplicates at the
	// history/screen boundary.
	var prevHistory [][]te.Cell
	if t.hscreen != nil {
		prevHistory = t.hscreen.History()
		if serverScrollback > 0 && len(prevHistory) > serverScrollback {
			prevHistory = prevHistory[:serverScrollback]
		}
	}
	t.hscreen = te.NewHistoryScreen(width, height, 10000)
	if len(prevHistory) > 0 {
		t.hscreen.PrependHistory(prevHistory)
	}
	if len(cells) > 0 {
		initScreenFromCells(t.hscreen.Screen, cells)
	} else {
		for i, line := range lines {
			if i > 0 {
				t.hscreen.LineFeed()
				t.hscreen.CarriageReturn()
			}
			t.hscreen.Draw(line)
		}
	}
	t.hscreen.CursorPosition(int(cursorRow)+1, int(cursorCol)+1)
	t.hscreen.Title = title
	t.hscreen.IconName = iconName
	// Replace the mode set entirely rather than merging — a missing
	// key in the snapshot means "unset" on the server, and merging
	// would silently keep stale modes alive (most importantly DECTCEM,
	// the cursor visibility flag).
	t.hscreen.Mode = make(map[int]struct{}, len(modes))
	for k, v := range modes {
		if v {
			t.hscreen.Mode[k] = struct{}{}
		}
	}

	if t.pendingClear {
		t.pendingClear = false
		return func() tea.Msg { return tea.ClearScreen() }
	}
	return nil
}

func (t *TerminalLayer) handleTerminalEvents(events []protocol.TerminalEvent) tea.Cmd {
	if t.hscreen == nil {
		return nil
	}
	// Extract sync markers: they are test signals, not screen operations.
	// Each sync is returned as a SyncMsg so the main event loop can
	// queue the ack and emit it after the next render (before-render
	// emission loses the ordering against the rendered frame bytes).
	var syncIDs []string
	filtered := events[:0]
	for _, ev := range events {
		if ev.Op == "sync" {
			syncIDs = append(syncIDs, ev.Data)
			continue
		}
		filtered = append(filtered, ev)
	}
	needsClear := replayEvents(t.hscreen, filtered)
	t.cursorRow = t.hscreen.Cursor.Row
	t.cursorCol = t.hscreen.Cursor.Col
	var cmds []tea.Cmd
	if needsClear {
		cmds = append(cmds, func() tea.Msg { return tea.ClearScreen() })
	}
	for _, id := range syncIDs {
		cmds = append(cmds, func() tea.Msg { return SyncMsg{ID: id} })
	}
	if len(cmds) == 0 {
		return nil
	}
	if len(cmds) == 1 {
		return cmds[0]
	}
	return tea.Batch(cmds...)
}

// View implements the Layer interface. Renders terminal content, or
// delegates to the scrollback layer if active. The active param controls
// cursor visibility. SessionLayer sets disconnected before calling View.
func (t *TerminalLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	showCursor := rs.Active
	// Honor DECTCEM (private mode 25). Programs that draw their own
	// cursor — bubbletea-based TUIs, less, claude code, a nested
	// nxterm — emit \e[?25l to hide ours so we don't render a phantom
	// reverse-video cell on top of theirs (which would otherwise show
	// up as a doubled cursor in nested sessions or a stray cursor at
	// the bottom-left in claude code).
	if t.hscreen != nil {
		if _, ok := t.hscreen.Mode[privateModeKey(25)]; !ok {
			showCursor = false
		}
	}
	dim := !rs.Active

	var sb strings.Builder
	if dim {
		sb.WriteString("\x1b[2m")
	}
	if t.hscreen != nil {
		cells := t.hscreen.LinesCells()
		for i := range height {
			var row []te.Cell
			if i < len(cells) {
				row = cells[i]
			}
			if dim {
				renderCellLineDim(&sb, row, width)
			} else {
				renderCellLine(&sb, row, width, i, t.cursorRow, t.cursorCol, showCursor, t.disconnected, rs.CommandMode)
			}
			if i < height-1 {
				sb.WriteByte('\n')
			}
		}
	} else {
		// Plain text fallback (no screen yet)
		for i := range height {
			line := ""
			if i < len(t.lines) {
				line = t.lines[i]
			}
			runes := []rune(line)
			if len(runes) > width {
				runes = runes[:width]
			}
			for col := range width {
				ch := ' '
				if col < len(runes) {
					ch = runes[col]
				}
				if showCursor && i == t.cursorRow && col == t.cursorCol {
					if rs.CommandMode {
						sb.WriteString(ansi.SGR(ansi.AttrBold, ansi.AttrReverse, ansi.AttrBrightCyanForegroundColor))
						sb.WriteByte('?')
						sb.WriteString(ansi.ResetStyle)
					} else {
						sb.WriteString(ansi.SGR(ansi.AttrReverse))
						sb.WriteRune(ch)
						sb.WriteString(ansi.SGR(ansi.AttrNoReverse))
					}
				} else {
					sb.WriteRune(ch)
				}
			}
			if i < height-1 {
				sb.WriteByte('\n')
			}
		}
	}
	if dim {
		sb.WriteString("\x1b[0m")
	}
	return []*lipgloss.Layer{lipgloss.NewLayer(sb.String())}
}

// Title returns the display name for the tab bar and outer window
// title. The PTY-set title (OSC 0/2) takes precedence; if none has been
// set, the server-assigned region name is used as a fallback.
func (t *TerminalLayer) Title() string {
	if t.hscreen != nil && t.hscreen.Title != "" {
		return t.hscreen.Title
	}
	return t.regionName
}

// Status implements the TermdLayer interface.
func (t *TerminalLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	return "", lipgloss.Style{}
}

func (t *TerminalLayer) WantsKeyboardInput() bool {
	return false
}

// NewScrollbackLayer creates a scrollback layer for this terminal and
// sends a server scrollback request. The caller is responsible for
// pushing it onto the layer stack.
// ScrollbackActive returns whether scrollback mode is active.
func (t *TerminalLayer) ScrollbackActive() bool { return t.inScrollback }

// NewScrollbackLayer creates a scrollback layer for this terminal and
// sends a server scrollback request. The caller is responsible for
// pushing it onto the layer stack.
func (t *TerminalLayer) NewScrollbackLayer(offset int) *ScrollbackLayer {
	t.inScrollback = true
	sl := newScrollbackLayer(t, offset)
	// Request server scrollback so we can sync any lines the client missed.
	t.server.Send(protocol.GetScrollbackRequest{RegionID: t.regionID})
	return sl
}

// SetPendingClear marks that a screen clear should happen on the next screen update.
func (t *TerminalLayer) SetPendingClear() {
	t.pendingClear = true
}

// ForwardMouse encodes and sends a mouse event to the server.
func (t *TerminalLayer) ForwardMouse(msg tea.MouseMsg) {
	mouse := msg.Mouse()
	seq := encodeSGRMouse(msg, mouse.X, mouse.Y-1)
	if seq != "" {
		data := base64.StdEncoding.EncodeToString([]byte(seq))
		t.server.Send(protocol.InputMsg{
			RegionID: t.regionID, Data: data,
		})
	}
}

// IsAltScreen reports whether the child's alternate screen buffer is active.
func (t *TerminalLayer) IsAltScreen() bool {
	if t.hscreen == nil {
		return false
	}
	return t.hscreen.IsAltScreenActive()
}

// ChildWantsMouse checks if the child application has mouse mode enabled.
func (t *TerminalLayer) ChildWantsMouse() bool {
	if t.hscreen == nil {
		return false
	}
	_, m1000 := t.hscreen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]
	_, m1002 := t.hscreen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]
	_, m1003 := t.hscreen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
	return m1000 || m1002 || m1003
}

// MouseMode returns the bubbletea mouse mode based on the child's mode state.
func (t *TerminalLayer) MouseMode() int {
	if t.hscreen == nil {
		return 0
	}
	_, m1003 := t.hscreen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
	if m1003 {
		return 2 // AllMotion
	}
	return 1 // CellMotion (default for scroll wheel)
}

// ScreenCells returns the current cell data for rendering.
func (t *TerminalLayer) ScreenCells() [][]te.Cell {
	if t.hscreen == nil {
		return nil
	}
	return t.hscreen.LinesCells()
}

// ScrollbackLines returns the local scrollback history as cell rows.
func (t *TerminalLayer) ScrollbackLines() [][]te.Cell {
	if t.hscreen == nil {
		return nil
	}
	return t.hscreen.History()
}

// RegionID returns the terminal's region ID.
func (t *TerminalLayer) RegionID() string { return t.regionID }

// Width returns the terminal width.
func (t *TerminalLayer) Width() int { return t.termWidth }

// Height returns the terminal height.
func (t *TerminalLayer) Height() int { return t.termHeight }

// KeyboardFlags returns kitty keyboard protocol flags.
func (t *TerminalLayer) KeyboardFlags() int { return t.keyboardFlags }

// BgDark returns the background darkness state.
func (t *TerminalLayer) BgDark() *bool { return t.bgDark }

// TermEnv returns terminal environment variables.
func (t *TerminalLayer) TermEnv() map[string]string { return t.termEnv }

// Screen returns the underlying te.Screen (for status caps mouse mode detection).
func (t *TerminalLayer) Screen() *te.Screen {
	if t.hscreen == nil {
		return nil
	}
	return t.hscreen.Screen
}

// MouseModes returns a human-readable mouse mode string for status display.
// decPrivateModeNames maps DEC private mode numbers to short
// human-readable names. Used by Modes() for the status dialog.
var decPrivateModeNames = map[int]string{
	1:    "DECCKM(app-cursor)",
	3:    "DECCOLM(132col)",
	4:    "DECSCLM(smooth-scroll)",
	5:    "DECSCNM(reverse-video)",
	6:    "DECOM(origin)",
	7:    "DECAWM(autowrap)",
	8:    "DECARM(autorepeat)",
	9:    "mouse-x10",
	12:   "att610(blink-cursor)",
	25:   "DECTCEM(cursor-visible)",
	40:   "allow-80-to-132",
	45:   "reverse-wrap",
	47:   "alt-screen-legacy",
	66:   "DECNKM(app-keypad)",
	67:   "DECBKM(backarrow=BS)",
	1000: "mouse-normal",
	1001: "mouse-highlight",
	1002: "mouse-button-event",
	1003: "mouse-any-event",
	1004: "focus-events",
	1005: "mouse-utf8",
	1006: "mouse-sgr",
	1015: "mouse-urxvt",
	1016: "mouse-pixel",
	1047: "alt-screen",
	1048: "save-cursor",
	1049: "alt-screen+save",
	2004: "bracketed-paste",
	2026: "synchronized-output",
	2027: "grapheme-clusters",
}

// ansiModeNames maps ANSI (non-private) mode numbers to short names.
var ansiModeNames = map[int]string{
	2:  "KAM(keyboard-action)",
	4:  "IRM(insert)",
	12: "SRM(send-receive)",
	20: "LNM(linefeed-newline)",
}

// Modes returns a comma-separated list of currently-set terminal modes
// (both DEC private and ANSI), formatted for the status dialog. Each
// mode is shown with its friendly name and decimal number; unknown
// modes appear as "?(N)".
func (t *TerminalLayer) Modes() string {
	if t.hscreen == nil || len(t.hscreen.Mode) == 0 {
		return ""
	}
	// Collect set modes as (number, isPrivate) pairs so we can sort.
	type entry struct {
		num     int
		private bool
	}
	var entries []entry
	for k := range t.hscreen.Mode {
		if k >= 32 {
			// DEC private mode keys are stored shifted left by 5.
			entries = append(entries, entry{num: k >> 5, private: true})
		} else {
			entries = append(entries, entry{num: k, private: false})
		}
	}
	// Sort: ANSI first by number, then DEC private by number.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].private != entries[j].private {
			return !entries[i].private
		}
		return entries[i].num < entries[j].num
	})
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.private {
			if name, ok := decPrivateModeNames[e.num]; ok {
				parts = append(parts, fmt.Sprintf("%s(%d)", name, e.num))
			} else {
				parts = append(parts, fmt.Sprintf("?(%d)", e.num))
			}
		} else {
			if name, ok := ansiModeNames[e.num]; ok {
				parts = append(parts, fmt.Sprintf("%s(%d)", name, e.num))
			} else {
				parts = append(parts, fmt.Sprintf("?ansi(%d)", e.num))
			}
		}
	}
	return strings.Join(parts, ", ")
}

func (t *TerminalLayer) MouseModes() string {
	if t.hscreen == nil {
		return ""
	}
	var modes []string
	if _, ok := t.hscreen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]; ok {
		modes = append(modes, "normal(1000)")
	}
	if _, ok := t.hscreen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]; ok {
		modes = append(modes, "button(1002)")
	}
	if _, ok := t.hscreen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]; ok {
		modes = append(modes, "any(1003)")
	}
	if _, ok := t.hscreen.Mode[privateModeKey(ansi.ModeMouseExtSgr.Mode())]; ok {
		modes = append(modes, "sgr(1006)")
	}
	if len(modes) > 0 {
		return strings.Join(modes, ", ")
	}
	return "off"
}

// ── Cell rendering ──────────────────────────────────────────────────────────

func renderCellLine(sb *strings.Builder, row []te.Cell, width, rowIdx, cursorRow, cursorCol int, showCursor bool, disconnected bool, commandMode bool) {
	var cur te.Attr // tracks current SGR state (zero = default)
	col := 0
	for col < width {
		var cell te.Cell
		if col < len(row) {
			cell = row[col]
		} else {
			cell.Data = " "
		}

		isCursor := showCursor && rowIdx == cursorRow && col == cursorCol

		target := cell.Attr
		if isCursor {
			if disconnected {
				target = te.Attr{
					Reverse: true,
					Fg:      te.Color{Mode: te.ColorANSI16, Name: "red"},
				}
				cell.Data = "X"
			} else if commandMode {
				target = te.Attr{
					Bold:    true,
					Reverse: true,
					Fg:      te.Color{Mode: te.ColorANSI16, Name: "brightcyan"},
				}
				cell.Data = "?"
			} else {
				target.Reverse = !target.Reverse
			}
		}

		if target != cur {
			sb.WriteString(sgrTransition(cur, target))
			cur = target
		}

		ch := cell.Data
		if ch == "" || ch == "\x00" {
			ch = " "
			col++
		} else {
			w := runewidth.StringWidth(ch)
			if w < 1 {
				w = 1
			}
			col += w
		}
		sb.WriteString(ch)
	}

	if cur != (te.Attr{}) {
		sb.WriteString(ansi.ResetStyle)
	}
}

// renderCellLineDim renders a row as plain text with no colors or attributes,
// for use when the terminal is dimmed behind an overlay.
func renderCellLineDim(sb *strings.Builder, row []te.Cell, width int) {
	col := 0
	for col < width {
		ch := " "
		if col < len(row) {
			ch = row[col].Data
			if ch == "" || ch == "\x00" {
				ch = " "
				col++
			} else {
				w := runewidth.StringWidth(ch)
				if w < 1 {
					w = 1
				}
				col += w
			}
		} else {
			col++
		}
		sb.WriteString(ch)
	}
}

// sgrTransition emits the SGR sequence to move from one attribute set to another.
func sgrTransition(from, to te.Attr) string {
	if to == (te.Attr{}) {
		return ansi.ResetStyle
	}

	var attrs []ansi.Attr

	needsReset := (from.Bold && !to.Bold) ||
		(from.Faint && !to.Faint) ||
		(from.Blink && !to.Blink) ||
		(from.Conceal && !to.Conceal)

	if needsReset {
		attrs = append(attrs, ansi.AttrReset)
		from = te.Attr{}
	}

	if to.Bold && !from.Bold {
		attrs = append(attrs, ansi.AttrBold)
	}
	if to.Faint && !from.Faint {
		attrs = append(attrs, ansi.AttrFaint)
	}
	if to.Italics && !from.Italics {
		attrs = append(attrs, ansi.AttrItalic)
	} else if !to.Italics && from.Italics {
		attrs = append(attrs, ansi.AttrNoItalic)
	}
	if to.Underline && !from.Underline {
		attrs = append(attrs, ansi.AttrUnderline)
	} else if !to.Underline && from.Underline {
		attrs = append(attrs, ansi.AttrNoUnderline)
	}
	if to.Blink && !from.Blink {
		attrs = append(attrs, ansi.AttrBlink)
	}
	if to.Reverse && !from.Reverse {
		attrs = append(attrs, ansi.AttrReverse)
	} else if !to.Reverse && from.Reverse {
		attrs = append(attrs, ansi.AttrNoReverse)
	}
	if to.Conceal && !from.Conceal {
		attrs = append(attrs, ansi.AttrConceal)
	}
	if to.Strikethrough && !from.Strikethrough {
		attrs = append(attrs, ansi.AttrStrikethrough)
	} else if !to.Strikethrough && from.Strikethrough {
		attrs = append(attrs, ansi.AttrNoStrikethrough)
	}

	if to.Fg != from.Fg {
		attrs = append(attrs, teColorAttrs(to.Fg, false)...)
	}
	if to.Bg != from.Bg {
		attrs = append(attrs, teColorAttrs(to.Bg, true)...)
	}

	if len(attrs) == 0 {
		return ""
	}

	return ansi.SGR(attrs...)
}

func teColorAttrs(c te.Color, isBg bool) []ansi.Attr {
	switch c.Mode {
	case te.ColorDefault:
		if isBg {
			return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
		}
		return []ansi.Attr{ansi.AttrDefaultForegroundColor}
	case te.ColorANSI16:
		if isBg {
			if code, ok := protocol.BgSGRCode[c.Name]; ok {
				return []ansi.Attr{code}
			}
			return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
		}
		if code, ok := protocol.FgSGRCode[c.Name]; ok {
			return []ansi.Attr{code}
		}
		return []ansi.Attr{ansi.AttrDefaultForegroundColor}
	case te.ColorANSI256:
		if isBg {
			return []ansi.Attr{ansi.AttrExtendedBackgroundColor, 5, ansi.Attr(c.Index)}
		}
		return []ansi.Attr{ansi.AttrExtendedForegroundColor, 5, ansi.Attr(c.Index)}
	case te.ColorTrueColor:
		r, g, b := protocol.ParseHexColor(c.Name)
		if isBg {
			return []ansi.Attr{ansi.AttrExtendedBackgroundColor, 2, ansi.Attr(r), ansi.Attr(g), ansi.Attr(b)}
		}
		return []ansi.Attr{ansi.AttrExtendedForegroundColor, 2, ansi.Attr(r), ansi.Attr(g), ansi.Attr(b)}
	}
	if isBg {
		return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
	}
	return []ansi.Attr{ansi.AttrDefaultForegroundColor}
}

// ── Screen helpers ──────────────────────────────────────────────────────────

// ReplayEvents replays terminal events on a screen. Exported for server tests.
func ReplayEvents(screen te.EventHandler, events []protocol.TerminalEvent) bool {
	return replayEvents(screen, events)
}

func replayEvents(screen te.EventHandler, events []protocol.TerminalEvent) bool {
	needsClear := false
	for _, ev := range events {
		switch ev.Op {
		case "draw":
			screen.Draw(ev.Data)
		case "cup":
			screen.CursorPosition(ev.Params...)
		case "cuu":
			screen.CursorUp(ev.Params...)
		case "cud":
			screen.CursorDown(ev.Params...)
		case "cuf":
			screen.CursorForward(ev.Params...)
		case "cub":
			screen.CursorBack(ev.Params...)
		case "su":
			screen.ScrollUp(ev.Params...)
		case "sd":
			screen.ScrollDown(ev.Params...)
		case "ed":
			screen.EraseInDisplay(ev.How, ev.Private)
		case "el":
			screen.EraseInLine(ev.How, ev.Private)
		case "il":
			screen.InsertLines(ev.Params...)
		case "dl":
			screen.DeleteLines(ev.Params...)
		case "ich":
			screen.InsertCharacters(ev.Params...)
		case "dch":
			screen.DeleteCharacters(ev.Params...)
		case "ech":
			screen.EraseCharacters(ev.Params...)
		case "sgr":
			screen.SelectGraphicRendition(ev.Attrs, ev.Private)
		case "lf":
			screen.LineFeed()
		case "cr":
			screen.CarriageReturn()
		case "tab":
			screen.Tab()
		case "bs":
			screen.Backspace()
		case "ind":
			screen.Index()
		case "ri":
			screen.ReverseIndex()
		case "decstbm":
			screen.SetMargins(ev.Params...)
		case "sc":
			screen.SaveCursor()
		case "rc":
			screen.RestoreCursor()
		case "decsc":
			screen.SaveCursorDEC()
		case "decrc":
			screen.RestoreCursorDEC()
		case "cud1":
			screen.CursorDown1(ev.Params...)
		case "cuu1":
			screen.CursorUp1(ev.Params...)
		case "cha":
			screen.CursorToColumn(ev.Params...)
		case "hpa":
			screen.CursorToColumnAbsolute(ev.Params...)
		case "cbt":
			screen.CursorBackTab(ev.Params...)
		case "cht":
			screen.CursorForwardTab(ev.Params...)
		case "vpa":
			screen.CursorToLine(ev.Params...)
		case "nel":
			screen.NextLine()
		case "so":
			screen.ShiftOut()
		case "si":
			screen.ShiftIn()
		case "hts":
			screen.SetTabStop()
		case "tbc":
			screen.ClearTabStop(ev.Params...)
		case "decaln":
			screen.AlignmentDisplay()
		case "fi":
			screen.ForwardIndex()
		case "bi":
			screen.BackIndex()
		case "decstr":
			screen.SoftReset()
		case "spa":
			screen.StartProtectedArea()
		case "epa":
			screen.EndProtectedArea()
		case "rep":
			screen.RepeatLast(ev.Params...)
		case "decsca":
			if len(ev.Params) > 0 {
				screen.SetCharacterProtection(ev.Params[0])
			}
		case "declrmm":
			if len(ev.Params) >= 2 {
				screen.SetLeftRightMargins(ev.Params[0], ev.Params[1])
			}
		case "decic":
			if len(ev.Params) > 0 {
				screen.InsertColumns(ev.Params[0])
			}
		case "decdc":
			if len(ev.Params) > 0 {
				screen.DeleteColumns(ev.Params[0])
			}
		case "decer":
			if len(ev.Params) >= 4 {
				screen.EraseRectangle(ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3])
			}
		case "decser":
			if len(ev.Params) >= 4 {
				screen.SelectiveEraseRectangle(ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3])
			}
		case "decfra":
			if len(ev.Params) >= 4 && len(ev.Data) > 0 {
				ch := []rune(ev.Data)[0]
				screen.FillRectangle(ch, ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3])
			}
		case "deccra":
			if len(ev.Params) >= 6 {
				screen.CopyRectangle(ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3], ev.Params[4], ev.Params[5])
			}
		case "savem":
			screen.SaveModes(ev.Params)
		case "restm":
			screen.RestoreModes(ev.Params)
		case "icon":
			screen.SetIconName(ev.Data)
		case "charset":
			if parts := splitCharset(ev.Data); len(parts) == 2 {
				screen.DefineCharset(parts[0], parts[1])
			}
		case "winop":
			screen.WindowOp(ev.Params)
		case "decscl":
			if len(ev.Params) >= 2 {
				screen.SetConformance(ev.Params[0], ev.Params[1])
			}
		case "titlemode":
			screen.SetTitleMode(ev.Params, false)
		case "decscusr":
			if len(ev.Params) > 0 {
				if s, ok := screen.(*te.Screen); ok {
					s.SetCursorStyle(ev.Params[0])
				} else if h, ok := screen.(*te.HistoryScreen); ok {
					h.Screen.SetCursorStyle(ev.Params[0])
				}
			}
		case "decsasd":
			if len(ev.Params) > 0 {
				if s, ok := screen.(*te.Screen); ok {
					s.SetActiveStatusDisplay(ev.Params[0])
				} else if h, ok := screen.(*te.HistoryScreen); ok {
					h.Screen.SetActiveStatusDisplay(ev.Params[0])
				}
			}
		case "reset":
			screen.Reset()
		case "title":
			screen.SetTitle(ev.Data)
		case "bell":
			screen.Bell()
		case "sm":
			screen.SetMode(ev.Params, ev.Private)
			if ev.Private {
				for _, m := range ev.Params {
					if m == ansi.ModeAltScreenSaveCursor.Mode() || m == ansi.ModeAltScreen.Mode() || m == modeAltScreenLegacy {
						needsClear = true
					}
				}
			}
		case "rm":
			screen.ResetMode(ev.Params, ev.Private)
			if ev.Private {
				for _, m := range ev.Params {
					if m == ansi.ModeAltScreenSaveCursor.Mode() || m == ansi.ModeAltScreen.Mode() || m == modeAltScreenLegacy {
						needsClear = true
					}
				}
			}
		}
	}
	return needsClear
}

func initScreenFromCells(screen *te.Screen, cells [][]protocol.ScreenCell) {
	for row := range screen.Buffer {
		if row >= len(cells) {
			break
		}
		for col := range screen.Buffer[row] {
			if col >= len(cells[row]) {
				break
			}
			pc := cells[row][col]
			ch := pc.Char
			if ch == "" || ch == "\x00" {
				ch = " "
			}
			screen.Buffer[row][col] = te.Cell{
				Data: ch,
				Attr: te.Attr{
					Fg:            specToColor(pc.Fg),
					Bg:            specToColor(pc.Bg),
					Bold:          pc.A&1 != 0,
					Italics:       pc.A&2 != 0,
					Underline:     pc.A&4 != 0,
					Strikethrough: pc.A&8 != 0,
					Reverse:       pc.A&16 != 0,
					Blink:         pc.A&32 != 0,
					Conceal:       pc.A&64 != 0,
					Faint:         pc.A&128 != 0,
				},
			}
		}
	}
}

func specToColor(spec string) te.Color {
	if spec == "" {
		return te.Color{Mode: te.ColorDefault, Name: "default"}
	}
	if len(spec) > 2 && spec[0] == '5' && spec[1] == ';' {
		var idx uint8
		fmt.Sscanf(spec[2:], "%d", &idx)
		return te.Color{Mode: te.ColorANSI256, Index: idx}
	}
	if len(spec) > 2 && spec[0] == '2' && spec[1] == ';' {
		return te.Color{Mode: te.ColorTrueColor, Name: spec[2:]}
	}
	idx, _ := protocol.ANSI16NameToIndex[spec]
	return te.Color{Mode: te.ColorANSI16, Name: spec, Index: idx}
}

func splitCharset(s string) []string {
	i := strings.Index(s, ":")
	if i < 0 {
		return nil
	}
	return []string{s[:i], s[i+1:]}
}
