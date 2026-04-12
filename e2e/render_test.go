package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"nxtermd/internal/nxtest"
	"nxtermd/pkg/te"
)

func TestStartAndRender(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	// Wait for the shell prompt to render below the tab bar.
	nxt.WaitFor("$", 10*time.Second)

	// Verify the tab bar (row 0) shows tab "1" near the start. The
	// active tab is rendered as " 1 " between bullets ("• 1 •..."),
	// so "1" should land within the first ~5 columns.
	lines := nxt.ScreenLines()
	row, col := nxtest.FindOnScreen(lines, "1")
	if row != 0 {
		t.Fatalf("expected tab '1' on row 0, found on row %d", row)
	}
	if col > 5 {
		t.Fatalf("expected tab '1' near start of row 0, found at col %d", col)
	}
}

func TestCursorPosition(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	nxt.Write([]byte("xy"))

	lines := nxt.WaitForScreen(func(lines []string) bool {
		row, _ := nxtest.FindOnScreen(lines[1:], "xy")
		return row >= 0
	}, "'xy' adjacent on a content row", 10*time.Second)

	row, col := nxtest.FindOnScreen(lines[1:], "xy")
	t.Logf("'xy' at content row %d, col %d", row, col)

	// Verify the server also has "xy" via termctl
	out := runNxtermctl(t, socketPath, "region", "list")
	var regionID string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && len(fields[0]) == 36 {
			regionID = fields[0]
			break
		}
	}
	if regionID == "" {
		t.Fatal("could not find region ID")
	}
	view := runNxtermctl(t, socketPath, "region", "view", regionID)
	viewRow, viewCol := nxtest.FindOnScreen(strings.Split(view, "\n"), "xy")
	t.Logf("server view: 'xy' at row %d, col %d", viewRow, viewCol)
	if viewRow < 0 {
		t.Fatalf("server region view does not contain 'xy':\n%s", view)
	}
}

func TestCursorMovementAfterProgram(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Type several lines so there's content on screen
	nxt.Write([]byte("echo line_a\r"))
	nxt.WaitFor("line_a", 10*time.Second)
	nxt.WaitFor("nxterm$",10*time.Second)
	nxt.Write([]byte("echo line_b\r"))
	nxt.WaitFor("line_b", 10*time.Second)
	nxt.WaitFor("nxterm$",10*time.Second)

	// Run a command that writes to many rows (like top does).
	// Use seq to fill several lines, then echo a unique marker so
	// we can wait for the prompt that follows it (a bare
	// WaitFor("nxterm$") would race-match the prompt that's still
	// on screen from before the seq command).
	nxt.Write([]byte("seq 1 10\r"))
	nxt.WaitFor("10", 10*time.Second)
	nxt.Write([]byte("echo SEQ_DONE\r"))
	nxt.WaitFor("SEQ_DONE", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// The prompt should be on a row AFTER the seq output. Find the
	// LAST occurrence of "10" (the actual seq output line, not the
	// command echo) and the LAST prompt.
	lines := nxt.ScreenLines()
	lastSeq := -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " ")
		// Match a line that is exactly "10" — that's seq output,
		// not the command echo "seq 1 10".
		if trimmed == "10" {
			lastSeq = i
		}
	}
	lastPrompt := -1
	for i, line := range lines {
		if strings.Contains(line, "nxterm$") {
			lastPrompt = i
		}
	}
	t.Logf("'10' last at row %d, prompt last at row %d", lastSeq, lastPrompt)

	if lastSeq < 0 {
		t.Fatalf("could not find seq output '10' on screen:\n%s", strings.Join(lines, "\n"))
	}
	if lastPrompt <= lastSeq {
		t.Fatalf("prompt (row %d) should be after seq output '10' (row %d)\nscreen:\n%s",
			lastPrompt, lastSeq, strings.Join(lines, "\n"))
	}
}

func TestColorRendering(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$",10*time.Second)

	// Produce colored output via printf using ansi constants converted to shell notation.
	nxt.Write([]byte("printf '" +
		shellSGR(ansi.AttrRedForegroundColor) + "RED" + shellResetStyle + " " +
		shellSGR(ansi.AttrGreenForegroundColor) + "GRN" + shellResetStyle + " " +
		shellSGR(ansi.AttrBold) + "BLD" + shellResetStyle + " " +
		shellSGR(ansi.AttrExtendedForegroundColor, 5, 208) + "ORNG" + shellResetStyle +
		`\n'` + "\r"))

	// Wait for the output line (starts with "RED" at col 0, not the command echo)
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "RED") {
				return true
			}
		}
		return false
	}, "output line starting with 'RED'", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	cells := nxt.ScreenCells()

	// Find the output line (starts with "RED" at col 0)
	outputRow := -1
	for row, line := range cells {
		if len(line) > 0 && line[0].Data == "R" &&
			len(line) > 2 && line[1].Data == "E" && line[2].Data == "D" {
			outputRow = row
			break
		}
	}
	if outputRow < 0 {
		t.Fatal("could not find output line starting with 'RED'")
	}

	line := cells[outputRow]
	t.Logf("output row %d: first 40 chars with attrs:", outputRow)
	for col := 0; col < 40 && col < len(line); col++ {
		c := line[col]
		if c.Data != " " || (c.Attr.Fg.Name != "" && c.Attr.Fg.Name != "default") {
			t.Logf("  [%d] %q fg=%d/%q bg=%d/%q bold=%v",
				col, c.Data, c.Attr.Fg.Mode, c.Attr.Fg.Name, c.Attr.Bg.Mode, c.Attr.Bg.Name, c.Attr.Bold)
		}
	}

	// Check "RED" (col 0-2) has red foreground
	if line[0].Attr.Fg.Name != "red" {
		t.Errorf("'RED' at col 0: expected red fg, got mode=%d name=%q", line[0].Attr.Fg.Mode, line[0].Attr.Fg.Name)
	}

	// Find "GRN" after "RED " (col 4-6)
	grnCol := -1
	for col := 3; col+3 <= len(line); col++ {
		if line[col].Data == "G" && line[col+1].Data == "R" && line[col+2].Data == "N" {
			grnCol = col
			break
		}
	}
	if grnCol < 0 {
		t.Fatal("could not find 'GRN' on output line")
	}
	if line[grnCol].Attr.Fg.Name != "green" {
		t.Errorf("'GRN' at col %d: expected green fg, got mode=%d name=%q", grnCol, line[grnCol].Attr.Fg.Mode, line[grnCol].Attr.Fg.Name)
	}

	// Find "BLD" and check bold
	bldCol := -1
	for col := grnCol + 3; col+3 <= len(line); col++ {
		if line[col].Data == "B" && line[col+1].Data == "L" && line[col+2].Data == "D" {
			bldCol = col
			break
		}
	}
	if bldCol < 0 {
		t.Fatal("could not find 'BLD' on output line")
	}
	if !line[bldCol].Attr.Bold {
		t.Errorf("'BLD' at col %d: expected bold", bldCol)
	}

	// Find "ORNG" and check 256-color (index 208)
	orngCol := -1
	for col := bldCol + 3; col+4 <= len(line); col++ {
		if line[col].Data == "O" && line[col+1].Data == "R" &&
			line[col+2].Data == "N" && line[col+3].Data == "G" {
			orngCol = col
			break
		}
	}
	if orngCol < 0 {
		t.Fatal("could not find 'ORNG' on output line")
	}
	fg := line[orngCol].Attr.Fg
	if fg.Mode == 0 && (fg.Name == "" || fg.Name == "default") {
		t.Errorf("'ORNG' at col %d: expected non-default color, got mode=%d name=%q index=%d",
			orngCol, fg.Mode, fg.Name, fg.Index)
	}
}

// TestFaintRendering reproduces the SGR sequence lipgloss emits for the
// status bar (faint separators around a bold label) and verifies the faint
// attribute round-trips through the server's VT parser, the protocol, and
// the client's screen state — without leaking unrelated attributes like
// underline.
func TestFaintRendering(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Mimics statusFaint.Render("• ") + statusBold.Render("BOLD") + statusFaint.Render(" •")
	// from frontend/ui/model.go:renderStatusBar — what the inner ttui's
	// status bar produces and what the outer ttui must reproduce faithfully.
	nxt.Write([]byte("printf '" +
		shellSGR(ansi.AttrFaint) + "FNT1 " + shellResetStyle +
		shellSGR(ansi.AttrBold) + "BOLD" + shellResetStyle + " " +
		shellSGR(ansi.AttrFaint) + "FNT2" + shellResetStyle +
		`\n'` + "\r"))

	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "FNT1") {
				return true
			}
		}
		return false
	}, "output line starting with 'FNT1'", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	cells := nxt.ScreenCells()

	outputRow := -1
	for row, line := range cells {
		if len(line) >= 4 && line[0].Data == "F" && line[1].Data == "N" &&
			line[2].Data == "T" && line[3].Data == "1" {
			outputRow = row
			break
		}
	}
	if outputRow < 0 {
		t.Fatal("could not find output line starting with 'FNT1'")
	}

	line := cells[outputRow]
	t.Logf("output row %d, first 20 cells:", outputRow)
	for col := 0; col < 20 && col < len(line); col++ {
		c := line[col]
		t.Logf("  [%d] %q bold=%v faint=%v underline=%v italic=%v",
			col, c.Data, c.Attr.Bold, c.Attr.Faint, c.Attr.Underline, c.Attr.Italics)
	}

	// FNT1 (cols 0-3) should be faint, not bold, not underlined.
	for col := 0; col < 4; col++ {
		c := line[col]
		if !c.Attr.Faint {
			t.Errorf("col %d %q: expected faint=true", col, c.Data)
		}
		if c.Attr.Bold {
			t.Errorf("col %d %q: expected bold=false", col, c.Data)
		}
		if c.Attr.Underline {
			t.Errorf("col %d %q: expected underline=false", col, c.Data)
		}
	}

	// Locate BOLD — should be bold and NOT faint.
	bldCol := -1
	for col := 4; col+4 <= len(line); col++ {
		if line[col].Data == "B" && line[col+1].Data == "O" &&
			line[col+2].Data == "L" && line[col+3].Data == "D" {
			bldCol = col
			break
		}
	}
	if bldCol < 0 {
		t.Fatal("could not find 'BOLD' on output line")
	}
	for col := bldCol; col < bldCol+4; col++ {
		c := line[col]
		if !c.Attr.Bold {
			t.Errorf("col %d %q: expected bold=true", col, c.Data)
		}
		if c.Attr.Faint {
			t.Errorf("col %d %q: expected faint=false", col, c.Data)
		}
		if c.Attr.Underline {
			t.Errorf("col %d %q: expected underline=false", col, c.Data)
		}
	}

	// Locate FNT2 — should be faint again, not bold.
	fnt2Col := -1
	for col := bldCol + 4; col+4 <= len(line); col++ {
		if line[col].Data == "F" && line[col+1].Data == "N" &&
			line[col+2].Data == "T" && line[col+3].Data == "2" {
			fnt2Col = col
			break
		}
	}
	if fnt2Col < 0 {
		t.Fatal("could not find 'FNT2' on output line")
	}
	for col := fnt2Col; col < fnt2Col+4; col++ {
		c := line[col]
		if !c.Attr.Faint {
			t.Errorf("col %d %q: expected faint=true", col, c.Data)
		}
		if c.Attr.Bold {
			t.Errorf("col %d %q: expected bold=false", col, c.Data)
		}
		if c.Attr.Underline {
			t.Errorf("col %d %q: expected underline=false", col, c.Data)
		}
	}
}

// TestModifyOtherKeysDoesNotLeakSGR verifies that xterm's
// "modifyOtherKeys" CSI sequences (\e[>4m, \e[>4;2m) — emitted by
// bubbletea v2 on startup to enable extended keyboard reporting — are
// NOT misparsed as SGR by the server's VT layer. Without the fix in
// pkg/te/stream.go, the parser stripped the '>' private prefix and
// dispatched [4;2]m to SelectGraphicRendition, where 4 is the SGR
// code for underline and 2 is faint, contaminating every subsequent
// draw with stale attributes.
//
// This is a full-pipeline regression: bytes go through bash → server
// te.Stream → te.Screen cells → protocol → client te.Screen → test
// harness te.Screen, so any cell-level corruption shows up here.
func TestModifyOtherKeysDoesNotLeakSGR(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Emit \e[>4;2m (modifyOtherKeys) and then a plain "MARKER" with
	// no styling. If the parser misinterprets >4;2 as SGR, MARKER
	// will pick up underline+faint from the contaminated cursor.
	nxt.Write([]byte("printf '\\e[>4;2mMARKER\\n'\r"))

	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "MARKER") {
				return true
			}
		}
		return false
	}, "output line starting with 'MARKER'", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	cells := nxt.ScreenCells()
	outputRow := -1
	for row, line := range cells {
		if len(line) >= 6 && line[0].Data == "M" && line[1].Data == "A" &&
			line[2].Data == "R" && line[3].Data == "K" && line[4].Data == "E" && line[5].Data == "R" {
			outputRow = row
			break
		}
	}
	if outputRow < 0 {
		t.Fatal("could not find output line starting with 'MARKER'")
	}

	for col := 0; col < 6; col++ {
		c := cells[outputRow][col]
		if c.Attr.Underline {
			t.Errorf("col %d %q: expected Underline=false (modifyOtherKeys leaked)", col, c.Data)
		}
		if c.Attr.Faint {
			t.Errorf("col %d %q: expected Faint=false (modifyOtherKeys leaked)", col, c.Data)
		}
		if c.Attr.Bold {
			t.Errorf("col %d %q: expected Bold=false", col, c.Data)
		}
	}
}

// TestKittyKeyboardSequencesDoNotLeakAsText verifies that CSI
// sequences with the '<' / '=' private prefixes (kitty keyboard
// protocol push/pop) are fully consumed by the parser instead of
// having their parameter bytes drawn as text. Bubbletea v2 emits
// "\e[=0;1u" / "\e[=1;1u" on startup and a corresponding pop on
// shutdown; before the fix, the parser bailed on '=' (treated it as
// an unknown final byte), then drew "0;1u" as plain text on the
// screen next to the cursor.
func TestKittyKeyboardSequencesDoNotLeakAsText(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Emit the kitty keyboard push and pop sequences, then a marker
	// we can locate. If the parser leaks, "0;1u" or "1;1u" appears in
	// the screen text adjacent to the marker.
	nxt.Write([]byte("printf '\\e[=0;1u\\e[=1;1u\\e[<uMARKER\\n'\r"))

	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "MARKER") {
				return true
			}
		}
		return false
	}, "output line starting with 'MARKER'", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	cells := nxt.ScreenCells()
	for row, line := range cells {
		if len(line) == 0 || line[0].Data != "M" {
			continue
		}
		// Check that no leaked kitty params appear ahead of MARKER on
		// this row, and that MARKER itself is at column 0.
		_ = row
		if line[0].Data == "M" && line[1].Data == "A" && line[2].Data == "R" {
			// Walk the row scanning for stray "0;1u" / "1;1u" / "u" before MARKER.
			// (MARKER starts at col 0, so there should be nothing before it.)
			return
		}
	}

	// Also walk the whole screen and assert no row contains the
	// distinctive leaked-text patterns.
	for _, line := range cells {
		var sb strings.Builder
		for _, c := range line {
			sb.WriteString(c.Data)
		}
		row := sb.String()
		for _, leak := range []string{"0;1u", "1;1u"} {
			if strings.Contains(row, leak) {
				t.Errorf("found leaked kitty keyboard params %q in row: %q", leak, strings.TrimRight(row, " "))
			}
		}
	}
}

// TestPTYRegionRespondsToDECRQM verifies that the server's te.Screen
// writes replies back to the PTY for terminal queries from the child.
// Specifically, when the child sends DECRQM (\e[?2026$p), it should
// receive a DECRPM reply on its stdin. Without the WriteProcessInput
// wiring in NewRegion, the te generates the reply but it goes to a
// no-op callback and the child times out.
//
// Bubbletea v2 emits these queries on startup; without replies it
// falls back to assuming features are unsupported and adds startup
// latency. This test exercises the same query path via bash.
func TestPTYRegionRespondsToDECRQM(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Send a DECRQM query (\e[?2026$p) directly to /dev/tty so it
	// reaches the server's te through the PTY master, then read the
	// reply from /dev/tty with a 1s timeout, and finally print the
	// captured byte count. If the wiring is correct, len > 0; if not,
	// len = 0 (timeout). The DECRPM reply for mode 2026 is
	// "\e[?2026;<status>$y" which is 11 bytes.
	//
	// We have to use a marker that survives bash's command echo: the
	// raw command line is also written to the screen, so any literal
	// like "DECRQM_LEN=" appears in both the echoed command and the
	// actual output. Embedding the variable expansion only in the
	// output line — "ANS:${#reply}" — means the echoed command has
	// "ANS:$" while the output has "ANS:11" (or "ANS:0" on failure),
	// which we can distinguish.
	nxt.Write([]byte(`printf '\e[?2026$p' > /dev/tty; IFS= read -rsn 16 -t 1 reply < /dev/tty; echo "ANS:${#reply}"` + "\r"))

	hasNonZeroAns := func(lines []string) bool {
		for _, line := range lines {
			for i := 0; i+5 < len(line); i++ {
				if line[i:i+4] == "ANS:" {
					next := line[i+4]
					if next >= '1' && next <= '9' {
						return true
					}
				}
			}
		}
		return false
	}
	nxt.WaitForScreen(hasNonZeroAns, `ANS:N where N > 0`, 10*time.Second)
}

// TestCursorHiddenByDECTCEM verifies that the frontend honors the
// DECTCEM cursor visibility flag (private mode 25). When the child
// emits \e[?25l, the frontend must not draw a reverse-video phantom
// cursor on top of the cell at the cursor position. Without the fix,
// programs that draw their own cursor (bubbletea TUIs, less, claude
// code, a nested nxterm) would have a stray cursor cell painted by
// the outer renderer, causing doubled cursors in nested sessions and
// stray cursors elsewhere.
func TestCursorHiddenByDECTCEM(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Use `read -rsn 1` to park bash with the cursor in a known
	// position: hide the cursor, move to row 12 col 20, print 'Z',
	// then wait for one keystroke. While bash is in `read`, the
	// inner cursor sits at (row 12, col 21) with DECTCEM unset. The
	// outer's renderer must NOT paint a reverse-video phantom cursor
	// at that cell.
	nxt.Write([]byte(`printf '\e[?25l\e[12;20HZ'; read -rsn 1 dummy` + "\r"))

	// Wait for the 'Z' to land. The outer's tab bar occupies row 0
	// and the status-bar margin (default 1) occupies row 1, so the
	// inner's row 12 (1-indexed) lands at outer row 13 (0-indexed
	// inner row 11; +1 for tab bar; +1 for margin).
	nxt.WaitForScreen(func(lines []string) bool {
		if len(lines) < 14 {
			return false
		}
		return len(lines[13]) > 19 && lines[13][19] == 'Z'
	}, "'Z' at outer row 13 col 19", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	cells := nxt.ScreenCells()

	// The cell at outer (13, 19) is the 'Z' itself; it should be
	// plain, not reverse.
	zCell := cells[13][19]
	if zCell.Data != "Z" {
		t.Fatalf("expected 'Z' at (13,19), got %q", zCell.Data)
	}
	if zCell.Attr.Reverse {
		t.Errorf("'Z' cell at (13,19) has Reverse=true (cursor not hidden by DECTCEM)")
	}

	// The cell at outer (13, 20) is where the cursor would be —
	// after printing 'Z' the inner cursor advances to col 21 (1-indexed)
	// = col 20 (0-indexed). With DECTCEM off, this cell must not be
	// reverse-video.
	cursorCell := cells[13][20]
	if cursorCell.Attr.Reverse {
		t.Errorf("cursor cell at (13,20) has Reverse=true after \\e[?25l (phantom cursor not hidden), data=%q", cursorCell.Data)
	}

	// Unblock bash's read so the test can clean up.
	nxt.Write([]byte("\r"))
}

func TestActiveTabBold(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)

	// Spawn a second region so we have active and inactive tabs.
	// Tab 2 becomes active (label " 2 "), tab 1 becomes inactive
	// (label " 1:bash ").
	nxt.Write([]byte("\x02c"))
	nxt.WaitFor("1:bash", 10*time.Second)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	cells := nxt.ScreenCells()
	if len(cells) == 0 {
		t.Fatal("no screen cells")
	}
	tabRow := cells[0]

	// Locate each tab number on row 0. The active tab renders as
	// " <n> " (digit followed by ">") and the inactive tab as
	// " n:<name> " (digit followed by ":").
	tab1Col := findDigitFollowedBy(tabRow, "1", ":") // inactive
	tab2Col := findDigitFollowedBy(tabRow, "2", ">") // active
	if tab1Col < 0 || tab2Col < 0 {
		t.Fatalf("could not find tab labels on row 0: tab1=%d tab2=%d", tab1Col, tab2Col)
	}
	if tabRow[tab1Col].Attr.Bold {
		t.Errorf("inactive tab '1' at col %d should not be bold", tab1Col)
	}
	if !tabRow[tab2Col].Attr.Bold {
		t.Errorf("active tab '2' at col %d should be bold", tab2Col)
	}

	// Switch to tab 1: tab 1 becomes active (" <1> "), tab 2 inactive (" 2:bash ").
	nxt.Write([]byte("\x021"))
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	cells = nxt.ScreenCells()
	tabRow = cells[0]
	tab1Col = findDigitFollowedBy(tabRow, "1", ">") // active
	tab2Col = findDigitFollowedBy(tabRow, "2", ":") // inactive
	if tab1Col < 0 || tab2Col < 0 {
		t.Fatalf("could not find tab labels after switch: tab1=%d tab2=%d", tab1Col, tab2Col)
	}
	if !tabRow[tab1Col].Attr.Bold {
		t.Errorf("active tab '1' at col %d should be bold after switch", tab1Col)
	}
	if tabRow[tab2Col].Attr.Bold {
		t.Errorf("inactive tab '2' at col %d should not be bold after switch", tab2Col)
	}
}

// findDigitFollowedBy returns the column of digit on row, where the
// next cell's data equals next. -1 if not found. Used by
// TestActiveTabBold to locate tab number cells on the tab bar row.
func findDigitFollowedBy(row []te.Cell, digit, next string) int {
	for col := 0; col+1 < len(row); col++ {
		if row[col].Data == digit && row[col+1].Data == next {
			return col
		}
	}
	return -1
}

func TestResize(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Write([]byte("tput cols\r"))

	lines := nxt.WaitForScreen(func(lines []string) bool {
		row, _ := nxtest.FindOnScreen(lines[1:], "80")
		return row >= 0
	}, "'80' on a content row", 10*time.Second)

	row, col := nxtest.FindOnScreen(lines[1:], "80")
	t.Logf("'80' at content row %d, col %d", row, col)
}

func TestResizeMidSession(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$",10*time.Second)

	// Verify initial 80 columns
	nxt.Write([]byte("tput cols\r"))
	nxt.WaitForScreen(func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "80") {
				return true
			}
		}
		return false
	}, "'80' at col 0 on a content row", 10*time.Second)

	nxt.WaitFor("nxterm$",10*time.Second)

	// Resize to 120x40
	nxt.Resize(120, 40)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Verify new column count
	nxt.Write([]byte("tput cols\r"))
	nxt.WaitForScreen(func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "120") {
				return true
			}
		}
		return false
	}, "'120' at col 0 on a content row", 10*time.Second)

	// Verify new row count (40 - 1 for tab bar - 1 for status-bar margin = 38)
	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.Write([]byte("tput lines\r"))
	nxt.WaitForScreen(func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "38") {
				return true
			}
		}
		return false
	}, "'38' at col 0 on a content row", 10*time.Second)
}

func TestAltScreenRestore(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Use nxtermctl to verify server-side screen state (avoids bubbletea
	// rendering diff issues in the test's go-te Screen).
	id := spawnRegion(t, socketPath, "shell")
	regionSendAndWait(t, socketPath, id, `echo alt_screen_marker\r`, "alt_screen_marker")

	// Enter alt screen via less
	runNxtermctl(t, socketPath, "region", "send", "-e", id, `echo 'line1\nline2\nline3' | less\r`)

	// Wait for less to show content in alt screen AND be ready for input.
	// Checking for "(END)" ensures less has fully initialized its display.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runNxtermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "(END)") && !strings.Contains(view, "alt_screen_marker") {
			goto inAlt
		}
		time.Sleep(50 * time.Millisecond)
	}
	{
		view := runNxtermctl(t, socketPath, "region", "view", id)
		t.Fatalf("timeout waiting for less to enter alt screen\nscreen:\n%s", view)
	}

inAlt:
	// Quit less
	runNxtermctl(t, socketPath, "region", "send", id, "q")

	// The marker should reappear (alt screen exited, main buffer restored)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runNxtermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "alt_screen_marker") {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	{
		view := runNxtermctl(t, socketPath, "region", "view", id)
		t.Fatalf("timeout waiting for marker to reappear after less exits\nscreen:\n%s", view)
	}
}

func TestScreenSyncAfterTop(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$",10*time.Second)

	// Run top, wait for it to render, then quit
	nxt.Write([]byte("top\r"))
	nxt.WaitForScreen(func(lines []string) bool {
		for _, line := range lines {
			if strings.Contains(line, "load average") {
				return true
			}
		}
		return false
	}, "top showing load average", 5*time.Second)
	nxt.Write([]byte("q"))

	// Wait for prompt to reappear
	nxt.WaitFor("nxterm$",10*time.Second)
	nxt.WaitForSilence(500 * time.Millisecond)

	// Get server screen via termctl
	out := runNxtermctl(t, socketPath, "region", "list")
	var regionID string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && len(fields[0]) == 36 {
			regionID = fields[0]
			break
		}
	}
	if regionID == "" {
		t.Fatal("no region found")
	}
	serverView := runNxtermctl(t, socketPath, "region", "view", regionID)
	serverLines := strings.Split(strings.TrimRight(serverView, "\n"), "\n")

	// Find prompt row on server (view trims trailing spaces)
	serverPromptRow := -1
	for i := len(serverLines) - 1; i >= 0; i-- {
		if strings.Contains(serverLines[i], "nxterm$") {
			serverPromptRow = i
			break
		}
	}

	// Find prompt row on frontend (row 0 is tab bar, row 1 is the
	// status-bar margin, content starts at row 2 by default).
	frontendLines := nxt.ScreenLines()
	frontendPromptRow := -1
	for i := len(frontendLines) - 1; i >= 0; i-- {
		if strings.Contains(frontendLines[i], "nxterm$") {
			frontendPromptRow = i
			break
		}
	}

	t.Logf("server prompt at row %d/%d, frontend prompt at row %d/%d",
		serverPromptRow, len(serverLines), frontendPromptRow, len(frontendLines))

	// Frontend prompt should be at serverPromptRow + 2 (tab bar + margin)
	expectedRow := serverPromptRow + 2
	if frontendPromptRow != expectedRow {
		t.Logf("=== FRONTEND ===")
		for i, line := range frontendLines {
			if strings.TrimSpace(line) != "" {
				t.Logf("  f[%2d]: %.70s", i, line)
			}
		}
		t.Logf("=== SERVER ===")
		for i, line := range serverLines {
			if strings.TrimSpace(line) != "" {
				t.Logf("  s[%2d]: %.70s", i, line)
			}
		}
		t.Fatalf("frontend prompt at row %d, expected %d (server row %d + 1 for tab bar + 1 for margin)",
			frontendPromptRow, expectedRow, serverPromptRow)
	}
}
