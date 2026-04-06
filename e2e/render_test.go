package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestStartAndRender(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// Wait for the tab bar to render with the region name.
	lines := pio.WaitFor(t, "bash", 10*time.Second)
	row, col := findOnScreen(lines, "bash")
	if row != 0 {
		t.Fatalf("expected 'bash' on row 0, found on row %d", row)
	}
	if col > 10 {
		t.Fatalf("expected 'bash' near start of row 0, found at col %d", col)
	}

	// Wait for the shell prompt to render below the tab bar.
	pio.WaitFor(t, "$", 10*time.Second)
}

func TestCursorPosition(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	pio.Write([]byte("xy"))

	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "xy")
		return row >= 0
	}, "'xy' adjacent on a content row", 10*time.Second)

	row, col := findOnScreen(lines[1:], "xy")
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
	viewRow, viewCol := findOnScreen(strings.Split(view, "\n"), "xy")
	t.Logf("server view: 'xy' at row %d, col %d", viewRow, viewCol)
	if viewRow < 0 {
		t.Fatalf("server region view does not contain 'xy':\n%s", view)
	}
}

func TestCursorMovementAfterProgram(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Type several lines so there's content on screen
	pio.Write([]byte("echo line_a\r"))
	pio.WaitFor(t, "line_a", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)
	pio.Write([]byte("echo line_b\r"))
	pio.WaitFor(t, "line_b", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Run a command that writes to many rows (like top does).
	// Use seq to fill several lines, then verify the prompt appears
	// AFTER the seq output, not overlapping it.
	pio.Write([]byte("seq 1 10\r"))
	pio.WaitFor(t, "10", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)

	// The prompt should be on a row AFTER "10"
	lines := pio.ScreenLines()
	seqRow, _ := findOnScreen(lines, "10")
	promptRow, _ := findOnScreen(lines, "nxterm$")
	t.Logf("'10' at row %d, last 'nxterm$ ' at row %d", seqRow, promptRow)

	// Find the LAST occurrence of "nxterm$" (the current prompt)
	lastPrompt := -1
	for i, line := range lines {
		if strings.Contains(line, "nxterm$") {
			lastPrompt = i
		}
	}
	t.Logf("last prompt at row %d", lastPrompt)

	if lastPrompt <= seqRow {
		t.Fatalf("prompt (row %d) should be after seq output '10' (row %d)\nscreen:\n%s",
			lastPrompt, seqRow, strings.Join(lines, "\n"))
	}
}

func TestColorRendering(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Produce colored output via printf using ansi constants converted to shell notation.
	pio.Write([]byte("printf '" +
		shellSGR(ansi.AttrRedForegroundColor) + "RED" + shellResetStyle + " " +
		shellSGR(ansi.AttrGreenForegroundColor) + "GRN" + shellResetStyle + " " +
		shellSGR(ansi.AttrBold) + "BLD" + shellResetStyle + " " +
		shellSGR(ansi.AttrExtendedForegroundColor, 5, 208) + "ORNG" + shellResetStyle +
		`\n'` + "\r"))

	// Wait for the output line (starts with "RED" at col 0, not the command echo)
	pio.WaitForScreen(t, func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "RED") {
				return true
			}
		}
		return false
	}, "output line starting with 'RED'", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	cells := pio.ScreenCells()

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
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Mimics statusFaint.Render("• ") + statusBold.Render("BOLD") + statusFaint.Render(" •")
	// from frontend/ui/model.go:renderStatusBar — what the inner ttui's
	// status bar produces and what the outer ttui must reproduce faithfully.
	pio.Write([]byte("printf '" +
		shellSGR(ansi.AttrFaint) + "FNT1 " + shellResetStyle +
		shellSGR(ansi.AttrBold) + "BOLD" + shellResetStyle + " " +
		shellSGR(ansi.AttrFaint) + "FNT2" + shellResetStyle +
		`\n'` + "\r"))

	pio.WaitForScreen(t, func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "FNT1") {
				return true
			}
		}
		return false
	}, "output line starting with 'FNT1'", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	cells := pio.ScreenCells()

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

func TestActiveTabBold(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Spawn a second region so we have active and inactive tabs
	pio.Write([]byte("\x02c"))
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && strings.Contains(lines[0], "2:bash")
	}, "tab bar with both tabs", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	cells := pio.ScreenCells()
	if len(cells) == 0 {
		t.Fatal("no screen cells")
	}
	tabRow := cells[0]

	// Find "1:" and "2:" on row 0 to locate each tab label
	tab1Col, tab2Col := -1, -1
	for col := 0; col+1 < len(tabRow); col++ {
		if tabRow[col].Data == "1" && tabRow[col+1].Data == ":" {
			tab1Col = col
		}
		if tabRow[col].Data == "2" && tabRow[col+1].Data == ":" {
			tab2Col = col
		}
	}
	if tab1Col < 0 || tab2Col < 0 {
		t.Fatalf("could not find tab labels on row 0: tab1=%d tab2=%d", tab1Col, tab2Col)
	}

	// Tab 2 is active (just spawned), tab 1 is inactive.
	// Active tab should be bold, inactive should NOT be bold.
	if tabRow[tab1Col].Attr.Bold {
		t.Errorf("inactive tab '1:bash' at col %d should not be bold", tab1Col)
	}
	if !tabRow[tab2Col].Attr.Bold {
		t.Errorf("active tab '2:bash' at col %d should be bold", tab2Col)
	}

	// Switch to tab 1 and verify bold flips
	pio.Write([]byte("\x021"))
	pio.WaitFor(t, "nxterm$", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	cells = pio.ScreenCells()
	tabRow = cells[0]
	tab1Col, tab2Col = -1, -1
	for col := 0; col+1 < len(tabRow); col++ {
		if tabRow[col].Data == "1" && tabRow[col+1].Data == ":" {
			tab1Col = col
		}
		if tabRow[col].Data == "2" && tabRow[col+1].Data == ":" {
			tab2Col = col
		}
	}
	if tab1Col < 0 || tab2Col < 0 {
		t.Fatalf("could not find tab labels after switch: tab1=%d tab2=%d", tab1Col, tab2Col)
	}
	if !tabRow[tab1Col].Attr.Bold {
		t.Errorf("active tab '1:bash' at col %d should be bold after switch", tab1Col)
	}
	if tabRow[tab2Col].Attr.Bold {
		t.Errorf("inactive tab '2:bash' at col %d should not be bold after switch", tab2Col)
	}
}

func TestResize(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$",10*time.Second)
	pio.Write([]byte("tput cols\r"))

	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "80")
		return row >= 0
	}, "'80' on a content row", 10*time.Second)

	row, col := findOnScreen(lines[1:], "80")
	t.Logf("'80' at content row %d, col %d", row, col)
}

func TestResizeMidSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Verify initial 80 columns
	pio.Write([]byte("tput cols\r"))
	pio.WaitForScreen(t, func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "80") {
				return true
			}
		}
		return false
	}, "'80' at col 0 on a content row", 10*time.Second)

	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Resize to 120x40
	pio.Resize(120, 40)
	pio.WaitForSilence(200 * time.Millisecond)

	// Verify new column count
	pio.Write([]byte("tput cols\r"))
	pio.WaitForScreen(t, func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "120") {
				return true
			}
		}
		return false
	}, "'120' at col 0 on a content row", 10*time.Second)

	// Verify new row count (40 - 1 for tab bar = 39)
	pio.WaitFor(t, "nxterm$",10*time.Second)
	pio.Write([]byte("tput lines\r"))
	pio.WaitForScreen(t, func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "39") {
				return true
			}
		}
		return false
	}, "'39' at col 0 on a content row", 10*time.Second)
}

func TestAltScreenRestore(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Use nxtermctl to verify server-side screen state (avoids bubbletea
	// rendering diff issues in the test's go-te Screen).
	id := spawnRegion(t, socketPath, "shell")

	// Type a marker
	runNxtermctl(t, socketPath, "region", "send", "-e", id, `echo alt_screen_marker\r`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runNxtermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "alt_screen_marker") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Enter alt screen via less
	runNxtermctl(t, socketPath, "region", "send", "-e", id, `echo 'line1\nline2\nline3' | less\r`)

	// Wait for less to show "line1" AND marker to be gone (alt screen active)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runNxtermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "line1") && !strings.Contains(view, "alt_screen_marker") {
			goto inAlt
		}
		time.Sleep(10 * time.Millisecond)
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
		time.Sleep(10 * time.Millisecond)
	}
	{
		view := runNxtermctl(t, socketPath, "region", "view", id)
		t.Fatalf("timeout waiting for marker to reappear after less exits\nscreen:\n%s", view)
	}
}

func TestScreenSyncAfterTop(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$",10*time.Second)

	// Run top briefly then quit
	pio.Write([]byte("top\r"))
	time.Sleep(2 * time.Second)
	pio.Write([]byte("q"))

	// Wait for prompt to reappear
	pio.WaitFor(t, "nxterm$",10*time.Second)
	pio.WaitForSilence(500 * time.Millisecond)

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

	// Find prompt row on frontend (row 0 is tab bar, content starts at row 1)
	frontendLines := pio.ScreenLines()
	frontendPromptRow := -1
	for i := len(frontendLines) - 1; i >= 0; i-- {
		if strings.Contains(frontendLines[i], "nxterm$") {
			frontendPromptRow = i
			break
		}
	}

	t.Logf("server prompt at row %d/%d, frontend prompt at row %d/%d",
		serverPromptRow, len(serverLines), frontendPromptRow, len(frontendLines))

	// Frontend prompt should be at serverPromptRow + 1 (tab bar offset)
	expectedRow := serverPromptRow + 1
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
		t.Fatalf("frontend prompt at row %d, expected %d (server row %d + 1 for tab bar)",
			frontendPromptRow, expectedRow, serverPromptRow)
	}
}
