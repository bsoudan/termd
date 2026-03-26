package e2e

import (
	"os"
	"os/exec"
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
