package ui

import (
	"encoding/base64"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	te "termd/pkg/te"
	"termd/frontend/protocol"
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
// ScrollbackLayer onto the inner stack.
type TerminalLayer struct {
	screen       *te.Screen
	lines        []string
	cursorRow    int
	cursorCol    int
	pendingClear bool
	disconnected bool

	scrollbackLayer *ScrollbackLayer // non-nil when scrollback is active

	server     *Server
	regionID   string
	regionName string
	termWidth  int
	termHeight int

	termEnv       map[string]string
	keyboardFlags int
	bgDark        *bool
}

// NewTerminalLayer creates a terminal layer for a region.
func NewTerminalLayer(server *Server, regionID, regionName string, width, height int) *TerminalLayer {
	return &TerminalLayer{
		server:     server,
		regionID:   regionID,
		regionName: regionName,
		termWidth:  width,
		termHeight: height,
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
	h := t.termHeight - 1 // tab bar
	if h < 1 {
		h = 1
	}
	return h
}

// Update implements the Layer interface.
func (t *TerminalLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	// If scrollback is active, delegate input and scrollback data to it.
	if sl := t.scrollbackLayer; sl != nil {
		resp, cmd, handled := sl.Update(msg)
		if _, ok := resp.(QuitLayerMsg); ok {
			t.scrollbackLayer = nil
		}
		if handled {
			return nil, cmd, true
		}
		// Fall through for messages scrollback doesn't handle
		// (e.g., terminal events, screen updates continue below).
	}

	switch msg := msg.(type) {
	case protocol.ScreenUpdate:
		return nil, t.handleScreenUpdate(msg.Lines, msg.Cells, msg.CursorRow, msg.CursorCol, msg.Modes), true
	case protocol.GetScreenResponse:
		return nil, t.handleScreenUpdate(msg.Lines, msg.Cells, msg.CursorRow, msg.CursorCol, msg.Modes), true
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
	default:
		return nil, nil, false
	}
}

func (t *TerminalLayer) handleScreenUpdate(lines []string, cells [][]protocol.ScreenCell, cursorRow, cursorCol uint16, modes map[int]bool) tea.Cmd {
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
	t.screen = te.NewScreen(width, height)
	if len(cells) > 0 {
		initScreenFromCells(t.screen, cells)
	} else {
		for i, line := range lines {
			if i > 0 {
				t.screen.LineFeed()
				t.screen.CarriageReturn()
			}
			t.screen.Draw(line)
		}
	}
	t.screen.CursorPosition(int(cursorRow)+1, int(cursorCol)+1)
	for k, v := range modes {
		if v {
			t.screen.Mode[k] = struct{}{}
		}
	}

	if t.pendingClear {
		t.pendingClear = false
		return func() tea.Msg { return tea.ClearScreen() }
	}
	return nil
}

func (t *TerminalLayer) handleTerminalEvents(events []protocol.TerminalEvent) tea.Cmd {
	if t.screen == nil {
		return nil
	}
	needsClear := replayEvents(t.screen, events)
	t.cursorRow = t.screen.Cursor.Row
	t.cursorCol = t.screen.Cursor.Col
	if needsClear {
		return func() tea.Msg { return tea.ClearScreen() }
	}
	return nil
}

// View implements the Layer interface. Renders terminal content, or
// delegates to the scrollback layer if active. The active param controls
// cursor visibility. SessionLayer sets disconnected before calling View.
func (t *TerminalLayer) View(width, height int, active bool) []*lipgloss.Layer {
	// If scrollback is active, render it instead of the live terminal.
	if t.scrollbackLayer != nil {
		return t.scrollbackLayer.View(width, height, active)
	}

	showCursor := active
	dim := !active

	var sb strings.Builder
	if dim {
		sb.WriteString("\x1b[2m")
	}
	if t.screen != nil {
		cells := t.screen.LinesCells()
		for i := range height {
			var row []te.Cell
			if i < len(cells) {
				row = cells[i]
			}
			if dim {
				renderCellLineDim(&sb, row, width)
			} else {
				renderCellLine(&sb, row, width, i, t.cursorRow, t.cursorCol, showCursor, t.disconnected)
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
					sb.WriteString(ansi.SGR(ansi.AttrReverse))
					sb.WriteRune(ch)
					sb.WriteString(ansi.SGR(ansi.AttrNoReverse))
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

// Title returns the region name for the tab bar.
func (t *TerminalLayer) Title() string { return t.regionName }

// Status implements the TermdLayer interface.
func (t *TerminalLayer) Status() (string, lipgloss.Style) {
	if sl := t.scrollbackLayer; sl != nil {
		return sl.Status()
	}
	return "", lipgloss.Style{}
}

func (t *TerminalLayer) WantsKeyboardInput() *KeyboardFilter {
	if t.scrollbackLayer != nil {
		return allKeysFilter
	}
	return nil
}

// ScrollbackActive returns whether scrollback mode is active.
func (t *TerminalLayer) ScrollbackActive() bool { return t.scrollbackLayer != nil }

// EnterScrollback activates scrollback mode and requests data from the server.
func (t *TerminalLayer) EnterScrollback(offset int) {
	t.scrollbackLayer = newScrollbackLayer(t, offset)
	t.server.Send(protocol.GetScrollbackRequest{RegionID: t.regionID})
}

// ExitScrollback deactivates scrollback mode.
func (t *TerminalLayer) ExitScrollback() {
	t.scrollbackLayer = nil
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

// ChildWantsMouse checks if the child application has mouse mode enabled.
func (t *TerminalLayer) ChildWantsMouse() bool {
	if t.screen == nil {
		return false
	}
	_, m1000 := t.screen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]
	_, m1002 := t.screen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]
	_, m1003 := t.screen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
	return m1000 || m1002 || m1003
}

// MouseMode returns the bubbletea mouse mode based on the child's mode state.
func (t *TerminalLayer) MouseMode() int {
	if t.screen == nil {
		return 0
	}
	_, m1003 := t.screen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
	if m1003 {
		return 2 // AllMotion
	}
	return 1 // CellMotion (default for scroll wheel)
}

// ScreenCells returns the current cell data for rendering.
func (t *TerminalLayer) ScreenCells() [][]te.Cell {
	if t.screen == nil {
		return nil
	}
	return t.screen.LinesCells()
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
func (t *TerminalLayer) Screen() *te.Screen { return t.screen }

// MouseModes returns a human-readable mouse mode string for status display.
func (t *TerminalLayer) MouseModes() string {
	if t.screen == nil {
		return ""
	}
	var modes []string
	if _, ok := t.screen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]; ok {
		modes = append(modes, "normal(1000)")
	}
	if _, ok := t.screen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]; ok {
		modes = append(modes, "button(1002)")
	}
	if _, ok := t.screen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]; ok {
		modes = append(modes, "any(1003)")
	}
	if _, ok := t.screen.Mode[privateModeKey(ansi.ModeMouseExtSgr.Mode())]; ok {
		modes = append(modes, "sgr(1006)")
	}
	if len(modes) > 0 {
		return strings.Join(modes, ", ")
	}
	return "off"
}

// ── Cell rendering ──────────────────────────────────────────────────────────

func renderCellLine(sb *strings.Builder, row []te.Cell, width, rowIdx, cursorRow, cursorCol int, showCursor bool, disconnected bool) {
	var cur te.Attr // tracks current SGR state (zero = default)
	for col := range width {
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
	for col := range width {
		ch := " "
		if col < len(row) {
			ch = row[col].Data
			if ch == "" || ch == "\x00" {
				ch = " "
			}
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
		(from.Blink && !to.Blink) ||
		(from.Conceal && !to.Conceal)

	if needsReset {
		attrs = append(attrs, ansi.AttrReset)
		from = te.Attr{}
	}

	if to.Bold && !from.Bold {
		attrs = append(attrs, ansi.AttrBold)
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
func ReplayEvents(screen *te.Screen, events []protocol.TerminalEvent) bool {
	return replayEvents(screen, events)
}

func replayEvents(screen *te.Screen, events []protocol.TerminalEvent) bool {
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
				screen.SetCursorStyle(ev.Params[0])
			}
		case "decsasd":
			if len(ev.Params) > 0 {
				screen.SetActiveStatusDisplay(ev.Params[0])
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
