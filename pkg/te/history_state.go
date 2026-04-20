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
	TotalAdded  uint64      `json:"total_added,omitempty"`
	// FirstSeq is the seq of TopItems[0] (or equal to TotalAdded when
	// TopItems is empty). Marshalled explicitly so round-trips across
	// live upgrade don't derive a shifted seq space from buffer length.
	// Older snapshots that predate this field default to 0 on load; see
	// UnmarshalState's backfill.
	FirstSeq uint64 `json:"first_seq,omitempty"`
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
		TotalAdded:  h.history.TotalAdded,
		FirstSeq:    h.history.FirstSeq,
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
	h.history.TotalAdded = st.TotalAdded
	h.history.FirstSeq = st.FirstSeq
	// Backfill for snapshots that predate FirstSeq: derive from the
	// legacy implicit formula so upgrades from old binaries land in a
	// consistent state.
	if st.FirstSeq == 0 && st.TotalAdded > uint64(len(st.TopItems)) {
		h.history.FirstSeq = st.TotalAdded - uint64(len(st.TopItems))
	}
}
