package main

import (
	"fyne.io/fyne/v2"
)

// Ensure TerminalWidget implements focusable interfaces.
var _ fyne.Focusable = (*TerminalWidget)(nil)

func (tw *TerminalWidget) FocusGained() {}
func (tw *TerminalWidget) FocusLost()   {}

func (tw *TerminalWidget) TypedRune(r rune) {
	if tw.prefixMode {
		tw.handlePrefixKey(r)
		return
	}

	// ctrl+b (0x02) enters prefix mode
	if r == 0x02 {
		tw.prefixMode = true
		return
	}

	// Other control characters are sent directly
	if isControlChar(r) {
		tw.session.sendInput([]byte{byte(r)})
		return
	}

	tw.session.sendInput([]byte(string(r)))
}

func (tw *TerminalWidget) TypedKey(ev *fyne.KeyEvent) {
	if tw.prefixMode {
		tw.prefixMode = false
		return
	}

	seq := keyToVTSequence(ev.Name)
	if seq != nil {
		tw.session.sendInput(seq)
	}
}

// handlePrefixKey processes the key after ctrl+b.
func (tw *TerminalWidget) handlePrefixKey(r rune) {
	tw.prefixMode = false
	switch r {
	case 'd':
		if tw.onDetach != nil {
			tw.onDetach()
		}
	case '?':
		// TODO: show help overlay
	case 'l':
		// TODO: show log viewer overlay
	case 's':
		// TODO: show status overlay
	case 'n':
		// TODO: show changelog overlay
	case 'b':
		// ctrl+b b sends a literal ctrl+b
		tw.session.sendInput([]byte{0x02})
	}
}

// TypedShortcut handles keyboard shortcuts including ctrl+key combinations.
func (tw *TerminalWidget) TypedShortcut(shortcut fyne.Shortcut) {
	// Fyne doesn't have a great ctrl+key story; we handle it in TypedRune
	// by checking for control characters.
}

// keyToVTSequence maps a Fyne key name to the VT escape sequence bytes.
func keyToVTSequence(key fyne.KeyName) []byte {
	switch key {
	case fyne.KeyReturn:
		return []byte{'\r'}
	case fyne.KeyTab:
		return []byte{'\t'}
	case fyne.KeyBackspace:
		return []byte{0x7f}
	case fyne.KeyEscape:
		return []byte{0x1b}
	case fyne.KeyUp:
		return []byte{0x1b, '[', 'A'}
	case fyne.KeyDown:
		return []byte{0x1b, '[', 'B'}
	case fyne.KeyRight:
		return []byte{0x1b, '[', 'C'}
	case fyne.KeyLeft:
		return []byte{0x1b, '[', 'D'}
	case fyne.KeyHome:
		return []byte{0x1b, '[', 'H'}
	case fyne.KeyEnd:
		return []byte{0x1b, '[', 'F'}
	case fyne.KeyInsert:
		return []byte{0x1b, '[', '2', '~'}
	case fyne.KeyDelete:
		return []byte{0x1b, '[', '3', '~'}
	case fyne.KeyPageUp:
		return []byte{0x1b, '[', '5', '~'}
	case fyne.KeyPageDown:
		return []byte{0x1b, '[', '6', '~'}
	case fyne.KeyF1:
		return []byte{0x1b, 'O', 'P'}
	case fyne.KeyF2:
		return []byte{0x1b, 'O', 'Q'}
	case fyne.KeyF3:
		return []byte{0x1b, 'O', 'R'}
	case fyne.KeyF4:
		return []byte{0x1b, 'O', 'S'}
	case fyne.KeyF5:
		return []byte{0x1b, '[', '1', '5', '~'}
	case fyne.KeyF6:
		return []byte{0x1b, '[', '1', '7', '~'}
	case fyne.KeyF7:
		return []byte{0x1b, '[', '1', '8', '~'}
	case fyne.KeyF8:
		return []byte{0x1b, '[', '1', '9', '~'}
	case fyne.KeyF9:
		return []byte{0x1b, '[', '2', '0', '~'}
	case fyne.KeyF10:
		return []byte{0x1b, '[', '2', '1', '~'}
	case fyne.KeyF11:
		return []byte{0x1b, '[', '2', '3', '~'}
	case fyne.KeyF12:
		return []byte{0x1b, '[', '2', '4', '~'}
	default:
		return nil
	}
}

// We need a way to intercept ctrl+key combinations. Fyne sends control
// characters as runes, so ctrl+b = 0x02, ctrl+c = 0x03, etc.
// The TypedRune handler receives these directly.

// isControlChar returns true if the rune is a control character (0x01-0x1a).
func isControlChar(r rune) bool {
	return r >= 0x01 && r <= 0x1a
}
