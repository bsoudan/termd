package ui

import tea "charm.land/bubbletea/v2"

// Layer is the interface for all layers in the UI stack.
// Layers are pointers with mutable state - Update mutates in place.
// The bool return signals whether the message was handled (stop propagating)
// or should continue to the next layer.
type Layer interface {
	Update(tea.Msg) (response tea.Msg, cmd tea.Cmd, handled bool)
	View(width, height int) string
	Status() (text string, bold bool, red bool)
}

// OverlayViewer is implemented by layers that render a dialog composited
// over the base view (logviewer, status, help, release notes).
type OverlayViewer interface {
	ViewOverlay(base string, width, height int) string
}

// QuitLayerMsg is returned by a layer's Update to request removal from the stack.
type QuitLayerMsg struct{}

// PushLayerMsg is sent as a tea.Msg to push a new layer onto the stack.
type PushLayerMsg struct{ Layer Layer }

// DetachMsg is returned by session to signal the app should set Detached and quit.
type DetachMsg struct{}

// ReplyFunc is called when a server response matches a pending request.
type ReplyFunc func(payload any)

// RequestFunc sends a message to the server with a req_id and registers
// a reply handler. Used by session and overlay layers.
type RequestFunc func(msg any, reply ReplyFunc)

// requestState holds the shared req_id counter and pending reply handlers.
type requestState struct {
	nextReqID uint64
	pending   map[uint64]ReplyFunc
}
