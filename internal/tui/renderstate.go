package tui

// RenderState carries shared state through the render pass.
// Built by Model.View() before iterating the layer stack;
// every layer receives it during View().
type RenderState struct {
	Active      bool // base layer has focus (no overlay with keyboard input)
	CommandMode bool // prefix key pressed, waiting for command
	HasOverlay  bool // an overlay with keyboard focus is present
	HasHint     bool // HintLayer is in the stack
}
