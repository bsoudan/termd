package tui

import (
	"bytes"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestSplitCompleteOSC2459(t *testing.T) {
	input := []byte("\x1b]2459;nx;sync;boot\x07")
	n := splitComplete(input)
	if n != len(input) {
		t.Errorf("splitComplete for OSC2459: got %d, want %d", n, len(input))
	}
}

func TestDecodeOSC2459(t *testing.T) {
	input := []byte("\x1b]2459;nx;sync;boot\x07")
	_, _, n, state := ansi.DecodeSequence(input, ansi.NormalState, nil)
	t.Logf("decoded n=%d state=%v len=%d", n, state, len(input))
	if n != len(input) {
		t.Errorf("DecodeSequence: consumed %d of %d", n, len(input))
	}
	if state != ansi.NormalState {
		t.Errorf("DecodeSequence: state %v, want NormalState", state)
	}
}

func TestIsCapabilityResponseOSC2459(t *testing.T) {
	seq := []byte("\x1b]2459;nx;sync;boot\x07")
	if isCapabilityResponse(seq) {
		t.Error("OSC 2459 should NOT be a capability response")
	}
}

func TestFilterCapabilityResponsesOSC2459(t *testing.T) {
	input := []byte("\x1b]2459;nx;sync;boot\x07")
	var cap bytes.Buffer
	rest := filterCapabilityResponses(input, &cap)
	if !bytes.Equal(rest, input) {
		t.Errorf("filter ate OSC 2459: got rest=%q cap=%q", rest, cap.Bytes())
	}
}

// ── isCapabilityResponse ────────────────────────────────────────────────────

func TestIsCapabilityResponse(t *testing.T) {
	tests := []struct {
		name string
		seq  []byte
		want bool
	}{
		// Capability responses — should be filtered.
		{"DECRPM mode 2026", []byte("\x1b[?2026;2$y"), true},
		{"DECRPM mode 2027", []byte("\x1b[?2027;3$y"), true},
		{"DA1", []byte("\x1b[?65;1;9c"), true},
		{"DA2", []byte("\x1b[>1;1;0c"), true},
		{"kitty keyboard query", []byte("\x1b[?1u"), true},
		{"DCS XTVERSION", []byte("\x1bP>|foot(1.19)\x1b\\"), true},
		{"OSC bg color", []byte("\x1b]11;rgb:1a1a/1a1a/1a1a\x1b\\"), true},
		{"OSC bg color BEL", []byte("\x1b]11;rgb:ff/ff/ff\x07"), true},

		// User input — should NOT be filtered.
		{"plain text", []byte("hello"), false},
		{"ctrl+b", []byte{0x02}, false},
		{"arrow up", []byte("\x1b[A"), false},
		{"arrow with modifier", []byte("\x1b[1;2A"), false},
		{"function key", []byte("\x1b[15~"), false},
		{"SGR mouse", []byte("\x1b[<0;10;5M"), false},
		{"kitty key event", []byte("\x1b[97u"), false},
		{"kitty key+mod", []byte("\x1b[97;5u"), false},
		{"lone ESC", []byte{0x1b}, false},
		{"short seq", []byte("\x1b["), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCapabilityResponse(tt.seq)
			if got != tt.want {
				t.Errorf("isCapabilityResponse(%q) = %v, want %v", tt.seq, got, tt.want)
			}
		})
	}
}

// ── filterCapabilityResponses ───────────────────────────────────────────────

func TestFilterCapabilityResponses_NoCapabilities(t *testing.T) {
	var buf bytes.Buffer
	chunk := []byte("hello\x1b[Aworld")
	got := filterCapabilityResponses(chunk, &buf)
	if !bytes.Equal(got, chunk) {
		t.Fatalf("got %q, want %q", got, chunk)
	}
	if buf.Len() > 0 {
		t.Fatalf("expected no capability output, got %q", buf.Bytes())
	}
}

func TestFilterCapabilityResponses_NoEscape(t *testing.T) {
	var buf bytes.Buffer
	chunk := []byte("hello world")
	got := filterCapabilityResponses(chunk, &buf)
	if !bytes.Equal(got, chunk) {
		t.Fatalf("got %q, want %q", got, chunk)
	}
}

func TestFilterCapabilityResponses_OnlyCapabilities(t *testing.T) {
	var buf bytes.Buffer
	chunk := []byte("\x1b[?2026;2$y\x1b[?2027;3$y")
	got := filterCapabilityResponses(chunk, &buf)
	if len(got) != 0 {
		t.Fatalf("expected empty remainder, got %q", got)
	}
	if !bytes.Equal(buf.Bytes(), chunk) {
		t.Fatalf("expected all bytes in capW, got %q", buf.Bytes())
	}
}

func TestFilterCapabilityResponses_Mixed(t *testing.T) {
	var buf bytes.Buffer
	// User types "ls", then DECRPM arrives, then user types enter.
	chunk := []byte("ls\x1b[?2026;2$y\r")
	got := filterCapabilityResponses(chunk, &buf)
	if !bytes.Equal(got, []byte("ls\r")) {
		t.Fatalf("remainder = %q, want %q", got, "ls\r")
	}
	if !bytes.Equal(buf.Bytes(), []byte("\x1b[?2026;2$y")) {
		t.Fatalf("capW = %q, want %q", buf.Bytes(), "\x1b[?2026;2$y")
	}
}

func TestFilterCapabilityResponses_CapBetweenCSI(t *testing.T) {
	var buf bytes.Buffer
	// Arrow up, then DECRPM, then arrow down.
	chunk := []byte("\x1b[A\x1b[?2027;3$y\x1b[B")
	got := filterCapabilityResponses(chunk, &buf)
	if !bytes.Equal(got, []byte("\x1b[A\x1b[B")) {
		t.Fatalf("remainder = %q, want %q", got, "\x1b[A\x1b[B")
	}
	if !bytes.Equal(buf.Bytes(), []byte("\x1b[?2027;3$y")) {
		t.Fatalf("capW = %q, want %q", buf.Bytes(), "\x1b[?2027;3$y")
	}
}

// newTestParser creates an InputParser with a short timeout and channels
// for feeding input and collecting output. Call close(inputCh) to stop.
func newTestParser() (inputCh chan []byte, outputCh chan []byte, parser *InputParser) {
	inputCh = make(chan []byte, 16)
	outputCh = make(chan []byte, 16)
	parser = &InputParser{
		Input:      inputCh,
		Send:       func(msg RawInputMsg) { outputCh <- []byte(msg) },
		EscTimeout: 10 * time.Millisecond,
	}
	return
}

// recvOne reads one message from outputCh with a timeout.
func recvOne(t *testing.T, outputCh <-chan []byte, timeout time.Duration) []byte {
	t.Helper()
	select {
	case msg := <-outputCh:
		return msg
	case <-time.After(timeout):
		t.Fatal("timeout waiting for output")
		return nil
	}
}

// expectNoOutput verifies nothing is emitted within the given duration.
func expectNoOutput(t *testing.T, outputCh <-chan []byte, dur time.Duration) {
	t.Helper()
	select {
	case msg := <-outputCh:
		t.Fatalf("expected no output, got %q", msg)
	case <-time.After(dur):
	}
}

// ── Complete sequences (emitted immediately) ────────────────────────────────

func TestInputParserPlainText(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte("hello")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestInputParserControlChar(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte{0x02} // ctrl+b
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte{0x02}) {
		t.Fatalf("got %q, want %q", got, []byte{0x02})
	}
}

func TestInputParserCompleteCSI(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte("\x1b[A") // arrow up
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[A")) {
		t.Fatalf("got %q, want %q", got, "\x1b[A")
	}
}

func TestInputParserCompleteSGRMouse(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte("\x1b[<0;10;5M")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[<0;10;5M")) {
		t.Fatalf("got %q, want %q", got, "\x1b[<0;10;5M")
	}
}

func TestInputParserMultipleSequences(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	// Two CSI sequences in one read — emitted together as one batch.
	inputCh <- []byte("\x1b[A\x1b[B")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[A\x1b[B")) {
		t.Fatalf("got %q, want %q", got, "\x1b[A\x1b[B")
	}
}

func TestInputParserMixedTextAndCSI(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte("hello\x1b[Aworld")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("hello\x1b[Aworld")) {
		t.Fatalf("got %q, want %q", got, "hello\x1b[Aworld")
	}
}

// ── Incomplete sequences (buffered, then completed) ─────────────────────────

func TestInputParserSplitCSI(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	// First read: ESC alone — held back (might be start of sequence).
	inputCh <- []byte{0x1b}
	expectNoOutput(t, outputCh, 5*time.Millisecond)

	// Second read: completes the CSI sequence.
	inputCh <- []byte("[A")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[A")) {
		t.Fatalf("got %q, want %q", got, "\x1b[A")
	}
}

func TestInputParserSplitSGRMouse(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte("\x1b[<0;10")
	expectNoOutput(t, outputCh, 5*time.Millisecond)

	inputCh <- []byte(";5M")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[<0;10;5M")) {
		t.Fatalf("got %q, want %q", got, "\x1b[<0;10;5M")
	}
}

func TestInputParserSplitOSC(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte("\x1b]0;tit")
	expectNoOutput(t, outputCh, 5*time.Millisecond)

	inputCh <- []byte("le\x07")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b]0;title\x07")) {
		t.Fatalf("got %q, want %q", got, "\x1b]0;title\x07")
	}
}

// ── Mixed complete + incomplete ─────────────────────────────────────────────

func TestInputParserTextThenIncompleteESC(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	// "hello" is complete, ESC is held back.
	inputCh <- []byte("hello\x1b")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("got %q, want %q", got, "hello")
	}

	// Complete the sequence.
	inputCh <- []byte("[A")
	got = recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[A")) {
		t.Fatalf("got %q, want %q", got, "\x1b[A")
	}
}

func TestInputParserCompleteCSIThenIncompleteESC(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	inputCh <- []byte("\x1b[A\x1b")
	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[A")) {
		t.Fatalf("got %q, want %q", got, "\x1b[A")
	}

	inputCh <- []byte("[B")
	got = recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[B")) {
		t.Fatalf("got %q, want %q", got, "\x1b[B")
	}
}

// ── Timeout flush (standalone ESC) ──────────────────────────────────────────

func TestInputParserTimeoutFlush(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()
	defer close(inputCh)

	// Send lone ESC — no followup data.
	inputCh <- []byte{0x1b}

	// Should flush after the timeout (10ms in test).
	got := recvOne(t, outputCh, 100*time.Millisecond)
	if !bytes.Equal(got, []byte{0x1b}) {
		t.Fatalf("got %q, want %q", got, []byte{0x1b})
	}
}

// ── Channel close flushes remaining ─────────────────────────────────────────

func TestInputParserCloseFlushes(t *testing.T) {
	inputCh, outputCh, parser := newTestParser()
	go parser.Run()

	// Send incomplete sequence then close.
	inputCh <- []byte("\x1b[")
	close(inputCh)

	got := recvOne(t, outputCh, time.Second)
	if !bytes.Equal(got, []byte("\x1b[")) {
		t.Fatalf("got %q, want %q", got, "\x1b[")
	}
}
