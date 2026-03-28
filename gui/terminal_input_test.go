package main

import (
	"testing"

	"fyne.io/fyne/v2"
)

func TestKeyToVTSequence(t *testing.T) {
	tests := []struct {
		key  fyne.KeyName
		want []byte
	}{
		{fyne.KeyReturn, []byte{'\r'}},
		{fyne.KeyTab, []byte{'\t'}},
		{fyne.KeyBackspace, []byte{0x7f}},
		{fyne.KeyEscape, []byte{0x1b}},
		{fyne.KeyUp, []byte{0x1b, '[', 'A'}},
		{fyne.KeyDown, []byte{0x1b, '[', 'B'}},
		{fyne.KeyRight, []byte{0x1b, '[', 'C'}},
		{fyne.KeyLeft, []byte{0x1b, '[', 'D'}},
		{fyne.KeyHome, []byte{0x1b, '[', 'H'}},
		{fyne.KeyEnd, []byte{0x1b, '[', 'F'}},
		{fyne.KeyDelete, []byte{0x1b, '[', '3', '~'}},
		{fyne.KeyPageUp, []byte{0x1b, '[', '5', '~'}},
		{fyne.KeyPageDown, []byte{0x1b, '[', '6', '~'}},
		{fyne.KeyF1, []byte{0x1b, 'O', 'P'}},
		{fyne.KeyF12, []byte{0x1b, '[', '2', '4', '~'}},
	}
	for _, tt := range tests {
		t.Run(string(tt.key), func(t *testing.T) {
			got := keyToVTSequence(tt.key)
			if len(got) != len(tt.want) {
				t.Fatalf("keyToVTSequence(%s) = %v, want %v", tt.key, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("keyToVTSequence(%s)[%d] = %02x, want %02x", tt.key, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestKeyToVTSequence_Unknown(t *testing.T) {
	got := keyToVTSequence("SomeUnknownKey")
	if got != nil {
		t.Errorf("unknown key returned %v, want nil", got)
	}
}

func TestIsControlChar(t *testing.T) {
	if !isControlChar(0x01) {
		t.Error("0x01 should be control")
	}
	if !isControlChar(0x03) {
		t.Error("0x03 (ctrl+c) should be control")
	}
	if !isControlChar(0x1a) {
		t.Error("0x1a (ctrl+z) should be control")
	}
	if isControlChar('a') {
		t.Error("'a' should not be control")
	}
	if isControlChar(0x00) {
		t.Error("0x00 should not be control (NUL)")
	}
	if isControlChar(0x1b) {
		t.Error("0x1b (ESC) is outside ctrl range")
	}
}
