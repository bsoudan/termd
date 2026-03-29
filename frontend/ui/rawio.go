package ui

import (
	"bytes"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// RawInputMsg carries raw bytes from the terminal input goroutine.
// InputParser guarantees that each RawInputMsg contains only complete
// ANSI escape sequences — no sequence is ever split across messages.
// This allows downstream consumers to safely identify control characters
// (like ctrl+b) and escape sequences (like SGR mouse events) without
// worrying about partial sequences spanning message boundaries.
type RawInputMsg []byte

// splitComplete finds the boundary between complete and incomplete ANSI
// sequences in buf. It returns the number of leading bytes that form
// complete sequences. Any bytes after that are a partial escape sequence
// that should be buffered until more data arrives.
//
// For example, given "hello\x1b" (text + lone ESC), it returns 5 — the
// "hello" is complete, but the ESC might be the start of an escape
// sequence and should be held for the next read.
//
// Uses ansi.DecodeSequence to tokenize the byte stream. Each call to
// DecodeSequence consumes one complete token (printable character,
// control character, or escape sequence) and returns a parser state.
// NormalState means the token was complete; any other state means we're
// mid-sequence.
func splitComplete(buf []byte) int {
	var state byte // ansi.NormalState = 0
	lastComplete := 0
	pos := 0
	for pos < len(buf) {
		_, _, n, s := ansi.DecodeSequence(buf[pos:], state, nil)
		if n == 0 {
			break // no progress — avoid infinite loop on malformed input
		}
		pos += n
		state = s
		if state == ansi.NormalState {
			lastComplete = pos
		}
	}
	return lastComplete
}

// InputParser reads raw byte chunks from an input channel, buffers them,
// and emits complete chunks via a send function. It guarantees that no
// ANSI escape sequence is split across emissions.
//
// When the buffer ends with a partial escape sequence (e.g., a lone ESC
// that might be the start of CSI, OSC, etc.), it waits briefly for more
// data. If nothing arrives within EscTimeout, it flushes the partial
// data as-is — this handles the case where the user pressed the Escape
// key rather than starting an escape sequence.
type InputParser struct {
	Input      <-chan []byte
	Send       func(RawInputMsg)
	EscTimeout time.Duration
}

// Run processes input until the input channel is closed.
func (p *InputParser) Run() {
	var pending []byte
	timeout := p.EscTimeout
	if timeout == 0 {
		timeout = 50 * time.Millisecond
	}

	for {
		var data []byte
		var ok bool

		if len(pending) > 0 {
			// Have buffered bytes (incomplete sequence) — wait briefly
			// for more data to complete the sequence before flushing.
			select {
			case data, ok = <-p.Input:
				if !ok {
					if len(pending) > 0 {
						p.Send(RawInputMsg(pending))
					}
					return
				}
			case <-time.After(timeout):
				// No more data arrived — flush as-is. This handles
				// standalone ESC key presses that aren't followed by
				// an escape sequence.
				p.Send(RawInputMsg(pending))
				pending = nil
				continue
			}
		} else {
			data, ok = <-p.Input
			if !ok {
				return
			}
		}

		pending = append(pending, data...)

		completeLen := splitComplete(pending)
		if completeLen > 0 {
			msg := make([]byte, completeLen)
			copy(msg, pending[:completeLen])
			p.Send(RawInputMsg(msg))

			// Retain incomplete tail for next iteration.
			remaining := pending[completeLen:]
			pending = make([]byte, len(remaining))
			copy(pending, remaining)
		}
	}
}

// InputLoop reads raw bytes from stdin and sends complete chunks to
// bubbletea. It spawns a goroutine for blocking reads, then runs
// InputParser to handle buffering and sequence boundary detection.
//
// During startup bubbletea negotiates terminal capabilities via
// query/response sequences. Since bubbletea reads from a pipe (not
// stdin directly), we forward stdin data through pipeW until Init
// completes so bubbletea can process those responses. After that we
// switch to sending RawInputMsg for normal operation.
func InputLoop(stdin *os.File, p *tea.Program, pipeW io.Writer, ready <-chan struct{}) {
	ch := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				ch <- data
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()

	// Forward all input to bubbletea's pipe until init completes.
	for {
		select {
		case <-ready:
			goto rawMode
		case data, ok := <-ch:
			if !ok {
				return
			}
			pipeW.Write(data)
		}
	}

rawMode:
	parser := &InputParser{
		Input:      ch,
		Send:       func(msg RawInputMsg) { p.Send(msg) },
		EscTimeout: 50 * time.Millisecond,
	}
	parser.Run()
}

const prefixKey = 0x02 // ctrl+b

// sgrMouseCSIPrefix identifies SGR mouse sequences (ESC [ <).
var sgrMouseCSIPrefix = []byte{0x1b, '[', '<'}

// Alt key sequences for tab navigation.
var (
	altPrevTab = []byte{0x1b, ','} // Alt+, = previous tab
	altNextTab = []byte{0x1b, '.'} // Alt+. = next tab
)

// handleRawInput processes raw bytes in normal mode (no focus layer active,
// ctrl+b already handled by Model). It uses DecodeSequence to iterate
// complete tokens, routing SGR mouse sequences based on whether the child
// terminal wants mouse input and forwarding everything else to the server.
//
// InputParser guarantees that all sequences in chunk are complete, so
// we can safely identify mouse sequences by prefix without worrying
// about partial sequences.
func (s *SessionLayer) handleRawInput(chunk []byte) (tea.Msg, tea.Cmd) {
	// Fast path: no special sequences in the chunk at all.
	if !bytes.Contains(chunk, sgrMouseCSIPrefix) &&
		!bytes.Contains(chunk, altPrevTab) &&
		!bytes.Contains(chunk, altNextTab) {
		s.sendRawToServer(chunk)
		return nil, nil
	}

	// Iterate complete tokens, separating special sequences from regular input.
	var mice []tea.MouseMsg
	var rest []byte
	pos := 0
	for pos < len(chunk) {
		_, _, n, _ := ansi.DecodeSequence(chunk[pos:], ansi.NormalState, nil)
		if n == 0 {
			break
		}
		seq := chunk[pos : pos+n]
		switch {
		case bytes.HasPrefix(seq, sgrMouseCSIPrefix):
			if msg := parseSGRMouse(seq); msg != nil {
				mice = append(mice, msg)
			}
		case bytes.Equal(seq, altPrevTab):
			if len(rest) > 0 {
				s.sendRawToServer(rest)
				rest = nil
			}
			s.prevTab()
		case bytes.Equal(seq, altNextTab):
			if len(rest) > 0 {
				s.sendRawToServer(rest)
				rest = nil
			}
			s.nextTab()
		default:
			rest = append(rest, seq...)
		}
		pos += n
	}

	if len(rest) > 0 {
		s.sendRawToServer(rest)
	}
	var cmds []tea.Cmd
	t := s.activeTerm()
	for _, mouse := range mice {
		if t != nil && t.ChildWantsMouse() {
			t.ForwardMouse(mouse)
		} else {
			cmd := s.handleMouse(mouse)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return nil, tea.Batch(cmds...)
}
