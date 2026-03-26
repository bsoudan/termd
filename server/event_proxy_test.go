package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	te "github.com/rcarmo/go-te/pkg/te"
	"termd/frontend/protocol"
)

// replayEvents applies terminal events to a screen (mirrors frontend/ui/model.go)
func replayEvents(screen *te.Screen, events []protocol.TerminalEvent) {
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
			if i := strings.Index(ev.Data, ":"); i >= 0 {
				screen.DefineCharset(ev.Data[:i], ev.Data[i+1:])
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
		case "rm":
			screen.ResetMode(ev.Params, ev.Private)
		}
	}
}

// roundTripEvents serializes events to JSON and back, simulating the network.
func roundTripEvents(events []protocol.TerminalEvent) []protocol.TerminalEvent {
	msg := protocol.TerminalEvents{
		Type:     "terminal_events",
		RegionID: "test",
		Events:   events,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	var out protocol.TerminalEvents
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out.Events
}

func TestEventProxyReplay(t *testing.T) {
	// Simulate top-like behavior: home, erase, draw lines, repeat
	const cols, rows = 80, 24

	// "Server" screen with proxy
	serverScreen := te.NewScreen(cols, rows)
	proxy := NewEventProxy(serverScreen)
	stream := te.NewStream(proxy, false)

	// Simulate: prompt, then top-like redraw cycle
	input := ""
	input += "termd$ " // Draw prompt
	input += "top\r\n" // Type "top" + enter

	// Top startup: home, clear, draw header
	input += "\x1b[?1h"   // DECCKM
	input += "\x1b[?25l"  // Hide cursor
	input += "\x1b[H"     // Cursor home
	input += "\x1b[2J"    // Erase display

	// Draw top's display
	for row := range rows {
		input += fmt.Sprintf("\x1b[%d;1H", row+1) // Position cursor
		input += fmt.Sprintf("top line %02d", row) // Draw content
		input += "\x1b[K"                          // Erase to end of line
	}

	// Simulate a second refresh cycle (top refreshes)
	input += "\x1b[H" // Home
	for row := range rows {
		input += fmt.Sprintf("\x1b[%d;1H", row+1)
		input += fmt.Sprintf("top refresh %02d", row)
		input += "\x1b[K"
	}

	// Top exits: move to bottom, show cursor, reset DECCKM
	input += fmt.Sprintf("\x1b[%d;1H", rows) // Move to last row
	input += "\x1b[K"                         // Clear last line
	input += "\x1b[?25h"                      // Show cursor
	input += "\x1b[?1l"                       // Reset DECCKM

	// Bash prompt after top exits
	input += "\r\n"
	input += "termd$ "

	stream.FeedBytes([]byte(input))
	allEvents := proxy.Flush()

	t.Logf("total events captured: %d", len(allEvents))

	// Round-trip through JSON (simulates network)
	allEvents = roundTripEvents(allEvents)

	// "Frontend" screen: replay events
	frontendScreen := te.NewScreen(cols, rows)
	replayEvents(frontendScreen, allEvents)

	// Compare
	serverDisplay := serverScreen.Display()
	frontendDisplay := frontendScreen.Display()

	mismatches := 0
	for i := range serverDisplay {
		if i >= len(frontendDisplay) {
			break
		}
		sLine := strings.TrimRight(serverDisplay[i], " ")
		fLine := strings.TrimRight(frontendDisplay[i], " ")
		if sLine != fLine {
			t.Logf("row %d mismatch:\n  server:   %q\n  frontend: %q", i, sLine, fLine)
			mismatches++
		}
	}

	t.Logf("server cursor: (%d,%d), frontend cursor: (%d,%d)",
		serverScreen.Cursor.Row, serverScreen.Cursor.Col,
		frontendScreen.Cursor.Row, frontendScreen.Cursor.Col)

	if serverScreen.Cursor.Row != frontendScreen.Cursor.Row ||
		serverScreen.Cursor.Col != frontendScreen.Cursor.Col {
		t.Errorf("cursor mismatch: server=(%d,%d) frontend=(%d,%d)",
			serverScreen.Cursor.Row, serverScreen.Cursor.Col,
			frontendScreen.Cursor.Row, frontendScreen.Cursor.Col)
	}

	if mismatches > 0 {
		t.Errorf("%d rows differ", mismatches)
	}
}

func TestEventProxyReplayWithAltScreen(t *testing.T) {
	const cols, rows = 80, 24

	serverScreen := te.NewScreen(cols, rows)
	proxy := NewEventProxy(serverScreen)
	stream := te.NewStream(proxy, false)

	input := ""
	input += "termd$ " // Initial prompt

	// Enter alt screen
	input += "\x1b[?1049h"
	input += "\x1b[H\x1b[2J" // Home + clear

	// Draw on alt screen
	for row := 0; row < rows; row++ {
		input += fmt.Sprintf("\x1b[%d;1H", row+1)
		input += fmt.Sprintf("alt line %02d", row)
		input += "\x1b[K"
	}

	// Exit alt screen
	input += "\x1b[?1049l"

	// New prompt
	input += "termd$ "

	stream.FeedBytes([]byte(input))
	allEvents := proxy.Flush()

	t.Logf("total events: %d", len(allEvents))

	allEvents = roundTripEvents(allEvents)

	frontendScreen := te.NewScreen(cols, rows)
	replayEvents(frontendScreen, allEvents)

	serverDisplay := serverScreen.Display()
	frontendDisplay := frontendScreen.Display()

	mismatches := 0
	for i := 0; i < len(serverDisplay) && i < len(frontendDisplay); i++ {
		sLine := strings.TrimRight(serverDisplay[i], " ")
		fLine := strings.TrimRight(frontendDisplay[i], " ")
		if sLine != fLine {
			t.Logf("row %d mismatch:\n  server:   %q\n  frontend: %q", i, sLine, fLine)
			mismatches++
		}
	}

	t.Logf("server cursor: (%d,%d), frontend cursor: (%d,%d)",
		serverScreen.Cursor.Row, serverScreen.Cursor.Col,
		frontendScreen.Cursor.Row, frontendScreen.Cursor.Col)

	if serverScreen.Cursor.Row != frontendScreen.Cursor.Row ||
		serverScreen.Cursor.Col != frontendScreen.Cursor.Col {
		t.Errorf("cursor mismatch: server=(%d,%d) frontend=(%d,%d)",
			serverScreen.Cursor.Row, serverScreen.Cursor.Col,
			frontendScreen.Cursor.Row, frontendScreen.Cursor.Col)
	}

	if mismatches > 0 {
		t.Errorf("%d rows differ", mismatches)
	}
}

func TestEventProxyReplayColors(t *testing.T) {
	const cols, rows = 80, 24

	serverScreen := te.NewScreen(cols, rows)
	proxy := NewEventProxy(serverScreen)
	stream := te.NewStream(proxy, false)

	// SGR 31 = red fg, then draw "RED", then SGR 0 = reset, then " ", SGR 32 = green, draw "GRN"
	input := "\x1b[31mRED\x1b[0m \x1b[32mGRN\x1b[0m \x1b[1mBLD\x1b[0m"
	stream.FeedBytes([]byte(input))

	// Check server screen cells have correct colors
	serverCells := serverScreen.LinesCells()
	t.Logf("server cell[0][0]: Data=%q Fg=%+v Bold=%v", serverCells[0][0].Data, serverCells[0][0].Attr.Fg, serverCells[0][0].Attr.Bold)
	t.Logf("server cell[0][4]: Data=%q Fg=%+v Bold=%v", serverCells[0][4].Data, serverCells[0][4].Attr.Fg, serverCells[0][4].Attr.Bold)
	t.Logf("server cell[0][8]: Data=%q Fg=%+v Bold=%v", serverCells[0][8].Data, serverCells[0][8].Attr.Fg, serverCells[0][8].Attr.Bold)

	if serverCells[0][0].Attr.Fg.Name != "red" {
		t.Fatalf("server: expected 'R' to have red fg, got %+v", serverCells[0][0].Attr.Fg)
	}

	// Flush and replay
	allEvents := proxy.Flush()
	allEvents = roundTripEvents(allEvents)

	frontendScreen := te.NewScreen(cols, rows)
	replayEvents(frontendScreen, allEvents)

	frontendCells := frontendScreen.LinesCells()
	t.Logf("frontend cell[0][0]: Data=%q Fg=%+v Bold=%v", frontendCells[0][0].Data, frontendCells[0][0].Attr.Fg, frontendCells[0][0].Attr.Bold)
	t.Logf("frontend cell[0][4]: Data=%q Fg=%+v Bold=%v", frontendCells[0][4].Data, frontendCells[0][4].Attr.Fg, frontendCells[0][4].Attr.Bold)
	t.Logf("frontend cell[0][8]: Data=%q Fg=%+v Bold=%v", frontendCells[0][8].Data, frontendCells[0][8].Attr.Fg, frontendCells[0][8].Attr.Bold)

	// Verify colors match
	if frontendCells[0][0].Attr.Fg.Name != "red" {
		t.Errorf("frontend: 'R' expected red fg, got %+v", frontendCells[0][0].Attr.Fg)
	}
	if frontendCells[0][4].Attr.Fg.Name != "green" {
		t.Errorf("frontend: 'G' expected green fg, got %+v", frontendCells[0][4].Attr.Fg)
	}
	if !frontendCells[0][8].Attr.Bold {
		t.Errorf("frontend: 'B' expected bold, got %+v", frontendCells[0][8].Attr)
	}
}

func TestEventProxyJSONRoundTrip(t *testing.T) {
	// Test that events with zero-value fields survive JSON round-trip
	events := []protocol.TerminalEvent{
		{Op: "ed", How: 0, Private: false},    // EraseInDisplay(0, false) - common
		{Op: "el", How: 0, Private: false},    // EraseInLine(0, false) - common
		{Op: "cup"},                            // CursorPosition() - no params = home
		{Op: "sgr", Attrs: []int{0}},          // Reset attributes
		{Op: "sm", Params: []int{1049}, Private: true}, // Enter alt screen
		{Op: "rm", Params: []int{1049}, Private: true}, // Exit alt screen
		{Op: "sm", Params: []int{25}, Private: true},   // Show cursor (private=true)
		{Op: "rm", Params: []int{25}, Private: true},   // Hide cursor (private=true)
	}

	roundTripped := roundTripEvents(events)

	for i, orig := range events {
		rt := roundTripped[i]
		if orig.Op != rt.Op {
			t.Errorf("event %d: Op %q != %q", i, orig.Op, rt.Op)
		}
		if orig.How != rt.How {
			t.Errorf("event %d (%s): How %d != %d", i, orig.Op, orig.How, rt.How)
		}
		if orig.Private != rt.Private {
			t.Errorf("event %d (%s): Private %v != %v", i, orig.Op, orig.Private, rt.Private)
		}
		origJSON, _ := json.Marshal(orig)
		rtJSON, _ := json.Marshal(rt)
		t.Logf("event %d: %s -> %s", i, origJSON, rtJSON)
	}
}
