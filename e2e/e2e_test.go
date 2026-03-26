package e2e

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestStartAndRender(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	lines := pio.WaitFor(t, "bash", 10*time.Second)

	// "bash" should be at the start of row 0 (tab bar, left-aligned)
	row, col := findOnScreen(lines, "bash")
	if row != 0 {
		t.Fatalf("expected 'bash' on row 0, found on row %d", row)
	}
	if col > 2 {
		t.Fatalf("expected 'bash' near start of row 0, found at col %d", col)
	}

	hasContent := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) != "" {
			hasContent = true
			break
		}
	}
	if !hasContent {
		t.Fatal("no content below the tab bar")
	}
}

func TestInputRoundTrip(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)
	pio.WaitFor(t, "termd$ ", 10*time.Second)

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
	pio.WaitFor(t, "termd$ ", 10*time.Second)
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
	pio.WaitFor(t, "termd$ ", 10*time.Second)

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
	shell := findShell(t)
	id := spawnRegion(t, socketPath, shell)

	// Type a marker
	runTermctl(t, socketPath, "region", "send", "-e", id, "echo alt_screen_marker\r")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runTermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "alt_screen_marker") {
			break
		}
		runtime.Gosched()
	}

	// Enter alt screen via less
	runTermctl(t, socketPath, "region", "send", "-e", id, "echo 'line1\\nline2\\nline3' | less\r")

	// Wait for less to show "line1" AND marker to be gone (alt screen active)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := runTermctl(t, socketPath, "region", "view", id)
		if strings.Contains(view, "line1") && !strings.Contains(view, "alt_screen_marker") {
			goto inAlt
		}
		runtime.Gosched()
	}
	t.Fatal("timeout waiting for less to enter alt screen")

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
		runtime.Gosched()
	}
	t.Fatal("timeout waiting for marker to reappear after less exits")
}

func TestScreenSyncAfterHeavyOutput(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$ ", 10*time.Second)

	// Run seq to fill the screen with output, then wait for prompt
	pio.Write([]byte("seq 1 50\r"))
	pio.WaitFor(t, "termd$ ", 10*time.Second)

	// Give a moment for all events to propagate
	pio.WaitForSilence(200 * time.Millisecond)

	// Get the server's screen via termctl
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

	// Get the frontend's screen (what the test's go-te sees)
	frontendLines := pio.ScreenLines()

	// Compare line by line (skip row 0 which is the tab bar on frontend)
	// The server has rows 0..height-1, the frontend has row 0 = tab bar + rows 1..height
	mismatches := 0
	for i := 0; i < len(serverLines) && i+1 < len(frontendLines); i++ {
		sLine := strings.TrimRight(serverLines[i], " ")
		fLine := strings.TrimRight(frontendLines[i+1], " ")
		if sLine != fLine {
			if mismatches < 5 {
				t.Logf("mismatch row %d:\n  server:   %q\n  frontend: %q", i, sLine, fLine)
			}
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Errorf("%d rows differ between server and frontend after seq output", mismatches)
	}
}

func TestScreenSyncAfterTop(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$ ", 10*time.Second)

	// Run top briefly then quit
	pio.Write([]byte("top\r"))
	time.Sleep(2 * time.Second)
	pio.Write([]byte("q"))

	// Wait for prompt to reappear
	pio.WaitFor(t, "termd$ ", 10*time.Second)
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
	pio.WaitFor(t, "termd$ ", 10*time.Second)

	// Type several lines so there's content on screen
	pio.Write([]byte("echo line_a\r"))
	pio.WaitFor(t, "line_a", 10*time.Second)
	pio.WaitFor(t, "termd$ ", 10*time.Second)
	pio.Write([]byte("echo line_b\r"))
	pio.WaitFor(t, "line_b", 10*time.Second)
	pio.WaitFor(t, "termd$ ", 10*time.Second)

	// Run a command that writes to many rows (like top does).
	// Use seq to fill several lines, then verify the prompt appears
	// AFTER the seq output, not overlapping it.
	pio.Write([]byte("seq 1 10\r"))
	pio.WaitFor(t, "10", 10*time.Second)
	pio.WaitFor(t, "termd$ ", 10*time.Second)

	// The prompt should be on a row AFTER "10"
	lines := pio.ScreenLines()
	seqRow, _ := findOnScreen(lines, "10")
	promptRow, _ := findOnScreen(lines, "termd$ ")
	t.Logf("'10' at row %d, last 'termd$ ' at row %d", seqRow, promptRow)

	// Find the LAST occurrence of "termd$ " (the current prompt)
	lastPrompt := -1
	for i, line := range lines {
		if strings.Contains(line, "termd$ ") {
			lastPrompt = i
		}
	}
	t.Logf("last prompt at row %d", lastPrompt)

	if lastPrompt <= seqRow {
		t.Fatalf("prompt (row %d) should be after seq output '10' (row %d)\nscreen:\n%s",
			lastPrompt, seqRow, strings.Join(lines, "\n"))
	}
}

func TestRawInputPassthrough(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.WaitFor(t, "termd$ ", 10*time.Second)
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

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.Write([]byte{0x02})
	pio.Write([]byte("d"))

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for frontend to exit after ctrl+b d")
		case _, ok := <-pio.ch:
			if !ok {
				return
			}
		}
	}
}

func TestPrefixKeyLiteralCtrlB(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "termd$ ", 10*time.Second)

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
	pio.WaitFor(t, "termd$ ", 10*time.Second)
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
		_, col := findOnScreen(lines, "ctrl+b ...")
		// Should be right-justified on row 0 — col should be > 50 on an 80-col screen
		return col > 50
	}, "'ctrl+b ...' right-justified on row 0", 3*time.Second)

	row, col := findOnScreen(lines, "ctrl+b ...")
	t.Logf("'ctrl+b ...' at row %d, col %d", row, col)
	if row != 0 {
		t.Fatalf("expected status on row 0, found on row %d", row)
	}

	// Dismiss and verify it clears
	pio.Write([]byte("x"))
	pio.Write([]byte("echo prefix_cleared\r"))
	pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "prefix_cleared")
		return row >= 0
	}, "'prefix_cleared' on screen", 10*time.Second)

	row0 := pio.ScreenLine(0)
	if strings.Contains(row0, "ctrl+b ...") {
		t.Fatal("prefix status still on row 0 after dismissal")
	}
}

func TestLogViewerOverlay(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	cmd := exec.Command("termd-frontend", "--socket", socketPath, "--debug", "--command", "bash --norc")
	cmd.Env = append(os.Environ(), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend: %v", err)
	}
	pio := newPtyIO(ptmx, 80, 24)
	frontendCleanup := func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.WaitForSilence(500 * time.Millisecond)
	pio.Write([]byte{0x02})
	pio.Write([]byte("l"))

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

	pio.WaitFor(t, "termd$ ", 10*time.Second)
	pio.Write([]byte("echo logview_closed\r"))
	pio.WaitFor(t, "logview_closed", 10*time.Second)
}

func TestSessionPersistence(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio1, cleanup1 := startFrontend(t, socketPath)

	pio1.WaitFor(t, "bash", 10*time.Second)
	pio1.Write([]byte("echo persistence_marker_12345\r"))
	pio1.WaitFor(t, "termd$ ", 10*time.Second)
	pio1.WaitFor(t, "persistence_marker_12345", 10*time.Second)

	pio1.Write([]byte{0x02})
	pio1.Write([]byte("d"))

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

	pio2, cleanup2 := startFrontend(t, socketPath)
	defer cleanup2()

	pio2.WaitFor(t, "persistence_marker_12345", 10*time.Second)
}

func TestExit(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	pio.WaitFor(t, "termd$ ", 10*time.Second)
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
