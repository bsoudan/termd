package te

// HistoryState is a fully serializable snapshot of a HistoryScreen's state,
// including the embedded Screen and scrollback buffers.
type HistoryState struct {
	Screen      ScreenState `json:"screen"`
	TopItems    [][]Cell    `json:"top_items,omitempty"`
	BottomItems [][]Cell    `json:"bottom_items,omitempty"`
	Ratio       float64     `json:"ratio"`
	Size        int         `json:"size"`
	Position    int         `json:"position"`
}

// MarshalState exports the complete history screen state.
func (h *HistoryScreen) MarshalState() *HistoryState {
	return &HistoryState{
		Screen:      *h.Screen.MarshalState(),
		TopItems:    trimBuffer(h.history.Top.items),
		BottomItems: trimBuffer(h.history.Bottom.items),
		Ratio:       h.history.Ratio,
		Size:        h.history.Size,
		Position:    h.history.Position,
	}
}

// UnmarshalState restores history screen state from a serializable struct.
// After calling this, the caller must re-set WriteProcessInput and
// create a new Stream.
func (h *HistoryScreen) UnmarshalState(st *HistoryState) {
	if h.Screen == nil {
		h.Screen = &Screen{}
	}
	h.Screen.UnmarshalState(&st.Screen)

	h.history.Top.items = deepCopyBuffer(st.TopItems)
	h.history.Top.max = st.Size
	h.history.Bottom.items = deepCopyBuffer(st.BottomItems)
	h.history.Bottom.max = st.Size
	h.history.Ratio = st.Ratio
	h.history.Size = st.Size
	h.history.Position = st.Position
}
