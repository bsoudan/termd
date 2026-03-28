package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	te "github.com/rcarmo/go-te/pkg/te"
	"termd/frontend/protocol"
)

// Terminal manages the virtual screen state replicated from the server.
type Terminal struct {
	Screen     *te.Screen
	lines      []string
	CursorRow  int
	CursorCol  int
	pendingClear bool
}

// HandleScreenUpdate initializes the screen from a full snapshot.
func (t Terminal) HandleScreenUpdate(lines []string, cells [][]protocol.ScreenCell, cursorRow, cursorCol uint16, width, height int) Terminal {
	t.lines = lines
	t.CursorRow = int(cursorRow)
	t.CursorCol = int(cursorCol)
	if width <= 0 {
		width = 80
	}
	t.Screen = te.NewScreen(width, height)
	if len(cells) > 0 {
		initScreenFromCells(t.Screen, cells)
	} else {
		for i, line := range lines {
			if i > 0 {
				t.Screen.LineFeed()
				t.Screen.CarriageReturn()
			}
			t.Screen.Draw(line)
		}
	}
	t.Screen.CursorPosition(int(cursorRow)+1, int(cursorCol)+1)
	return t
}

// HandleTerminalEvents replays events on the screen. Returns the updated
// terminal and whether a full screen clear is needed (alt screen toggle).
func (t Terminal) HandleTerminalEvents(events []protocol.TerminalEvent) (Terminal, bool) {
	if t.Screen == nil {
		return t, false
	}
	needsClear := replayEvents(t.Screen, events)
	t.CursorRow = t.Screen.Cursor.Row
	t.CursorCol = t.Screen.Cursor.Col
	return t, needsClear
}

// ConsumePendingClear checks and clears the pendingClear flag.
func (t Terminal) ConsumePendingClear() (Terminal, bool) {
	if t.pendingClear {
		t.pendingClear = false
		return t, true
	}
	return t, false
}

// SetPendingClear marks that a screen clear should happen on the next update.
func (t Terminal) SetPendingClear() Terminal {
	t.pendingClear = true
	return t
}

// ChildWantsMouse checks if the child application has mouse mode enabled.
func (t Terminal) ChildWantsMouse() bool {
	if t.Screen == nil {
		return false
	}
	_, m1000 := t.Screen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]
	_, m1002 := t.Screen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]
	_, m1003 := t.Screen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
	return m1000 || m1002 || m1003
}

// MouseMode returns the bubbletea mouse mode based on the child's mode state.
func (t Terminal) MouseMode() int {
	if t.Screen == nil {
		return 0
	}
	_, m1003 := t.Screen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
	if m1003 {
		return 2 // AllMotion
	}
	return 1 // CellMotion (default for scroll wheel)
}

// ScreenCells returns the current cell data for rendering.
func (t Terminal) ScreenCells() [][]te.Cell {
	if t.Screen == nil {
		return nil
	}
	return t.Screen.LinesCells()
}

// View renders the terminal content area.
func (t Terminal) View(sb *strings.Builder, width, height int, showCursor, disconnected bool) {
	if t.Screen != nil {
		cells := t.Screen.LinesCells()
		for i := range height {
			var row []te.Cell
			if i < len(cells) {
				row = cells[i]
			}
			renderCellLine(sb, row, width, i, t.CursorRow, t.CursorCol, showCursor, disconnected)
			if i < height-1 {
				sb.WriteByte('\n')
			}
		}
		return
	}

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
			if showCursor && i == t.CursorRow && col == t.CursorCol {
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
