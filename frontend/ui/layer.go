package ui

import (
	"charm.land/lipgloss/v2"
	"nxtermd/pkg/tui"
)

// KeyboardFilter specifies what keyboard input a layer wants routed
// through bubbletea's key parser rather than forwarded raw to the server.
type KeyboardFilter struct {
	All  bool     // intercept all keyboard input
	Keys [][]byte // intercept only these specific raw byte sequences
}

// allKeysFilter is returned by layers that want all keyboard input.
var allKeysFilter = &KeyboardFilter{All: true}

// TermdLayer extends tui.Layer with nxtermd-specific capabilities.
// All nxtermd layers implement this interface.
type TermdLayer interface {
	tui.Layer

	// WantsKeyboardInput returns a filter describing which keys this
	// layer wants routed through bubbletea's key parser. Returns nil
	// if the layer doesn't need any keyboard input.
	WantsKeyboardInput() *KeyboardFilter

	// Status returns text and style for the status bar.
	Status() (text string, style lipgloss.Style)
}

// Aliases for tui types used throughout the ui package.
type QuitLayerMsg = tui.QuitLayerMsg
type PushLayerMsg = tui.PushLayerMsg

// DetachMsg is returned by session to signal the app should set Detached and quit.
type DetachMsg struct{}

// ReplyFunc is called when a server response matches a pending request.
type ReplyFunc func(payload any)

// RequestFunc sends a message to the server with a req_id and registers
// a reply handler. Used by session and overlay layers.
type RequestFunc func(msg any, reply ReplyFunc)

// requestState holds the shared req_id counter, pending reply handlers,
// and the requestFn that sends protocol messages to the server.
type requestState struct {
	nextReqID uint64
	pending   map[uint64]ReplyFunc
	requestFn RequestFunc
}

// needsFocusRouting iterates the layer stack and returns true if any
// TermdLayer wants all keyboard input routed through bubbletea.
func needsFocusRouting(stack *tui.Stack) bool {
	for _, l := range stack.Layers() {
		if tl, ok := l.(TermdLayer); ok {
			if f := tl.WantsKeyboardInput(); f != nil && f.All {
				return true
			}
		}
	}
	return false
}

// collectKeyFilters gathers specific raw byte sequences that layers
// want intercepted from raw input and delivered through bubbletea.
func collectKeyFilters(stack *tui.Stack) [][]byte {
	var keys [][]byte
	for _, l := range stack.Layers() {
		if tl, ok := l.(TermdLayer); ok {
			if f := tl.WantsKeyboardInput(); f != nil && !f.All {
				keys = append(keys, f.Keys...)
			}
		}
	}
	return keys
}

