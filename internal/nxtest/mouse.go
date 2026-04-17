package nxtest

import "fmt"

// MouseButton is the SGR mouse button code used in test input.
type MouseButton int

const (
	MouseLeft   MouseButton = 0
	MouseMiddle MouseButton = 1
	MouseRight  MouseButton = 2
)

const (
	mouseDragBit       = 32
	mouseWheelUpCode   = 64
	mouseWheelDownCode = 65
)

func sgrMouse(code int, col, row int, release bool) []byte {
	kind := 'M'
	if release {
		kind = 'm'
	}
	return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", code, col, row, kind)
}

// MousePress sends an SGR mouse press at the given 1-based outer-terminal
// coordinates.
func (t *T) MousePress(button MouseButton, col, row int) *WriteHandle {
	t.Helper()
	return t.Write(sgrMouse(int(button), col, row, false))
}

// MouseRelease sends an SGR mouse release at the given 1-based coordinates.
func (t *T) MouseRelease(button MouseButton, col, row int) *WriteHandle {
	t.Helper()
	return t.Write(sgrMouse(int(button), col, row, true))
}

// MouseDrag sends an SGR drag/motion event with the given button held.
func (t *T) MouseDrag(button MouseButton, col, row int) *WriteHandle {
	t.Helper()
	return t.Write(sgrMouse(mouseDragBit|int(button), col, row, false))
}

// MouseWheelUp sends an SGR wheel-up event.
func (t *T) MouseWheelUp(col, row int) *WriteHandle {
	t.Helper()
	return t.Write(sgrMouse(mouseWheelUpCode, col, row, false))
}

// MouseWheelDown sends an SGR wheel-down event.
func (t *T) MouseWheelDown(col, row int) *WriteHandle {
	t.Helper()
	return t.Write(sgrMouse(mouseWheelDownCode, col, row, false))
}
