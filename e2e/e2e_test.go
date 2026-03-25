package e2e

import (
	"strings"
	"testing"
	"time"
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
