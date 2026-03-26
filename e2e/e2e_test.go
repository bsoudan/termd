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

	// The frontend should show the tab bar with "bash" and some terminal content.
	// Wait for "bash" to appear (it's the region name shown in the tab bar).
	output := pio.WaitFor(t, "bash", 10*time.Second)

	// Verify we got some content (not just the tab bar)
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines of output, got %d", len(lines))
	}
}

func TestInputRoundTrip(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// Wait for the frontend to be ready (tab bar with "bash")
	pio.WaitFor(t, "bash", 10*time.Second)

	// Send a command that decodes base64 remotely.
	// "aGVsbG8K" is base64 for "hello\n".
	// The command text itself never contains "hello", so if we see
	// "hello" in the output, it must be the decoded program output.
	pio.Write([]byte("echo aGVsbG8K | base64 -d\r"))

	// Wait for the decoded "hello" to appear
	output := pio.WaitFor(t, "hello", 10*time.Second)
	t.Logf("saw 'hello' in output (len=%d)", len(output))
}

func TestResize(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// Wait for initial render
	pio.WaitFor(t, "bash", 10*time.Second)

	// Send a command that prints the terminal width
	pio.Write([]byte("tput cols\r"))

	// The initial PTY is 80 columns, so we should see "80" in the output.
	// Use a unique marker to avoid matching other numbers.
	pio.WaitFor(t, "80", 10*time.Second)
}

func TestCursorPosition(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// Wait for the bash prompt to render.
	pio.WaitFor(t, ">", 10*time.Second)

	// Check the initial cursor position (before any typing).
	// The last rendered frame should have the cursor at the prompt, not (0,0).
	initRaw := pio.buf.String()
	initRow, initCol := findCursorPosition(t, initRaw)
	t.Logf("initial cursor (before typing): row=%d col=%d", initRow, initCol)
	if initCol == 0 {
		t.Fatalf("initial cursor col is 0 — cursor stuck at default position")
	}

	// Type "a" — this triggers a screen_update with the cursor after the "a".
	// Clear the buffer first so we only see fresh frames.
	pio.buf.Reset()
	pio.Write([]byte("a"))
	raw := pio.WaitForRaw(t, "\x1b[7m", 10*time.Second)
	row1, col1 := findCursorPosition(t, raw)
	t.Logf("after typing 'a': cursor at row=%d col=%d", row1, col1)

	// The cursor must not be at col 0 — "a" was typed after the prompt,
	// so the cursor should be past the prompt text + 1 character.
	if col1 == 0 {
		t.Fatalf("cursor col is 0 after typing 'a' — cursor position not updating")
	}

	// Type "b" — cursor should advance by exactly 1 column.
	pio.buf.Reset()
	pio.Write([]byte("b"))
	raw = pio.WaitForRaw(t, "\x1b[7m", 10*time.Second)
	row2, col2 := findCursorPosition(t, raw)
	t.Logf("after typing 'b': cursor at row=%d col=%d", row2, col2)

	if row2 != row1 || col2 != col1+1 {
		t.Fatalf("expected cursor to advance from (%d,%d) to (%d,%d), got (%d,%d)",
			row1, col1, row1, col1+1, row2, col2)
	}
}

// findCursorPosition locates the reverse-video cursor (\x1b[7m...\x1b[27m)
// in the raw PTY output and returns its (row, col) within the content area.
// Row 0 of the PTY output is the tab bar; content starts at row 1.
// Returns content-area row (0-indexed from content start) and column.
func findCursorPosition(t *testing.T, raw string) (row, col int) {
	t.Helper()

	// Split raw output into lines. The last complete frame is what matters.
	// Frames start with \x1b[H (cursor home). Find the last one.
	frames := strings.Split(raw, "\x1b[H")
	if len(frames) < 2 {
		// No \x1b[H found — bubbletea uses different positioning.
		// Fall back to scanning the whole output.
		return findCursorInFrame(t, raw)
	}
	lastFrame := frames[len(frames)-1]
	return findCursorInFrame(t, lastFrame)
}

func findCursorInFrame(t *testing.T, frame string) (row, col int) {
	t.Helper()

	// Split into screen rows (newlines separate rows in the rendered frame)
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		idx := strings.Index(line, "\x1b[7m")
		if idx < 0 {
			continue
		}
		// Column = number of visible characters before the cursor marker.
		// Strip ANSI sequences from everything before idx to get the visual column.
		prefix := line[:idx]
		visCol := len([]rune(stripAnsi(prefix)))
		// Row i in the frame: row 0 is tab bar, content rows start at 1.
		// Return content-area row (subtract 1 for tab bar).
		contentRow := i - 1
		if contentRow < 0 {
			contentRow = 0
		}
		return contentRow, visCol
	}

	t.Fatalf("no cursor (\\x1b[7m) found in frame")
	return 0, 0
}

func TestRawInputPassthrough(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	// Start a long-running command
	pio.Write([]byte("sleep 999\r"))

	// Send ctrl+c (raw \x03). With raw input passthrough, this goes
	// directly to the PTY and kills sleep. With the old keyToBytes
	// approach, ctrl+c would have quit the frontend instead.
	pio.Write([]byte("\x03"))

	// Bash should show a new prompt after SIGINT kills sleep.
	// Type a command to verify the frontend is still alive.
	pio.Write([]byte("echo raw_input_works\r"))
	pio.WaitFor(t, "raw_input_works", 10*time.Second)
}

func TestPrefixKeyDetach(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	// Send ctrl+b then d — should detach (frontend exits cleanly).
	pio.Write([]byte{0x02}) // ctrl+b
	pio.Write([]byte("d"))

	// Wait for PTY to close (frontend exited).
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for frontend to exit after ctrl+b d")
		case _, ok := <-pio.ch:
			if !ok {
				return // success
			}
		}
	}
}

func TestPrefixKeyLiteralCtrlB(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, ">", 10*time.Second)

	// Use cat to echo input. Send ctrl+b ctrl+b — the double prefix should
	// send a literal ctrl+b (0x02) to the program. cat -v shows it as ^B.
	pio.Write([]byte("cat -v\r"))
	pio.WaitFor(t, "cat -v", 10*time.Second)

	pio.buf.Reset()
	pio.Write([]byte{0x02, 0x02}) // ctrl+b ctrl+b = send literal ctrl+b
	pio.WaitFor(t, "^B", 10*time.Second)

	// Send ctrl+c to exit cat (raw passthrough, not intercepted)
	pio.Write([]byte("\x03"))
	pio.WaitFor(t, ">", 10*time.Second)
}

func TestPrefixKeyStatusIndicator(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	// Wait until the system is truly idle (no new PTY output for 500ms).
	pio.WaitForSilence(500 * time.Millisecond)

	// Now send ctrl+b only. The status indicator should appear immediately.
	pio.buf.Reset()
	pio.Write([]byte{0x02})
	pio.WaitFor(t, "ctrl+b ...", 3*time.Second)

	// Send an unrecognized key to dismiss the prefix mode.
	// The status should disappear on the next render.
	pio.buf.Reset()
	pio.Write([]byte("x"))

	// Type a command to trigger a re-render, then verify "ctrl+b ..." is gone.
	pio.Write([]byte("echo prefix_cleared\r"))
	output := pio.WaitFor(t, "prefix_cleared", 10*time.Second)

	if strings.Contains(output, "ctrl+b ...") {
		t.Fatal("prefix status indicator still visible after prefix mode ended")
	}
}

func TestLogViewerOverlay(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// Start with --debug to fill the log buffer (reproduces overflow bug)
	cmd := exec.Command("termd-frontend", "--socket", socketPath, "--debug")
	// See startFrontend for why TERM=dumb.
	cmd.Env = append(os.Environ(), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend: %v", err)
	}
	pio := newPtyIO(ptmx)
	frontendCleanup := func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }
	defer frontendCleanup()

	pio.WaitFor(t, "bash", 10*time.Second)

	// Open log viewer: ctrl+b l
	pio.WaitForSilence(500 * time.Millisecond)
	pio.buf.Reset()
	pio.Write([]byte{0x02})
	pio.Write([]byte("l"))

	// The overlay must fit on screen. Verify:
	// 1. The top border (╭) appears
	// 2. The bottom border (╰) appears
	// 3. The help text appears
	// All three must be visible — if the overlay is clipped, one or more
	// will be missing.
	raw := pio.WaitForRaw(t, "\xe2\x95\xad", 5*time.Second) // ╭ = top border
	if !strings.Contains(raw, "\xe2\x95\xb0") {              // ╰ = bottom border
		t.Fatal("bottom border not visible — overlay is taller than screen")
	}
	if !strings.Contains(raw, "q/esc: close") {
		t.Fatal("help text not visible — overlay is taller than screen")
	}

	// Verify the overlay height: count lines between top and bottom border
	rawLines := strings.Split(raw, "\n")
	topLine := -1
	bottomLine := -1
	for i, line := range rawLines {
		if strings.Contains(line, "\xe2\x95\xad") {
			topLine = i
		}
		if strings.Contains(line, "\xe2\x95\xb0") {
			bottomLine = i
		}
	}
	if topLine >= 0 && bottomLine >= 0 {
		overlayHeight := bottomLine - topLine + 1
		t.Logf("overlay rendered: lines %d-%d (%d lines), screen height=24", topLine, bottomLine, overlayHeight)
		if overlayHeight > 24 {
			t.Fatalf("overlay height %d exceeds screen height 24", overlayHeight)
		}
	}

	// Close and verify normal input resumes
	pio.Write([]byte("q"))
	pio.WaitFor(t, ">", 10*time.Second)
	pio.Write([]byte("echo logview_closed\r"))
	pio.WaitFor(t, "logview_closed", 10*time.Second)
}

func TestSessionPersistence(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	// First frontend: type a unique marker, then detach.
	pio1, cleanup1 := startFrontend(t, socketPath)

	pio1.WaitFor(t, "bash", 10*time.Second)
	pio1.Write([]byte("echo persistence_marker_12345\r"))
	pio1.WaitFor(t, "persistence_marker_12345", 10*time.Second)

	// Detach (ctrl+b d)
	pio1.Write([]byte{0x02})
	pio1.Write([]byte("d"))

	// Wait for first frontend to exit
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

	// Second frontend: should reconnect to the existing region and see
	// the marker in the terminal scrollback/screen.
	pio2, cleanup2 := startFrontend(t, socketPath)
	defer cleanup2()

	pio2.WaitFor(t, "persistence_marker_12345", 10*time.Second)
}

func TestExit(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	// Wait for prompt
	pio.WaitFor(t, "bash", 10*time.Second)

	// Type "exit" to close bash — this should trigger region_destroyed
	// and the frontend should quit.
	pio.Write([]byte("exit\r"))

	// Wait for the PTY to close (readLoop will close the channel).
	// We detect this by waiting for "region destroyed" error message
	// or by the channel closing within the timeout.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for frontend to exit after 'exit' command")
		case data, ok := <-pio.ch:
			if !ok {
				// PTY closed — frontend exited. Success.
				return
			}
			pio.buf.Write(data)
		}
	}
}
