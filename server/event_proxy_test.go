package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	te "github.com/rcarmo/go-te/pkg/te"
	"termd/frontend/protocol"
	"termd/frontend/terminal"
)

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
	input += ansi.SetModeCursorKeys   // DECCKM
	input += ansi.HideCursor          // Hide cursor
	input += ansi.CursorHomePosition  // Cursor home
	input += ansi.EraseDisplay(2)     // Erase display

	// Draw top's display
	for row := range rows {
		input += ansi.CursorPosition(1, row+1) // Position cursor
		input += fmt.Sprintf("top line %02d", row)
		input += ansi.EraseLine(0)              // Erase to end of line
	}

	// Simulate a second refresh cycle (top refreshes)
	input += ansi.CursorHomePosition
	for row := range rows {
		input += ansi.CursorPosition(1, row+1)
		input += fmt.Sprintf("top refresh %02d", row)
		input += ansi.EraseLine(0)
	}

	// Top exits: move to bottom, show cursor, reset DECCKM
	input += ansi.CursorPosition(1, rows) // Move to last row
	input += ansi.EraseLine(0)
	input += ansi.ShowCursor
	input += ansi.ResetModeCursorKeys

	// Bash prompt after top exits
	input += "\r\n"
	input += "termd$ "

	stream.FeedBytes([]byte(input))
	allEvents, _ := proxy.Flush()

	t.Logf("total events captured: %d", len(allEvents))

	// Round-trip through JSON (simulates network)
	allEvents = roundTripEvents(allEvents)

	// "Frontend" screen: replay events
	frontendScreen := te.NewScreen(cols, rows)
	terminal.ReplayEvents(frontendScreen, allEvents)

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
	input += ansi.SetModeAltScreenSaveCursor
	input += ansi.CursorHomePosition + ansi.EraseDisplay(2)

	// Draw on alt screen
	for row := range rows {
		input += ansi.CursorPosition(1, row+1)
		input += fmt.Sprintf("alt line %02d", row)
		input += ansi.EraseLine(0)
	}

	// Exit alt screen
	input += ansi.ResetModeAltScreenSaveCursor

	// New prompt
	input += "termd$ "

	stream.FeedBytes([]byte(input))
	allEvents, _ := proxy.Flush()

	t.Logf("total events: %d", len(allEvents))

	allEvents = roundTripEvents(allEvents)

	frontendScreen := te.NewScreen(cols, rows)
	terminal.ReplayEvents(frontendScreen, allEvents)

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

	// ANSI16 red, green, bold, 256-color orange (208), true-color
	input := ansi.SGR(ansi.AttrRedForegroundColor) + "RED" + ansi.ResetStyle + " " +
		ansi.SGR(ansi.AttrGreenForegroundColor) + "GRN" + ansi.ResetStyle + " " +
		ansi.SGR(ansi.AttrBold) + "BLD" + ansi.ResetStyle + " " +
		ansi.SGR(ansi.AttrExtendedForegroundColor, 5, 208) + "IDX" + ansi.ResetStyle + " " +
		ansi.SGR(ansi.AttrExtendedForegroundColor, 2, 255, 128, 0) + "RGB" + ansi.ResetStyle
	stream.FeedBytes([]byte(input))

	allEvents, _ := proxy.Flush()
	allEvents = roundTripEvents(allEvents)

	frontendScreen := te.NewScreen(cols, rows)
	terminal.ReplayEvents(frontendScreen, allEvents)

	fc := frontendScreen.LinesCells()[0]

	// RED at col 0: ANSI16 red
	if fc[0].Attr.Fg.Name != "red" {
		t.Errorf("'R' expected red fg, got %+v", fc[0].Attr.Fg)
	}
	// GRN at col 4: ANSI16 green
	if fc[4].Attr.Fg.Name != "green" {
		t.Errorf("'G' expected green fg, got %+v", fc[4].Attr.Fg)
	}
	// BLD at col 8: bold
	if !fc[8].Attr.Bold {
		t.Errorf("'B' expected bold, got %+v", fc[8].Attr)
	}
	// IDX at col 12: 256-color index 208
	if fc[12].Attr.Fg.Mode != te.ColorANSI256 || fc[12].Attr.Fg.Index != 208 {
		t.Errorf("'I' expected ANSI256 index 208, got %+v", fc[12].Attr.Fg)
	}
	// RGB at col 16: true color
	if fc[16].Attr.Fg.Mode != te.ColorTrueColor {
		t.Errorf("'R' expected TrueColor, got %+v", fc[16].Attr.Fg)
	}
}

func TestSyncOutputSnapshot(t *testing.T) {
	const cols, rows = 80, 24

	serverScreen := te.NewScreen(cols, rows)
	proxy := NewEventProxy(serverScreen)
	stream := te.NewStream(proxy, false)

	// Draw initial content before sync
	stream.FeedBytes([]byte("before"))

	// Flush pre-sync events — should return normally
	preEvents, needsSnap := proxy.Flush()
	if needsSnap {
		t.Fatal("expected no snapshot before sync")
	}
	if len(preEvents) == 0 {
		t.Fatal("expected pre-sync events")
	}

	// Enter sync mode, draw content, exit sync mode, draw trailing content
	stream.FeedBytes([]byte(
		ansi.SetModeSynchronizedOutput + // begin synchronized output
			ansi.CursorHomePosition + ansi.EraseDisplay(2) + // home + clear (inside sync)
			"synced content" + // draw inside sync
			ansi.ResetModeSynchronizedOutput + // end synchronized output
			"trailing", // draw AFTER sync
	))

	// Flush should return needsSnapshot=true, no events (snapshot captures everything)
	events, needsSnap := proxy.Flush()
	if !needsSnap {
		t.Fatal("expected needsSnapshot after sync output")
	}
	if len(events) != 0 {
		t.Errorf("expected no events (snapshot captures all), got %d", len(events))
	}

	// The server screen should have both synced and trailing content
	display := serverScreen.Display()
	if !strings.Contains(display[0], "synced content") {
		t.Errorf("server screen should contain 'synced content', got %q", display[0])
	}
	if !strings.Contains(display[0], "trailing") {
		t.Errorf("server screen should contain 'trailing', got %q", display[0])
	}

	// A snapshot taken now captures the full state including trailing content.
	// The frontend would receive this as a screen_update and render it atomically.
	frontendScreen := te.NewScreen(cols, rows)
	copy(frontendScreen.Buffer[0], serverScreen.Buffer[0])

	frontendLines := frontendScreen.Display()
	if !strings.Contains(frontendLines[0], "synced contenttrailing") {
		t.Errorf("frontend should show full content, got %q", frontendLines[0])
	}
}

func TestSyncOutputHoldsDuringSync(t *testing.T) {
	const cols, rows = 80, 24

	serverScreen := te.NewScreen(cols, rows)
	proxy := NewEventProxy(serverScreen)
	stream := te.NewStream(proxy, false)

	// Enter sync mode and draw
	stream.FeedBytes([]byte(ansi.SetModeSynchronizedOutput + "inside sync"))

	// Flush while sync is active — should return nothing
	events, needsSnap := proxy.Flush()
	if needsSnap {
		t.Fatal("should not need snapshot while still in sync mode")
	}
	if events != nil {
		t.Fatalf("should return nil events during sync, got %d", len(events))
	}

	// End sync with no trailing content
	stream.FeedBytes([]byte(ansi.ResetModeSynchronizedOutput))

	events, needsSnap = proxy.Flush()
	if !needsSnap {
		t.Fatal("expected snapshot after sync end")
	}
	if len(events) != 0 {
		t.Errorf("expected no trailing events, got %d", len(events))
	}
}

