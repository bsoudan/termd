package te

import (
	"encoding/json"
	"testing"
)

// TestScreenStateRoundTrip creates a screen with complex state, marshals it,
// unmarshals into a fresh screen, and verifies all fields match.
func TestScreenStateRoundTrip(t *testing.T) {
	s := NewScreen(80, 24)
	stream := NewStream(s, false)

	// Draw some content.
	stream.FeedBytes([]byte("Hello, world!"))

	// Set cursor position.
	stream.FeedBytes([]byte("\x1b[5;10H")) // row 5, col 10

	// Set colors: red foreground.
	stream.FeedBytes([]byte("\x1b[31mColored"))

	// Set title.
	stream.FeedBytes([]byte("\x1b]0;My Title\x1b\\"))

	// Set tab stop at current column, then move and set another.
	stream.FeedBytes([]byte("\x1bH"))       // set tab stop
	stream.FeedBytes([]byte("\x1b[1;20H"))  // move to col 20
	stream.FeedBytes([]byte("\x1bH"))       // set another tab stop

	// Set margins.
	stream.FeedBytes([]byte("\x1b[5;20r")) // top=5, bottom=20

	// Save cursor.
	stream.FeedBytes([]byte("\x1b7"))

	// Switch to alt buffer.
	stream.FeedBytes([]byte("\x1b[?1049h"))
	stream.FeedBytes([]byte("Alt screen content"))

	// Marshal.
	state := s.MarshalState()

	// Serialize to JSON and back to verify JSON round-trip.
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	t.Logf("JSON size: %d bytes", len(data))

	var state2 ScreenState
	if err := json.Unmarshal(data, &state2); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Restore into a fresh screen.
	s2 := &Screen{}
	s2.UnmarshalState(&state2)

	// Verify dimensions.
	if s2.Columns != s.Columns || s2.Lines != s.Lines {
		t.Errorf("dimensions: got %dx%d, want %dx%d", s2.Columns, s2.Lines, s.Columns, s.Lines)
	}

	// Verify cursor.
	if s2.Cursor.Row != s.Cursor.Row || s2.Cursor.Col != s.Cursor.Col {
		t.Errorf("cursor: got (%d,%d), want (%d,%d)", s2.Cursor.Row, s2.Cursor.Col, s.Cursor.Row, s.Cursor.Col)
	}

	// Verify buffer content matches.
	for row := range s.Lines {
		for col := range s.Columns {
			if col < len(s.Buffer[row]) && col < len(s2.Buffer[row]) {
				if s.Buffer[row][col].Data != s2.Buffer[row][col].Data {
					t.Errorf("buffer[%d][%d]: got %q, want %q",
						row, col, s2.Buffer[row][col].Data, s.Buffer[row][col].Data)
				}
			}
		}
	}

	// Verify alt buffer exists.
	if s.altActive != s2.altActive {
		t.Errorf("altActive: got %v, want %v", s2.altActive, s.altActive)
	}

	// Verify title.
	if s2.Title != s.Title {
		t.Errorf("title: got %q, want %q", s2.Title, s.Title)
	}

	// Verify modes match.
	for k := range s.Mode {
		if _, ok := s2.Mode[k]; !ok {
			t.Errorf("mode %d missing after unmarshal", k)
		}
	}

	// Verify margins.
	if s.Margins != nil && s2.Margins != nil {
		if s.Margins.Top != s2.Margins.Top || s.Margins.Bottom != s2.Margins.Bottom {
			t.Errorf("margins: got %+v, want %+v", s2.Margins, s.Margins)
		}
	} else if (s.Margins == nil) != (s2.Margins == nil) {
		t.Errorf("margins nil mismatch: got %v, want %v", s2.Margins, s.Margins)
	}

	// Verify tab stops.
	for k := range s.TabStops {
		if _, ok := s2.TabStops[k]; !ok {
			t.Errorf("tab stop %d missing after unmarshal", k)
		}
	}

	// Verify savepoints.
	if len(s2.Savepoints) != len(s.Savepoints) {
		t.Errorf("savepoints: got %d, want %d", len(s2.Savepoints), len(s.Savepoints))
	}

	// Verify all rows are dirty after unmarshal.
	if len(s2.Dirty) != s2.Lines {
		t.Errorf("dirty rows: got %d, want %d (all dirty)", len(s2.Dirty), s2.Lines)
	}
}

// TestHistoryStateRoundTrip tests serialization of HistoryScreen including scrollback.
func TestHistoryStateRoundTrip(t *testing.T) {
	h := NewHistoryScreen(80, 5, 100) // small screen to force scrollback
	stream := NewStream(h, false)

	// Fill screen and generate scrollback by writing many lines.
	for i := range 20 {
		stream.FeedBytes([]byte("Line " + string(rune('A'+i)) + "\r\n"))
	}

	scrollback := h.History()
	if len(scrollback) == 0 {
		t.Fatal("expected scrollback history after 20 lines on 5-row screen")
	}
	t.Logf("scrollback lines: %d", len(scrollback))

	// Marshal.
	state := h.MarshalState()

	// JSON round-trip.
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	t.Logf("JSON size: %d bytes", len(data))

	var state2 HistoryState
	if err := json.Unmarshal(data, &state2); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Restore.
	h2 := &HistoryScreen{}
	h2.UnmarshalState(&state2)

	// Verify dimensions.
	if h2.Columns != h.Columns || h2.Lines != h.Lines {
		t.Errorf("dimensions: got %dx%d, want %dx%d", h2.Columns, h2.Lines, h.Columns, h.Lines)
	}

	// Verify scrollback preserved.
	scrollback2 := h2.History()
	if len(scrollback2) != len(scrollback) {
		t.Errorf("scrollback: got %d lines, want %d", len(scrollback2), len(scrollback))
	}

	// Verify scrollback content matches.
	for i := range min(len(scrollback), len(scrollback2)) {
		orig := cellsToString(scrollback[i])
		restored := cellsToString(scrollback2[i])
		if orig != restored {
			t.Errorf("scrollback[%d]: got %q, want %q", i, restored, orig)
		}
	}

	// Verify visible buffer matches.
	for row := range h.Lines {
		orig := cellsToString(h.Buffer[row])
		restored := cellsToString(h2.Buffer[row])
		if orig != restored {
			t.Errorf("buffer[%d]: got %q, want %q", row, restored, orig)
		}
	}

	// Verify history metadata.
	if h2.history.Size != h.history.Size {
		t.Errorf("history size: got %d, want %d", h2.history.Size, h.history.Size)
	}
	if h2.history.Ratio != h.history.Ratio {
		t.Errorf("history ratio: got %f, want %f", h2.history.Ratio, h.history.Ratio)
	}
}

func cellsToString(cells []Cell) string {
	var s string
	for _, c := range cells {
		s += c.Data
	}
	return s
}
