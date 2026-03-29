package e2e

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
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

func TestInputRoundTrip(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	// "aGVsbG8K" is base64 for "hello\n".
	pio.Write([]byte("echo aGVsbG8K | base64 -d\r"))

	// Wait for "hello" at col 0 — the decoded output on its own line.
	// Don't match "hello" embedded in a prompt or command echo.
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "hello") {
				return true
			}
		}
		return false
	}, "'hello' at col 0 on a content row", 10*time.Second)

	row, col := -1, -1
	for i := 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "hello") {
			row, col = i-1, 0
			break
		}
	}
	t.Logf("'hello' at content row %d, col %d", row, col)
	if col != 0 {
		t.Fatalf("expected 'hello' at col 0, found at col %d", col)
	}
}

func TestResize(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)
	pio.Write([]byte("tput cols\r"))

	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "80")
		return row >= 0
	}, "'80' on a content row", 10*time.Second)

	row, col := findOnScreen(lines[1:], "80")
	t.Logf("'80' at content row %d, col %d", row, col)
}

func TestCursorPosition(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	pio.Write([]byte("xy"))

	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "xy")
		return row >= 0
	}, "'xy' adjacent on a content row", 10*time.Second)

	row, col := findOnScreen(lines[1:], "xy")
	t.Logf("'xy' at content row %d, col %d", row, col)

	// Verify the server also has "xy" via termctl
	out := runTermctl(t, socketPath, "region", "list")
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
	view := runTermctl(t, socketPath, "region", "view", regionID)
	viewRow, viewCol := findOnScreen(strings.Split(view, "\n"), "xy")
	t.Logf("server view: 'xy' at row %d, col %d", viewRow, viewCol)
	if viewRow < 0 {
		t.Fatalf("server region view does not contain 'xy':\n%s", view)
	}
}

func TestAltScreenRestore(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Use termctl to verify server-side screen state (avoids bubbletea
	// rendering diff issues in the test's go-te Screen).
	id := spawnRegion(t, socketPath, "shell")

	// Type a marker
	runTermctl(t, socketPath, "region", "send", "-e", id, `echo alt_screen_marker\r`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runTermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "alt_screen_marker") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Enter alt screen via less
	runTermctl(t, socketPath, "region", "send", "-e", id, `echo 'line1\nline2\nline3' | less\r`)

	// Wait for less to show "line1" AND marker to be gone (alt screen active)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runTermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "line1") && !strings.Contains(view, "alt_screen_marker") {
			goto inAlt
		}
		time.Sleep(10 * time.Millisecond)
	}
	{
		view := runTermctl(t, socketPath, "region", "view", id)
		t.Fatalf("timeout waiting for less to enter alt screen\nscreen:\n%s", view)
	}

inAlt:
	// Quit less
	runTermctl(t, socketPath, "region", "send", id, "q")

	// The marker should reappear (alt screen exited, main buffer restored)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runTermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "alt_screen_marker") {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	{
		view := runTermctl(t, socketPath, "region", "view", id)
		t.Fatalf("timeout waiting for marker to reappear after less exits\nscreen:\n%s", view)
	}
}

func TestScreenSyncAfterTop(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Run top briefly then quit
	pio.Write([]byte("top\r"))
	time.Sleep(2 * time.Second)
	pio.Write([]byte("q"))

	// Wait for prompt to reappear
	pio.WaitFor(t, "termd$",10*time.Second)
	pio.WaitForSilence(500 * time.Millisecond)

	// Get server screen via termctl
	out := runTermctl(t, socketPath, "region", "list")
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
	serverView := runTermctl(t, socketPath, "region", "view", regionID)
	serverLines := strings.Split(strings.TrimRight(serverView, "\n"), "\n")

	// Find prompt row on server (view trims trailing spaces)
	serverPromptRow := -1
	for i := len(serverLines) - 1; i >= 0; i-- {
		if strings.Contains(serverLines[i], "termd$") {
			serverPromptRow = i
			break
		}
	}

	// Find prompt row on frontend (row 0 is tab bar, content starts at row 1)
	frontendLines := pio.ScreenLines()
	frontendPromptRow := -1
	for i := len(frontendLines) - 1; i >= 0; i-- {
		if strings.Contains(frontendLines[i], "termd$") {
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

func TestCursorMovementAfterProgram(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	// Type several lines so there's content on screen
	pio.Write([]byte("echo line_a\r"))
	pio.WaitFor(t, "line_a", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)
	pio.Write([]byte("echo line_b\r"))
	pio.WaitFor(t, "line_b", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	// Run a command that writes to many rows (like top does).
	// Use seq to fill several lines, then verify the prompt appears
	// AFTER the seq output, not overlapping it.
	pio.Write([]byte("seq 1 10\r"))
	pio.WaitFor(t, "10", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	// The prompt should be on a row AFTER "10"
	lines := pio.ScreenLines()
	seqRow, _ := findOnScreen(lines, "10")
	promptRow, _ := findOnScreen(lines, "termd$")
	t.Logf("'10' at row %d, last 'termd$ ' at row %d", seqRow, promptRow)

	// Find the LAST occurrence of "termd$" (the current prompt)
	lastPrompt := -1
	for i, line := range lines {
		if strings.Contains(line, "termd$") {
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

	pio.WaitFor(t, "termd$",10*time.Second)

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

func TestRawInputPassthrough(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.WaitFor(t, "termd$",10*time.Second)
	pio.Write([]byte("sleep 999\r"))
	pio.Write([]byte("\x03"))

	pio.Write([]byte("echo raw_input_works\r"))
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "raw_input_works")
		return row >= 0
	}, "'raw_input_works' on a content row", 10*time.Second)

	row, col := findOnScreen(lines[1:], "raw_input_works")
	t.Logf("'raw_input_works' at content row %d, col %d", row, col)
}

func TestPrefixKeyDetach(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendFull(t, socketPath)
	defer fe.Kill()

	fe.WaitFor(t, "bash", 10*time.Second)

	fe.Write([]byte{0x02, 'd'})

	// Process should exit cleanly with code 0, no panic
	if err := fe.Wait(5 * time.Second); err != nil {
		t.Fatalf("frontend exited with error: %v", err)
	}
}

func TestPrefixKeyLiteralCtrlB(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	pio.Write([]byte("cat -v\r"))
	pio.WaitFor(t, "cat -v", 10*time.Second)

	pio.Write([]byte{0x02, 0x02})
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines, "^B")
		return row >= 0
	}, "'^B' on screen", 10*time.Second)

	// "^B" should be at col 0 (cat -v output)
	row, col := findOnScreen(lines, "^B")
	t.Logf("'^B' at row %d, col %d", row, col)
	if col != 0 {
		t.Fatalf("expected '^B' at col 0, found at col %d", col)
	}

	pio.Write([]byte("\x03"))
	pio.WaitFor(t, "termd$",10*time.Second)
}

func TestPrefixKeyStatusIndicator(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitForSilence(500 * time.Millisecond)

	pio.Write([]byte{0x02})
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, col := findOnScreen(lines, "? ")
		return row == 0 && col > 50
	}, "'?' right-justified on row 0", 3*time.Second)

	row, col := findOnScreen(lines, "? ")
	t.Logf("'?' at row %d, col %d", row, col)
	if row != 0 {
		t.Fatalf("expected prefix indicator on row 0, found on row %d", row)
	}

	// Dismiss (press an unbound key) and verify it clears
	pio.Write([]byte("z"))
	pio.Write([]byte("echo prefix_cleared\r"))
	pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "prefix_cleared")
		return row >= 0
	}, "'prefix_cleared' on screen", 10*time.Second)
}

func TestLogViewerOverlay(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	cmd := exec.Command("termd-tui", "--socket", socketPath, "--debug", )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	frontendCleanup := func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.WaitForSilence(500 * time.Millisecond)
	pio.Write([]byte{0x02, 'l'})

	lines := pio.WaitForScreen(t, func(lines []string) bool {
		topRow, _ := findOnScreen(lines, "\u256d")
		bottomRow, _ := findOnScreen(lines, "\u2570")
		helpRow, _ := findOnScreen(lines, "q/esc: close")
		return topRow >= 0 && bottomRow >= 0 && helpRow >= 0
	}, "overlay with borders and help text", 5*time.Second)

	topRow, topCol := findOnScreen(lines, "\u256d")
	bottomRow, bottomCol := findOnScreen(lines, "\u2570")
	helpRow, _ := findOnScreen(lines, "q/esc: close")

	t.Logf("overlay top border: row %d col %d", topRow, topCol)
	t.Logf("overlay bottom border: row %d col %d", bottomRow, bottomCol)
	t.Logf("help text: row %d", helpRow)

	// Borders should be vertically aligned (same column)
	if topCol != bottomCol {
		t.Fatalf("border columns don't align: top col=%d, bottom col=%d", topCol, bottomCol)
	}
	// Bottom should be below top
	if bottomRow <= topRow {
		t.Fatalf("bottom border (row %d) not below top border (row %d)", bottomRow, topRow)
	}
	// Both within screen bounds
	if topRow > 23 || bottomRow > 23 {
		t.Fatalf("overlay outside screen: top=%d, bottom=%d", topRow, bottomRow)
	}
	// Help text should be below or at the bottom border
	if helpRow < bottomRow {
		t.Fatalf("help text (row %d) above bottom border (row %d)", helpRow, bottomRow)
	}

	overlayHeight := bottomRow - topRow + 1
	t.Logf("overlay: %d rows", overlayHeight)

	// Close and verify overlay disappears
	pio.Write([]byte("q"))
	pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines, "\u256d")
		return row < 0
	}, "overlay gone", 10*time.Second)

	pio.WaitFor(t, "termd$",10*time.Second)
	pio.Write([]byte("echo logview_closed\r"))
	pio.WaitFor(t, "logview_closed", 10*time.Second)
}

func TestSessionPersistence(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio1, cleanup1 := startFrontend(t, socketPath)

	pio1.WaitFor(t, "bash", 10*time.Second)
	pio1.WaitFor(t, "termd$",10*time.Second)

	// Output colored text before detaching
	pio1.Write([]byte("printf '" +
		shellSGR(ansi.AttrRedForegroundColor) + "COLOR_PERSIST" + shellResetStyle +
		`\n'` + "\r"))
	pio1.WaitForScreen(t, func(lines []string) bool {
		for _, line := range lines {
			if strings.HasPrefix(line, "COLOR_PERSIST") {
				return true
			}
		}
		return false
	}, "output line starting with 'COLOR_PERSIST'", 10*time.Second)
	pio1.WaitForSilence(200 * time.Millisecond)

	// Verify colors are present before detach
	cells1 := pio1.ScreenCells()
	for row, line := range cells1 {
		if len(line) > 0 && line[0].Data == "C" &&
			len(line) > 12 && line[12].Data == "T" {
			fg := line[0].Attr.Fg
			t.Logf("before detach: 'COLOR_PERSIST' at row %d, fg=%d/%q", row, fg.Mode, fg.Name)
			if fg.Name != "red" {
				t.Fatalf("before detach: expected red fg, got %q", fg.Name)
			}
			break
		}
	}

	// Detach
	pio1.Write([]byte{0x02, 'd'})

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			cleanup1()
			t.Fatal("timeout waiting for first frontend to exit")
		case _, ok := <-pio1.ch:
			if !ok {
				goto reconnect
			}
		}
	}

reconnect:
	cleanup1()

	// Reattach
	pio2, cleanup2 := startFrontend(t, socketPath)
	defer cleanup2()

	pio2.WaitFor(t, "COLOR_PERSIST", 10*time.Second)
	pio2.WaitForSilence(200 * time.Millisecond)

	// Verify colors survived reattach
	cells2 := pio2.ScreenCells()
	found := false
	for row, line := range cells2 {
		if len(line) > 0 && line[0].Data == "C" &&
			len(line) > 12 && line[12].Data == "T" {
			fg := line[0].Attr.Fg
			t.Logf("after reattach: 'COLOR_PERSIST' at row %d, fg=%d/%q", row, fg.Mode, fg.Name)
			if fg.Name != "red" {
				t.Errorf("after reattach: expected red fg, got mode=%d name=%q", fg.Mode, fg.Name)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("could not find 'COLOR_PERSIST' output line after reattach")
	}
}

func TestResizeMidSession(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

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

	pio.WaitFor(t, "termd$",10*time.Second)

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
	pio.WaitFor(t, "termd$",10*time.Second)
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

func TestTCPTransport(t *testing.T) {
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Spawn a region via the Unix socket (termctl)
	_ = runTermctl(t, socketPath, "region", "spawn", "shell")

	// Connect frontend via TCP
	cmd := exec.Command("termd-tui", "--socket", "tcp:"+tcpAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via TCP: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	// Verify the tab bar shows the TCP endpoint
	lines := pio.ScreenLines()
	row0 := lines[0]
	if !strings.Contains(row0, tcpAddr) {
		t.Errorf("tab bar should show TCP addr %q, got: %q", tcpAddr, row0)
	}

	// Type a command and verify round-trip works
	pio.Write([]byte("echo tcp_works\r"))
	pio.WaitFor(t, "tcp_works", 10*time.Second)
}

func TestMousePassthrough(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Run mousehelper which enables mouse tracking and prints mouse events
	pio.Write([]byte("mousehelper\r"))
	// Wait for mouse mode to be enabled — the helper prints nothing until
	// it receives a mouse event, but we need to give it time to start
	time.Sleep(500 * time.Millisecond)

	// waitForMouse checks the screen for a specific MOUSE line.
	waitForMouse := func(expected string) {
		t.Helper()
		pio.WaitForScreen(t, func(lines []string) bool {
			for _, line := range lines {
				if strings.Contains(line, expected) {
					return true
				}
			}
			return false
		}, expected, 5*time.Second)
	}

	// Coordinates sent are in outer terminal space (1-based SGR).
	// The tab bar occupies row 1, so the frontend adjusts row by -1
	// before forwarding to the child. mousehelper prints what it receives.

	// Left click at col 5, row 3 → child sees row 2
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;3M", ansi.ESC)))
	waitForMouse("MOUSE press 0 5 2")

	// Left release
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;3m", ansi.ESC)))
	waitForMouse("MOUSE release 0 5 2")

	// Right click (button 2) at row 4 → child sees row 3
	pio.Write([]byte(fmt.Sprintf("%c[<2;10;4M", ansi.ESC)))
	waitForMouse("MOUSE press 2 10 3")

	// Middle click (button 1) at row 6 → child sees row 5
	pio.Write([]byte(fmt.Sprintf("%c[<1;8;6M", ansi.ESC)))
	waitForMouse("MOUSE press 1 8 5")

	// Scroll wheel up at row 3 → child sees row 2
	pio.Write([]byte(fmt.Sprintf("%c[<64;5;3M", ansi.ESC)))
	waitForMouse("MOUSE wheelup 64 5 2")

	// Scroll wheel down at row 3 → child sees row 2
	pio.Write([]byte(fmt.Sprintf("%c[<65;5;3M", ansi.ESC)))
	waitForMouse("MOUSE wheeldown 65 5 2")

	// Motion event (button 32 = motion + left held) at row 7 → child sees row 6
	pio.Write([]byte(fmt.Sprintf("%c[<32;12;7M", ansi.ESC)))
	waitForMouse("MOUSE press 32 12 6")

	// Click on the tab bar (row 1) → clamped to child row 1
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;1M", ansi.ESC)))
	waitForMouse("MOUSE press 0 5 1")

	// Click on content row 1 (row 2 in outer) → child sees row 1
	pio.Write([]byte(fmt.Sprintf("%c[<0;20;2M", ansi.ESC)))
	waitForMouse("MOUSE press 0 20 1")

	// Quit the helper
	pio.Write([]byte("q"))
	pio.WaitFor(t, "termd$",10*time.Second)
}

func TestMouseAfterTabSwitch(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$", 10*time.Second)

	// Run mousehelper in tab 1
	pio.Write([]byte("mousehelper\r"))
	time.Sleep(500 * time.Millisecond)

	// Verify mouse works initially
	pio.Write([]byte(fmt.Sprintf("%c[<0;5;3M", ansi.ESC)))
	pio.WaitFor(t, "MOUSE press 0 5 2", 5*time.Second)

	// Spawn tab 2 (switches to it automatically)
	pio.Write([]byte("\x02c"))
	pio.WaitForScreen(t, func(lines []string) bool {
		return len(lines) > 0 && strings.Contains(lines[0], "2:")
	}, "tab 2 to appear", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Switch back to tab 1 (mousehelper)
	pio.Write([]byte("\x021"))
	pio.WaitForSilence(200 * time.Millisecond)

	// Mouse should still work after switching back
	pio.Write([]byte(fmt.Sprintf("%c[<0;10;4M", ansi.ESC)))
	pio.WaitFor(t, "MOUSE press 0 10 3", 5*time.Second)

	// Quit the helper
	pio.Write([]byte("q"))
	pio.WaitFor(t, "termd$", 10*time.Second)
}

func TestScrollbackBuffer(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Spawn a region and generate enough output to fill scrollback
	regionID := spawnRegion(t, socketPath, "shell")

	// Wait for shell prompt
	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()
	pio.WaitFor(t, "termd$",10*time.Second)

	// Output 200 lines — in a 24-row terminal, early lines scroll off
	pio.Write([]byte("seq 1 200\r"))
	pio.WaitFor(t, "termd$",10*time.Second)

	// Poll scrollback via termctl until early numbers are present.
	// The server's terminal emulator may still be processing output
	// even after the frontend shows the prompt.
	want := []string{"1", "2", "3", "10", "50"}
	deadline := time.After(10 * time.Second)
	for {
		out := runTermctl(t, socketPath, "region", "scrollback", regionID)
		lines := strings.Split(strings.TrimSpace(out), "\n")

		found := make(map[string]bool)
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			for _, w := range want {
				if trimmed == w {
					found[w] = true
				}
			}
		}
		allFound := true
		for _, w := range want {
			if !found[w] {
				allFound = false
				break
			}
		}
		if allFound {
			return
		}

		select {
		case <-deadline:
			for _, w := range want {
				if !found[w] {
					t.Errorf("scrollback missing line %q (got %d lines total)", w, len(lines))
				}
			}
			return
		default:
			runtime.Gosched()
		}
	}
}

func TestScrollbackNavigation(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Generate enough output to fill scrollback
	pio.Write([]byte("seq 1 200\r"))
	pio.WaitFor(t, "termd$",10*time.Second)

	// Enter scrollback mode with ctrl+b [
	pio.Write([]byte{0x02, '['})

	// Tab bar should show "scrollback"
	pio.WaitForScreen(t, func(lines []string) bool {
		return strings.Contains(lines[0], "scrollback")
	}, "scrollback indicator in tab bar", 5*time.Second)

	// Page up several times to reach early numbers
	for range 20 {
		pio.Write([]byte{0x15}) // ctrl+u = page up
		time.Sleep(30 * time.Millisecond)
	}

	// Verify early numbers appear on screen
	pio.WaitForScreen(t, func(lines []string) bool {
		for _, line := range lines[1:] { // skip tab bar
			trimmed := strings.TrimSpace(line)
			if trimmed == "1" || trimmed == "2" || trimmed == "3" {
				return true
			}
		}
		return false
	}, "early numbers (1/2/3) visible on screen", 5*time.Second)

	// Exit scrollback with q
	pio.Write([]byte("q"))

	// Tab bar should no longer show "scrollback" and prompt should be visible
	pio.WaitForScreen(t, func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "termd$") {
				return true
			}
		}
		return false
	}, "prompt visible, scrollback gone from tab bar", 5*time.Second)
}

func TestScrollbackScrollWheel(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Generate output that scrolls off screen
	pio.Write([]byte("seq 1 200\r"))
	pio.WaitFor(t, "termd$",10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Send a scroll wheel up event to activate scrollback
	pio.Write([]byte(fmt.Sprintf("%c[<64;5;5M", ansi.ESC)))

	// Wait for scrollback data to arrive (not just mode activation)
	pio.WaitForScreen(t, func(lines []string) bool {
		// Tab bar should show scrollback with non-zero total
		return strings.Contains(lines[0], "scrollback") &&
			!strings.Contains(lines[0], "/0]")
	}, "scrollback data loaded", 5*time.Second)

	// Send more scroll wheel up events to scroll to the top
	for range 70 {
		pio.Write([]byte(fmt.Sprintf("%c[<64;5;5M", ansi.ESC)))
		time.Sleep(20 * time.Millisecond)
	}

	// Verify early numbers appear on screen
	pio.WaitForScreen(t, func(lines []string) bool {
		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "1" || trimmed == "2" || trimmed == "3" {
				return true
			}
		}
		return false
	}, "early numbers visible via scroll wheel", 5*time.Second)

	// Scroll wheel down past offset 0 to auto-exit scrollback
	for range 80 {
		pio.Write([]byte(fmt.Sprintf("%c[<65;5;5M", ansi.ESC)))
		time.Sleep(20 * time.Millisecond)
	}

	// Verify scrollback exited and prompt is visible
	pio.WaitForScreen(t, func(lines []string) bool {
		if strings.Contains(lines[0], "scrollback") {
			return false
		}
		for _, line := range lines {
			if strings.Contains(line, "termd$") {
				return true
			}
		}
		return false
	}, "prompt visible after scroll down exit", 5*time.Second)
}

func TestWebSocketTransport(t *testing.T) {
	socketPath, addrs, serverCleanup := startServerWithListeners(t, "ws://127.0.0.1:0")
	defer serverCleanup()

	var wsAddr string
	for _, a := range addrs {
		wsAddr = a
	}
	if wsAddr == "" {
		t.Fatal("could not find WS listen address")
	}

	// Spawn a region via Unix socket
	_ = runTermctl(t, socketPath, "region", "spawn", "shell")

	// Connect frontend via WebSocket
	cmd := exec.Command("termd-tui", "--socket", "ws://"+wsAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via WS: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	pio.Write([]byte("echo ws_works\r"))
	pio.WaitFor(t, "ws_works", 10*time.Second)
}

func TestSSHTransport(t *testing.T) {
	dir := t.TempDir()
	hostKeyPath := filepath.Join(dir, "host_key")

	env := testEnv(t)
	writeTestServerConfig(t, env)

	// Start server with Unix + SSH (no auth keys = accept all for test)
	socketPath := filepath.Join(dir, "termd.sock")
	cmd := exec.Command("termd",
		"--ssh-host-key", hostKeyPath,
		"--ssh-no-auth",
		"unix:"+socketPath, "ssh://127.0.0.1:0")
	cmd.Env = env
	stderrR, stderrW, _ := os.Pipe()
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	stderrW.Close()

	// Parse SSH listen address from stderr
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
		stderrR.Close()
	}()

	var sshAddr string
	deadline := time.Now().Add(5 * time.Second)
	found := 0
	for found < 2 && time.Now().Before(deadline) {
		select {
		case line := <-lines:
			if idx := strings.Index(line, "addr="); idx >= 0 {
				addr := line[idx+len("addr="):]
				if sp := strings.IndexByte(addr, ' '); sp >= 0 {
					addr = addr[:sp]
				}
				if strings.Contains(addr, ":") {
					sshAddr = addr
				}
				found++
			}
		case <-time.After(5 * time.Second):
		}
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	if sshAddr == "" {
		t.Fatal("could not find SSH listen address")
	}

	// Spawn a region via Unix socket
	_ = runTermctl(t, socketPath, "region", "spawn", "shell")

	// Connect frontend via SSH
	feCmd := exec.Command("termd-tui", "--socket", "ssh://"+sshAddr, )
	feCmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(feCmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via SSH: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { feCmd.Process.Kill(); feCmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	pio.Write([]byte("echo ssh_works\r"))
	pio.WaitFor(t, "ssh_works", 10*time.Second)
}

func TestRegionKilledExternally(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Get the region ID
	out := runTermctl(t, socketPath, "region", "list")
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

	// Kill the region externally
	runTermctl(t, socketPath, "region", "kill", regionID)

	// Wait for the frontend's PTY to close (same pattern as TestPrefixKeyDetach)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for frontend to exit after region kill")
		case _, ok := <-pio.ch:
			if !ok {
				return
			}
		}
	}
}

func TestReconnectUnix(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Type a marker so we can verify content persists
	pio.Write([]byte("echo reconnect_marker\r"))
	pio.WaitFor(t, "reconnect_marker", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)

	// Find the frontend's client ID
	clientID := findFrontendClientID(t, socketPath)

	// Kill the client connection
	runTermctl(t, socketPath, "client", "kill", clientID)

	// Should see "reconnecting..." in the tab bar
	pio.WaitFor(t, "reconnecting", 10*time.Second)

	// Should reconnect and show the prompt again
	pio.WaitFor(t, "termd$",10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Verify typing still works after reconnect
	pio.Write([]byte("echo after_reconnect\r"))
	pio.WaitFor(t, "after_reconnect", 10*time.Second)
}

func TestReconnectTCP(t *testing.T) {
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Connect frontend via TCP
	cmd := exec.Command("termd-tui", "--socket", "tcp:"+tcpAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend via TCP: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	pio.WaitFor(t, "termd$",10*time.Second)

	// Find the frontend's client ID (use Unix socket for termctl)
	clientID := findFrontendClientID(t, socketPath)

	// Kill the client connection
	runTermctl(t, socketPath, "client", "kill", clientID)

	// Should reconnect
	pio.WaitFor(t, "reconnecting", 10*time.Second)
	pio.WaitFor(t, "termd$",10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Verify typing works
	pio.Write([]byte("echo tcp_reconnected\r"))
	pio.WaitFor(t, "tcp_reconnected", 10*time.Second)
}

// findFrontendClientID returns the client ID of the termd-tui process.
func findFrontendClientID(t *testing.T, socketPath string) string {
	t.Helper()
	out := runTermctl(t, socketPath, "client", "list")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "termd-tui") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	t.Fatal("could not find termd-tui client ID")
	return ""
}

func TestMultiTransportSharedRegion(t *testing.T) {
	socketPath, tcpAddr, serverCleanup := startServerWithTCP(t)
	defer serverCleanup()

	// Start frontend 1 via Unix socket
	pio1, cleanup1 := startFrontend(t, socketPath)
	defer cleanup1()

	pio1.WaitFor(t, "termd$",10*time.Second)

	// Type a marker in frontend 1
	pio1.Write([]byte("echo multi_transport_marker\r"))
	pio1.WaitFor(t, "multi_transport_marker", 10*time.Second)

	// Start frontend 2 via TCP (subscribes to the same region)
	cmd := exec.Command("termd-tui", "--socket", "tcp:"+tcpAddr, )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend 2 via TCP: %v", err)
	}
	pio2 := newPtyIO(ptmx, 80, 24)
	defer func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }()

	// Frontend 2 should see the marker (it gets the screen snapshot on subscribe)
	pio2.WaitFor(t, "multi_transport_marker", 10*time.Second)

	// Type in frontend 2, verify frontend 1 sees it
	pio2.WaitFor(t, "termd$",10*time.Second)
	pio2.Write([]byte("echo from_tcp_client\r"))
	pio1.WaitFor(t, "from_tcp_client", 10*time.Second)
}

func TestExit(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.WaitFor(t, "termd$",10*time.Second)
	pio.Write([]byte("exit\r"))

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for frontend to exit after 'exit' command")
		case _, ok := <-pio.ch:
			if !ok {
				return
			}
		}
	}
}

func TestActiveTabBold(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

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
	pio.WaitFor(t, "termd$", 10*time.Second)
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

func TestConnectPicksUpExistingRegions(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Pre-create two regions via termctl before the frontend connects.
	spawnRegion(t, socketPath, "shell")
	spawnRegion(t, socketPath, "shell")

	// Now start the frontend — it should enumerate both regions as tabs.
	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:") && strings.Contains(lines[0], "2:")
	}, "tab bar with two pre-existing regions", 10*time.Second)
}

func TestSpawnSecondRegion(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// Wait for initial tab and prompt
	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// ctrl+b c to spawn a second region
	pio.Write([]byte("\x02c"))

	// Wait for tab bar to show both tabs
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && strings.Contains(lines[0], "2:bash")
	}, "tab bar with '1:bash' and '2:bash'", 10*time.Second)

	// New tab should have a prompt
	pio.WaitFor(t, "termd$", 10*time.Second)
}

func TestSwitchTabs(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Type a marker in tab 1
	pio.Write([]byte("echo TAB1_MARKER\r"))
	pio.WaitFor(t, "TAB1_MARKER", 10*time.Second)

	// Spawn second region
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Type a marker in tab 2
	pio.Write([]byte("echo TAB2_MARKER\r"))
	pio.WaitFor(t, "TAB2_MARKER", 10*time.Second)

	// Switch to tab 1
	pio.Write([]byte("\x021"))

	// Tab 1 content should be restored (subscribe sends screen snapshot)
	pio.WaitFor(t, "TAB1_MARKER", 10*time.Second)

	// TAB2_MARKER should NOT be on screen
	lines := pio.ScreenLines()
	for _, line := range lines {
		if strings.Contains(line, "TAB2_MARKER") {
			t.Fatalf("TAB2_MARKER should not be visible on tab 1")
		}
	}
}

func TestInputIsolation(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Type a marker in tab 1 so we can identify its screen
	pio.Write([]byte("echo TAB1_HERE\r"))
	pio.WaitFor(t, "TAB1_HERE", 10*time.Second)

	// Spawn second region
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Type in tab 2
	pio.Write([]byte("echo ONLY_IN_TAB2\r"))
	pio.WaitFor(t, "ONLY_IN_TAB2", 10*time.Second)

	// Switch to tab 1 and wait for tab 1's content to appear
	pio.Write([]byte("\x021"))

	// Wait for tab 1 screen: must have TAB1_HERE and must NOT have ONLY_IN_TAB2
	pio.WaitForScreen(t, func(lines []string) bool {
		hasTab1 := false
		for _, line := range lines {
			if strings.Contains(line, "TAB1_HERE") {
				hasTab1 = true
			}
			if strings.Contains(line, "ONLY_IN_TAB2") {
				return false
			}
		}
		return hasTab1
	}, "tab 1 screen with TAB1_HERE and without ONLY_IN_TAB2", 10*time.Second)

	// Switch back to tab 2 and verify content is there
	pio.Write([]byte("\x022"))
	pio.WaitFor(t, "ONLY_IN_TAB2", 10*time.Second)
}

func TestRegionDestroyedRemovesTab(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Spawn second region
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Exit the shell in tab 2
	pio.Write([]byte("exit\r"))

	// Wait for tab bar to show only tab 1 (tab 2 removed)
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && !strings.Contains(lines[0], "2:bash")
	}, "tab bar with only '1:bash'", 10*time.Second)

	// Verify terminal is still functional
	pio.WaitFor(t, "termd$", 10*time.Second)
	pio.Write([]byte("echo ALIVE\r"))
	pio.WaitFor(t, "ALIVE", 10*time.Second)
}

func TestCloseTab(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Spawn second region
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Close tab 2 with ctrl+b x
	pio.Write([]byte("\x02x"))

	// Wait for tab bar to show only tab 1
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && !strings.Contains(lines[0], "2:bash")
	}, "tab bar with only '1:bash'", 10*time.Second)

	// Verify terminal is still functional
	pio.WaitFor(t, "termd$", 10*time.Second)
	pio.Write([]byte("echo STILL_ALIVE\r"))
	pio.WaitFor(t, "STILL_ALIVE", 10*time.Second)
}

func TestReconnectRestoresTabs(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Spawn a second region
	pio.Write([]byte("\x02c"))
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && strings.Contains(lines[0], "2:bash")
	}, "tab bar with '1:bash' and '2:bash'", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Kill the client connection to force reconnect
	clientID := findFrontendClientID(t, socketPath)
	runTermctl(t, socketPath, "client", "kill", clientID)

	// Wait for reconnecting then reconnected
	pio.WaitFor(t, "reconnecting", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Both tabs should be restored after reconnect
	pio.WaitForScreen(t, func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:bash") && strings.Contains(lines[0], "2:bash")
	}, "both tabs restored after reconnect", 10*time.Second)
}

func TestAllRegionsDestroyedQuits(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendFull(t, socketPath)
	defer fe.Kill()

	fe.WaitFor(t, "1:bash", 10*time.Second)
	fe.WaitFor(t, "termd$", 10*time.Second)

	// Exit the only shell
	fe.Write([]byte("exit\r"))

	// Frontend should exit
	if err := fe.Wait(10 * time.Second); err != nil {
		t.Fatalf("frontend did not exit cleanly: %v", err)
	}
}

func TestHelpOverlay(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)
	pio.WaitForSilence(200 * time.Millisecond)

	// Open help overlay: ctrl+b ?
	pio.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	pio.Write([]byte("?"))

	// Wait for the help overlay to render with keybinding content.
	pio.WaitFor(t, "detach", 5*time.Second)

	lines := pio.ScreenLines()
	// Should show category headers and keybindings.
	foundMain := false
	foundDetach := false
	foundTab := false
	for _, line := range lines {
		if strings.Contains(line, "main") {
			foundMain = true
		}
		if strings.Contains(line, "detach") {
			foundDetach = true
		}
		if strings.Contains(line, "tab") {
			foundTab = true
		}
	}
	if !foundMain {
		t.Error("help overlay should show 'main' category")
	}
	if !foundDetach {
		t.Error("help overlay should show 'detach' command")
	}
	if !foundTab {
		t.Error("help overlay should show 'tab' category")
	}

	// Close with q.
	pio.Write([]byte("q"))
	pio.WaitFor(t, "termd$", 5*time.Second)
}

// ── Keybinding tests ────────────────────────────────────────────────

func TestKeybindNativeNextPrevTab(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Mark tab 1
	pio.Write([]byte("echo TAB1_NATIVE\r"))
	pio.WaitFor(t, "TAB1_NATIVE", 10*time.Second)

	// Spawn second tab (ctrl+b c)
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "2:bash", 10*time.Second)
	pio.WaitFor(t, "termd$", 10*time.Second)

	// Mark tab 2
	pio.Write([]byte("echo TAB2_NATIVE\r"))
	pio.WaitFor(t, "TAB2_NATIVE", 10*time.Second)

	// Alt+, (prev-tab) → should go back to tab 1
	pio.Write([]byte("\x1b,"))
	pio.WaitFor(t, "TAB1_NATIVE", 10*time.Second)

	// Alt+. (next-tab) → should go back to tab 2
	pio.Write([]byte("\x1b."))
	pio.WaitFor(t, "TAB2_NATIVE", 10*time.Second)
}

func TestKeybindTmuxStyle(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	writeTestKeybindConfig(t, env, `style = "tmux"`)

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "1:bash", 10*time.Second)
	fe.WaitFor(t, "termd$", 10*time.Second)

	// Mark tab 1
	fe.Write([]byte("echo TAB1_TMUX\r"))
	fe.WaitFor(t, "TAB1_TMUX", 10*time.Second)

	// Spawn second tab (ctrl+b c — same as tmux)
	fe.Write([]byte("\x02c"))
	fe.WaitFor(t, "2:bash", 10*time.Second)
	fe.WaitFor(t, "termd$", 10*time.Second)

	// Mark tab 2
	fe.Write([]byte("echo TAB2_TMUX\r"))
	fe.WaitFor(t, "TAB2_TMUX", 10*time.Second)

	// ctrl+b p (prev-tab in tmux) → should go to tab 1
	fe.Write([]byte("\x02p"))
	fe.WaitFor(t, "TAB1_TMUX", 10*time.Second)

	// ctrl+b n (next-tab in tmux) → should go to tab 2
	fe.Write([]byte("\x02n"))
	fe.WaitFor(t, "TAB2_TMUX", 10*time.Second)
}

func TestKeybindScreenPrefix(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	writeTestKeybindConfig(t, env, `style = "screen"`)

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "1:bash", 10*time.Second)
	fe.WaitFor(t, "termd$", 10*time.Second)

	// ctrl+a d (detach in screen style; ctrl+a = 0x01)
	fe.Write([]byte("\x01d"))

	// Frontend should exit with detach
	if err := fe.Wait(10 * time.Second); err != nil {
		t.Fatalf("frontend did not exit after screen-style detach: %v", err)
	}
}

func TestKeybindCustomOverride(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	// Rebind ctrl+b x from close-tab to detach
	writeTestKeybindConfig(t, env, "style = \"native\"\n\n[main]\ndetach = [\"d\", \"x\"]\n")

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "1:bash", 10*time.Second)
	fe.WaitFor(t, "termd$", 10*time.Second)

	// ctrl+b x should now detach (instead of closing the tab)
	fe.Write([]byte("\x02x"))

	if err := fe.Wait(10 * time.Second); err != nil {
		t.Fatalf("frontend did not exit after override detach: %v", err)
	}
}

func TestKeybindNextPrevSession(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	// Use tmux style which has ) and ( for next/prev session
	writeTestKeybindConfig(t, env, `style = "tmux"`)

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "1:bash", 10*time.Second)
	fe.WaitFor(t, "termd$", 10*time.Second)

	// Mark session 1
	fe.Write([]byte("echo SESSION1_MARK\r"))
	fe.WaitFor(t, "SESSION1_MARK", 10*time.Second)

	// Create a second session: ctrl+b $ (open-session in tmux)
	fe.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	fe.Write([]byte("$"))
	fe.WaitFor(t, "Session name:", 5*time.Second)
	fe.WaitForSilence(200 * time.Millisecond)
	fe.Write([]byte("dev"))
	time.Sleep(100 * time.Millisecond)
	fe.Write([]byte("\r"))
	// Wait for the new session to connect (status shows "dev@...")
	fe.WaitFor(t, "dev@", 10*time.Second)
	fe.WaitFor(t, "termd$", 10*time.Second)
	fe.WaitForSilence(200 * time.Millisecond)

	// Mark session 2
	fe.Write([]byte("echo SESSION2_MARK\r"))
	fe.WaitFor(t, "SESSION2_MARK", 10*time.Second)

	// ctrl+b ( (prev-session in tmux) → should go to session 1
	fe.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	fe.Write([]byte("("))
	fe.WaitFor(t, "SESSION1_MARK", 10*time.Second)

	// ctrl+b ) (next-session in tmux) → should go to session 2
	fe.Write([]byte{0x02})
	time.Sleep(50 * time.Millisecond)
	fe.Write([]byte(")"))
	fe.WaitFor(t, "SESSION2_MARK", 10*time.Second)
}
