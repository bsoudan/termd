package main

import (
	"github.com/charmbracelet/x/ansi"
	te "nxtermd/pkg/te"
	"nxtermd/frontend/protocol"
)

type EventProxy struct {
	screen       te.EventHandler
	batch        []protocol.TerminalEvent
	syncMode     bool // true when synchronized output mode (2026) is active
	syncEndIndex int  // index in batch where sync mode ended (-1 = no sync completed)
}

func NewEventProxy(screen te.EventHandler) *EventProxy {
	return &EventProxy{screen: screen, syncEndIndex: -1}
}

// Flush returns accumulated events and whether a snapshot is needed.
// If a synchronized output batch completed (mode 2026), needsSnapshot is true
// and events contains only the events AFTER the sync ended. The caller should
// send a screen_update snapshot first, then send these trailing events.
func (p *EventProxy) Flush() (events []protocol.TerminalEvent, needsSnapshot bool) {
	if p.syncMode {
		// Still in sync mode — hold everything.
		return nil, false
	}

	if p.syncEndIndex >= 0 {
		// Sync completed. The snapshot captures the full screen state
		// including any events after the sync ended, so discard the
		// entire batch.
		p.batch = nil
		p.syncEndIndex = -1
		return nil, true
	}

	if len(p.batch) == 0 {
		return nil, false
	}
	out := p.batch
	p.batch = nil
	return out, false
}

func (p *EventProxy) ev(op string) {
	p.batch = append(p.batch, protocol.TerminalEvent{Op: op})
}
func (p *EventProxy) evData(op, data string) {
	p.batch = append(p.batch, protocol.TerminalEvent{Op: op, Data: data})
}
func (p *EventProxy) evParams(op string, params []int) {
	// Copy the slice — go-te reuses its internal params buffer across calls.
	cp := make([]int, len(params))
	copy(cp, params)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: op, Params: cp})
}

// Every method that modifies screen state is captured. Query/report methods
// are forwarded only (they don't change what's displayed).

func (p *EventProxy) Draw(data string) {
	p.screen.Draw(data)
	p.evData("draw", data)
}
func (p *EventProxy) Bell()            { p.screen.Bell(); p.ev("bell") }
func (p *EventProxy) Backspace()       { p.screen.Backspace(); p.ev("bs") }
func (p *EventProxy) Tab()             { p.screen.Tab(); p.ev("tab") }
func (p *EventProxy) LineFeed()        { p.screen.LineFeed(); p.ev("lf") }
func (p *EventProxy) NextLine()        { p.screen.NextLine(); p.ev("nel") }
func (p *EventProxy) CarriageReturn()  { p.screen.CarriageReturn(); p.ev("cr") }
func (p *EventProxy) ShiftOut()        { p.screen.ShiftOut(); p.ev("so") }
func (p *EventProxy) ShiftIn()         { p.screen.ShiftIn(); p.ev("si") }
func (p *EventProxy) Reset()           { p.screen.Reset(); p.ev("reset") }
func (p *EventProxy) Index()           { p.screen.Index(); p.ev("ind") }
func (p *EventProxy) ReverseIndex()    { p.screen.ReverseIndex(); p.ev("ri") }
func (p *EventProxy) SetTabStop()      { p.screen.SetTabStop(); p.ev("hts") }
func (p *EventProxy) SaveCursor()      { p.screen.SaveCursor(); p.ev("sc") }
func (p *EventProxy) RestoreCursor()   { p.screen.RestoreCursor(); p.ev("rc") }
func (p *EventProxy) SaveCursorDEC()   { p.screen.SaveCursorDEC(); p.ev("decsc") }
func (p *EventProxy) RestoreCursorDEC() { p.screen.RestoreCursorDEC(); p.ev("decrc") }
func (p *EventProxy) AlignmentDisplay() { p.screen.AlignmentDisplay(); p.ev("decaln") }
func (p *EventProxy) ForwardIndex()    { p.screen.ForwardIndex(); p.ev("fi") }
func (p *EventProxy) BackIndex()       { p.screen.BackIndex(); p.ev("bi") }
func (p *EventProxy) SoftReset()       { p.screen.SoftReset(); p.ev("decstr") }

func (p *EventProxy) StartProtectedArea() { p.screen.StartProtectedArea(); p.ev("spa") }
func (p *EventProxy) EndProtectedArea()   { p.screen.EndProtectedArea(); p.ev("epa") }

func (p *EventProxy) CursorPosition(params ...int)            { p.screen.CursorPosition(params...); p.evParams("cup", params) }
func (p *EventProxy) CursorUp(count ...int)                   { p.screen.CursorUp(count...); p.evParams("cuu", count) }
func (p *EventProxy) CursorDown(count ...int)                 { p.screen.CursorDown(count...); p.evParams("cud", count) }
func (p *EventProxy) CursorForward(count ...int)              { p.screen.CursorForward(count...); p.evParams("cuf", count) }
func (p *EventProxy) CursorBack(count ...int)                 { p.screen.CursorBack(count...); p.evParams("cub", count) }
func (p *EventProxy) CursorDown1(count ...int)                { p.screen.CursorDown1(count...); p.evParams("cud1", count) }
func (p *EventProxy) CursorUp1(count ...int)                  { p.screen.CursorUp1(count...); p.evParams("cuu1", count) }
func (p *EventProxy) CursorToColumn(column ...int)            { p.screen.CursorToColumn(column...); p.evParams("cha", column) }
func (p *EventProxy) CursorToColumnAbsolute(column ...int)    { p.screen.CursorToColumnAbsolute(column...); p.evParams("hpa", column) }
func (p *EventProxy) CursorToLine(line ...int)                { p.screen.CursorToLine(line...); p.evParams("vpa", line) }
func (p *EventProxy) CursorBackTab(count ...int)              { p.screen.CursorBackTab(count...); p.evParams("cbt", count) }
func (p *EventProxy) CursorForwardTab(count ...int)           { p.screen.CursorForwardTab(count...); p.evParams("cht", count) }
func (p *EventProxy) ScrollUp(count ...int)                   { p.screen.ScrollUp(count...); p.evParams("su", count) }
func (p *EventProxy) ScrollDown(count ...int)                 { p.screen.ScrollDown(count...); p.evParams("sd", count) }
func (p *EventProxy) InsertLines(count ...int)                { p.screen.InsertLines(count...); p.evParams("il", count) }
func (p *EventProxy) DeleteLines(count ...int)                { p.screen.DeleteLines(count...); p.evParams("dl", count) }
func (p *EventProxy) InsertCharacters(count ...int)           { p.screen.InsertCharacters(count...); p.evParams("ich", count) }
func (p *EventProxy) DeleteCharacters(count ...int)           { p.screen.DeleteCharacters(count...); p.evParams("dch", count) }
func (p *EventProxy) EraseCharacters(count ...int)            { p.screen.EraseCharacters(count...); p.evParams("ech", count) }
func (p *EventProxy) RepeatLast(count ...int)                 { p.screen.RepeatLast(count...); p.evParams("rep", count) }
func (p *EventProxy) SetMargins(params ...int)                { p.screen.SetMargins(params...); p.evParams("decstbm", params) }
func (p *EventProxy) ClearTabStop(how ...int)                 { p.screen.ClearTabStop(how...); p.evParams("tbc", how) }
func (p *EventProxy) InsertColumns(count int)                 { p.screen.InsertColumns(count); p.evParams("decic", []int{count}) }
func (p *EventProxy) DeleteColumns(count int)                 { p.screen.DeleteColumns(count); p.evParams("decdc", []int{count}) }

func (p *EventProxy) EraseInDisplay(how int, private bool, rest ...int) {
	p.screen.EraseInDisplay(how, private, rest...)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: "ed", How: how, Private: private})
}
func (p *EventProxy) EraseInLine(how int, private bool, rest ...int) {
	p.screen.EraseInLine(how, private, rest...)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: "el", How: how, Private: private})
}
func (p *EventProxy) SelectGraphicRendition(attrs []int, private bool) {
	p.screen.SelectGraphicRendition(attrs, private)
	cp := make([]int, len(attrs))
	copy(cp, attrs)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: "sgr", Attrs: cp, Private: private})
}
func (p *EventProxy) SetMode(modes []int, private bool) {
	p.screen.SetMode(modes, private)
	cp := make([]int, len(modes))
	copy(cp, modes)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: "sm", Params: cp, Private: private})
	if private {
		for _, m := range modes {
			if m == ansi.ModeSynchronizedOutput.Mode() {
				p.syncMode = true
			}
		}
	}
}
func (p *EventProxy) ResetMode(modes []int, private bool) {
	p.screen.ResetMode(modes, private)
	cp := make([]int, len(modes))
	copy(cp, modes)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: "rm", Params: cp, Private: private})
	if private {
		for _, m := range modes {
			if m == ansi.ModeSynchronizedOutput.Mode() {
				if p.syncMode {
					p.syncMode = false
					// Mark where sync ended — everything before this is
					// replaced by a snapshot, everything after is sent normally.
					p.syncEndIndex = len(p.batch)
				}
			}
		}
	}
}
func (p *EventProxy) SaveModes(modes []int)    { p.screen.SaveModes(modes); p.evParams("savem", modes) }
func (p *EventProxy) RestoreModes(modes []int) { p.screen.RestoreModes(modes); p.evParams("restm", modes) }

func (p *EventProxy) SetTitle(title string)    { p.screen.SetTitle(title); p.evData("title", title) }
func (p *EventProxy) SetIconName(param string) { p.screen.SetIconName(param); p.evData("icon", param) }
func (p *EventProxy) DefineCharset(code, mode string) {
	p.screen.DefineCharset(code, mode)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: "charset", Data: code + ":" + mode})
}
func (p *EventProxy) SetCharacterProtection(mode int) {
	p.screen.SetCharacterProtection(mode)
	p.evParams("decsca", []int{mode})
}
func (p *EventProxy) SetLeftRightMargins(left, right int) {
	p.screen.SetLeftRightMargins(left, right)
	p.evParams("declrmm", []int{left, right})
}
func (p *EventProxy) EraseRectangle(top, left, bottom, right int) {
	p.screen.EraseRectangle(top, left, bottom, right)
	p.evParams("decer", []int{top, left, bottom, right})
}
func (p *EventProxy) SelectiveEraseRectangle(top, left, bottom, right int) {
	p.screen.SelectiveEraseRectangle(top, left, bottom, right)
	p.evParams("decser", []int{top, left, bottom, right})
}
func (p *EventProxy) FillRectangle(ch rune, top, left, bottom, right int) {
	p.screen.FillRectangle(ch, top, left, bottom, right)
	p.batch = append(p.batch, protocol.TerminalEvent{Op: "decfra", Data: string(ch), Params: []int{top, left, bottom, right}})
}
func (p *EventProxy) CopyRectangle(srcTop, srcLeft, srcBottom, srcRight, dstTop, dstLeft int) {
	p.screen.CopyRectangle(srcTop, srcLeft, srcBottom, srcRight, dstTop, dstLeft)
	p.evParams("deccra", []int{srcTop, srcLeft, srcBottom, srcRight, dstTop, dstLeft})
}
func (p *EventProxy) SetTitleMode(params []int, reset bool) {
	p.screen.SetTitleMode(params, reset)
	p.evParams("titlemode", params)
}
func (p *EventProxy) SetConformance(level int, sevenBit int) {
	p.screen.SetConformance(level, sevenBit)
	p.evParams("decscl", []int{level, sevenBit})
}
func (p *EventProxy) WindowOp(params []int) { p.screen.WindowOp(params); p.evParams("winop", params) }

// SetCursorStyle and SetActiveStatusDisplay are not part of EventHandler.
// They exist on *te.Screen only, so we type-assert to reach them.
type cursorStyler interface{ SetCursorStyle(int) }
type statusDisplayer interface{ SetActiveStatusDisplay(int) }

func (p *EventProxy) SetCursorStyle(style int) {
	if s, ok := p.screen.(cursorStyler); ok {
		s.SetCursorStyle(style)
	}
	p.evParams("decscusr", []int{style})
}
func (p *EventProxy) SetActiveStatusDisplay(mode int) {
	if s, ok := p.screen.(statusDisplayer); ok {
		s.SetActiveStatusDisplay(mode)
	}
	p.evParams("decsasd", []int{mode})
}

func (p *EventProxy) SetColor(index int, value string)        { p.screen.SetColor(index, value) }
func (p *EventProxy) SetDynamicColor(index int, value string)  { p.screen.SetDynamicColor(index, value) }
func (p *EventProxy) SetSpecialColor(index int, value string)  { p.screen.SetSpecialColor(index, value) }
func (p *EventProxy) SetSelectionData(selection, data string)  { p.screen.SetSelectionData(selection, data) }

// Query/report methods — forwarded only, no event capture
func (p *EventProxy) ReportDeviceAttributes(mode int, private bool, prefix rune, rest ...int) {
	p.screen.ReportDeviceAttributes(mode, private, prefix, rest...)
}
func (p *EventProxy) ReportDeviceStatus(mode int, private bool, prefix rune, rest ...int) {
	p.screen.ReportDeviceStatus(mode, private, prefix, rest...)
}
func (p *EventProxy) ReportMode(mode int, private bool)   { p.screen.ReportMode(mode, private) }
func (p *EventProxy) RequestStatusString(query string)     { p.screen.RequestStatusString(query) }
func (p *EventProxy) QuerySelectionData(selection string)  { p.screen.QuerySelectionData(selection) }
func (p *EventProxy) QueryColor(index int)                 { p.screen.QueryColor(index) }
func (p *EventProxy) QueryDynamicColor(index int)          { p.screen.QueryDynamicColor(index) }
func (p *EventProxy) QuerySpecialColor(index int)          { p.screen.QuerySpecialColor(index) }
func (p *EventProxy) ResetColor(index int, all bool)       { p.screen.ResetColor(index, all) }
func (p *EventProxy) ResetSpecialColor(index int, all bool) { p.screen.ResetSpecialColor(index, all) }
func (p *EventProxy) ResetDynamicColor(index int, all bool) { p.screen.ResetDynamicColor(index, all) }
func (p *EventProxy) Debug(params ...any)                   { p.screen.Debug(params...) }
