package main

import (
	"strings"
	"testing"

	te "nxtermd/pkg/te"
)

// feedChunks simulates readLoop by feeding data through sequenceSafe and then
// into a go-te Stream, one chunk at a time.
func feedChunks(stream *te.Stream, chunks [][]byte) {
	var carry [maxCarry]byte
	var carryN int
	for _, chunk := range chunks {
		data, cn := sequenceSafe(carry[:carryN], chunk, carry[:])
		carryN = cn
		if len(data) > 0 {
			stream.FeedBytes(data)
		}
	}
	// Flush any remaining carry (simulates EOF).
	if carryN > 0 {
		stream.FeedBytes(carry[:carryN])
	}
}

func screenLine(screen *te.Screen, row int) string {
	display := screen.Display()
	if row >= len(display) {
		return ""
	}
	return strings.TrimRight(display[row], " ")
}

// TestSplitUTF8DirectFeedBug demonstrates the underlying go-te bug: feeding
// split UTF-8 directly to Stream.FeedBytes produces garbled output.
func TestSplitUTF8DirectFeedBug(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// Feed split UTF-8 directly without sequenceSafe.
	stream.FeedBytes([]byte{0xC3})
	stream.FeedBytes([]byte{0xA9})

	got := screenLine(screen, 0)
	if got == "é" {
		t.Skip("go-te handles split UTF-8 correctly now — sequenceSafe may no longer be needed for UTF-8")
	}
	// The bug: go-te renders two garbage characters instead of "é".
	t.Logf("confirmed go-te bug: split UTF-8 rendered as %q (% x) instead of %q", got, []byte(got), "é")
}

func TestSplitUTF8(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// "é" is U+00E9 = 0xC3 0xA9 in UTF-8. Split across two chunks.
	feedChunks(stream, [][]byte{
		{0xC3},
		{0xA9},
	})

	got := screenLine(screen, 0)
	if got != "é" {
		t.Errorf("split 2-byte UTF-8: got %q (% x), want %q", got, []byte(got), "é")
	}
}

func TestSplitUTF8ThreeByte(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// "€" is U+20AC = 0xE2 0x82 0xAC in UTF-8.
	// Split after first byte.
	feedChunks(stream, [][]byte{
		{0xE2},
		{0x82, 0xAC},
	})

	got := screenLine(screen, 0)
	if got != "€" {
		t.Errorf("split 3-byte UTF-8: got %q (% x), want %q", got, []byte(got), "€")
	}
}

func TestSplitUTF8FourByte(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// "𝄞" (musical symbol) is U+1D11E = 0xF0 0x9D 0x84 0x9E.
	// Split after second byte.
	feedChunks(stream, [][]byte{
		{0xF0, 0x9D},
		{0x84, 0x9E},
	})

	got := screenLine(screen, 0)
	if got != "𝄞" {
		t.Errorf("split 4-byte UTF-8: got %q (% x), want %q", got, []byte(got), "𝄞")
	}
}

func TestSplitCSICursorPosition(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// Write "X" at position (row 3, col 5) using CSI H: \x1b[3;5H
	// Split the escape sequence mid-parameters.
	feedChunks(stream, [][]byte{
		[]byte("\x1b[3;"),
		[]byte("5HX"),
	})

	got := screenLine(screen, 2) // row 3 is index 2
	// Column 5 is index 4, so we expect 4 spaces then X.
	want := "    X"
	if got != want {
		t.Errorf("split CSI cursor position: got %q, want %q", got, want)
	}
}

func TestSplitOSCTitle(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// OSC 0 (set title): \x1b]0;hello\x1b\\
	// Split mid-payload. The title shouldn't appear on screen.
	// Then write "A" so we can verify the screen is clean.
	feedChunks(stream, [][]byte{
		[]byte("\x1b]0;hel"),
		[]byte("lo\x1b\\A"),
	})

	got := screenLine(screen, 0)
	if got != "A" {
		t.Errorf("split OSC: got %q, want %q", got, "A")
	}
}

func TestSplitUTF8WithinEscapeSequence(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// Set cursor to row 1, col 1, then write "café".
	// The "é" (0xC3 0xA9) spans the chunk boundary.
	feedChunks(stream, [][]byte{
		[]byte("caf\xC3"),
		[]byte("\xA9"),
	})

	got := screenLine(screen, 0)
	if got != "café" {
		t.Errorf("UTF-8 within text: got %q (% x), want %q", got, []byte(got), "café")
	}
}

func TestNoSplitNeeded(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// Complete data in a single chunk — no splitting needed.
	feedChunks(stream, [][]byte{
		[]byte("hello world"),
	})

	got := screenLine(screen, 0)
	if got != "hello world" {
		t.Errorf("no split: got %q, want %q", got, "hello world")
	}
}

func TestSplitEscapeAtBoundary(t *testing.T) {
	screen := te.NewScreen(80, 24)
	stream := te.NewStream(screen, false)

	// ESC byte alone at end of chunk, CSI follows in next chunk.
	// \x1b[1mA\x1b[0m — bold "A" then reset.
	feedChunks(stream, [][]byte{
		{0x1b},
		[]byte("[1mA\x1b[0m"),
	})

	got := screenLine(screen, 0)
	if got != "A" {
		t.Errorf("ESC at boundary: got %q, want %q", got, "A")
	}
}
