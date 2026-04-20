package te

import (
	"strings"
	"testing"
)

func historyChars(lines [][]Cell, columns int) []string {
	out := make([]string, len(lines))
	for i := range lines {
		var b strings.Builder
		for x := 0; x < columns; x++ {
			b.WriteString(lines[i][x].Data)
		}
		out[i] = b.String()
	}
	return out
}

func historyLineString(line []Cell) string {
	var b strings.Builder
	for _, cell := range line {
		b.WriteString(cell.Data)
	}
	return b.String()
}

// From pyte/tests/test_screen.py::test_index
func TestHistoryIndex(t *testing.T) {
	screen := NewHistoryScreen(5, 5, 50)
	for idx := 0; idx < screen.Lines; idx++ {
		screen.Draw(string(rune('0' + idx)))
		if idx != screen.Lines-1 {
			screen.LineFeed()
		}
	}
	line := screen.Buffer[0]
	screen.Index()
	if len(screen.history.Top.items) == 0 {
		t.Fatalf("expected top history updated")
	}
	if historyLineString(screen.history.Top.items[len(screen.history.Top.items)-1]) != historyLineString(line) {
		t.Fatalf("expected top history line match")
	}
}

// From pyte/tests/test_screen.py::test_reverse_index
func TestHistoryReverseIndex(t *testing.T) {
	screen := NewHistoryScreen(5, 5, 50)
	for idx := 0; idx < screen.Lines; idx++ {
		screen.Draw(string(rune('0' + idx)))
		if idx != screen.Lines-1 {
			screen.LineFeed()
		}
	}
	screen.CursorPosition(0, 0)
	line := screen.Buffer[screen.Lines-1]
	screen.ReverseIndex()
	if len(screen.history.Bottom.items) == 0 {
		t.Fatalf("expected bottom history updated")
	}
	if historyLineString(screen.history.Bottom.items[0]) != historyLineString(line) {
		t.Fatalf("expected bottom history line match")
	}
}

func TestHistoryPrevNextPage(t *testing.T) {
	screen := NewHistoryScreen(4, 4, 40)
	screen.SetMode([]int{ModeLNM}, false)
	for idx := 0; idx < screen.Lines*10; idx++ {
		screen.Draw(string(rune('0' + idx%10)))
		screen.LineFeed()
	}
	if screen.history.Position != 40 {
		t.Fatalf("expected position 40")
	}
	screen.PrevPage()
	if screen.history.Position >= 40 {
		t.Fatalf("expected position decreased")
	}
	screen.NextPage()
	if screen.history.Position != 40 {
		t.Fatalf("expected position restored")
	}
}

func TestHistoryCursorHidden(t *testing.T) {
	screen := NewHistoryScreen(5, 5, 50)
	for idx := 0; idx < screen.Lines*5; idx++ {
		screen.Draw(string(rune('0' + idx%10)))
		screen.LineFeed()
	}
	if screen.Cursor.Hidden {
		t.Fatalf("expected cursor visible")
	}
	screen.PrevPage()
	if !screen.Cursor.Hidden {
		t.Fatalf("expected cursor hidden")
	}
	screen.NextPage()
	if screen.Cursor.Hidden {
		t.Fatalf("expected cursor visible")
	}
}

func checkSeqInvariant(t *testing.T, screen *HistoryScreen, desc string) {
	t.Helper()
	first := screen.FirstSeq()
	total := screen.TotalAdded()
	sb := screen.Scrollback()
	// Top covers exactly len(items) rows starting at FirstSeq. The
	// newest retained row's seq is FirstSeq + Scrollback - 1, which
	// must not exceed TotalAdded - 1. Empty Top → FirstSeq == TotalAdded.
	if first > total {
		t.Fatalf("%s: FirstSeq(%d) > TotalAdded(%d)", desc, first, total)
	}
	if first+uint64(sb) > total {
		t.Fatalf("%s: FirstSeq(%d) + Scrollback(%d) > TotalAdded(%d)",
			desc, first, sb, total)
	}
	if sb == 0 && first != total {
		t.Fatalf("%s: empty Top but FirstSeq(%d) != TotalAdded(%d)",
			desc, first, total)
	}
}

func TestHistoryFirstSeqLineFeed(t *testing.T) {
	// 3-row screen, 5-row scrollback cap. 20 LineFeeds — the first 2
	// move the cursor from row 0 to the bottom without scrolling, the
	// remaining 18 each scroll one row off the top.
	screen := NewHistoryScreen(5, 3, 5)
	screen.SetMode([]int{ModeLNM}, false)
	for range 20 {
		screen.Draw("X")
		screen.LineFeed()
		checkSeqInvariant(t, screen, "after linefeed")
	}
	if screen.TotalAdded() != 18 {
		t.Fatalf("TotalAdded=%d, want 18", screen.TotalAdded())
	}
	if screen.Scrollback() != 5 {
		t.Fatalf("Scrollback=%d, want 5", screen.Scrollback())
	}
	if screen.FirstSeq() != 13 {
		t.Fatalf("FirstSeq=%d, want 13 (18 total - 5 retained)", screen.FirstSeq())
	}
}

func TestHistoryFirstSeqResetAndAdopt(t *testing.T) {
	// Client-behind rebuild: after ResetHistory + SetTotalAdded, the
	// seq space is empty with FirstSeq at the new upper bound.
	screen := NewHistoryScreen(5, 3, 500)
	screen.SetMode([]int{ModeLNM}, false)
	// Build some state first so ResetHistory has something to clear.
	for range 5 {
		screen.Draw("E")
		screen.LineFeed()
	}
	screen.ResetHistory()
	checkSeqInvariant(t, screen, "after reset")
	screen.SetTotalAdded(1000)
	checkSeqInvariant(t, screen, "after SetTotalAdded")
	if screen.FirstSeq() != 1000 {
		t.Fatalf("FirstSeq=%d after SetTotalAdded(1000) on empty Top, want 1000",
			screen.FirstSeq())
	}
	// Prepend the server's authoritative range (200 rows, fits in cap).
	rows := make([][]Cell, 200)
	for i := range rows {
		rows[i] = []Cell{{Data: "S"}}
	}
	screen.PrependHistory(rows)
	checkSeqInvariant(t, screen, "after authoritative prepend")
	if screen.FirstSeq() != 800 {
		t.Fatalf("FirstSeq=%d after prepending 200 rows, want 800",
			screen.FirstSeq())
	}
}

func TestHistoryFirstSeqPrependCapped(t *testing.T) {
	// When a prepend would exceed the deque cap, only the newest rows
	// (closest to the existing range) are kept. FirstSeq decreases by
	// exactly the count kept.
	screen := NewHistoryScreen(5, 3, 100)
	screen.ResetHistory()
	screen.SetTotalAdded(1000)
	rows := make([][]Cell, 200)
	for i := range rows {
		rows[i] = []Cell{{Data: "S"}}
	}
	screen.PrependHistory(rows)
	checkSeqInvariant(t, screen, "prepend over cap")
	if screen.Scrollback() != 100 {
		t.Fatalf("Scrollback=%d, want 100 (cap)", screen.Scrollback())
	}
	if screen.FirstSeq() != 900 {
		t.Fatalf("FirstSeq=%d, want 900 (only 100 of 200 fit)", screen.FirstSeq())
	}
}

func TestHistoryFirstSeqSetTotalAddedPreservesRetained(t *testing.T) {
	// When client has retained rows and later adopts a jumped
	// TotalAdded (mode-2026 snapshot), the retained rows keep their
	// original seqs: FirstSeq is NOT moved. The "newer" range
	// [FirstSeq+Scrollback, TotalAdded) forms a gap that subsequent
	// sync paths can fill.
	screen := NewHistoryScreen(5, 3, 100)
	screen.SetMode([]int{ModeLNM}, false)
	for range 5 {
		screen.Draw("E")
		screen.LineFeed()
	}
	// 5 LineFeeds from row 0 in a 3-row screen: 2 to reach bottom, 3 scroll.
	if screen.FirstSeq() != 0 || screen.TotalAdded() != 3 || screen.Scrollback() != 3 {
		t.Fatalf("unexpected pre-adopt state: FirstSeq=%d TotalAdded=%d Scrollback=%d",
			screen.FirstSeq(), screen.TotalAdded(), screen.Scrollback())
	}
	screen.SetTotalAdded(100)
	if screen.FirstSeq() != 0 {
		t.Fatalf("FirstSeq moved from 0 to %d on SetTotalAdded with non-empty Top",
			screen.FirstSeq())
	}
	if screen.Scrollback() != 3 {
		t.Fatalf("Scrollback changed: %d", screen.Scrollback())
	}
}

// From pyte/tests/test_screen.py::test_erase_in_display
func TestHistoryEraseInDisplay(t *testing.T) {
	screen := NewHistoryScreen(5, 5, 6)
	screen.SetMode([]int{ModeLNM}, false)
	for idx := 0; idx < screen.Lines; idx++ {
		screen.Draw(string(rune('0' + idx)))
		screen.LineFeed()
	}
	screen.PrevPage()
	screen.EraseInDisplay(3, false)
	if len(screen.history.Top.items) != 0 || len(screen.history.Bottom.items) != 0 {
		t.Fatalf("expected history reset")
	}
}
