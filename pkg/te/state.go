package te

// ScreenState is a fully serializable snapshot of a Screen's state.
// All fields are exported and use JSON-friendly types (map[int]bool
// instead of map[int]struct{}).
type ScreenState struct {
	Columns int      `json:"columns"`
	Lines   int      `json:"lines"`
	Buffer  [][]Cell `json:"buffer"`

	AltBuffer      [][]Cell `json:"alt_buffer,omitempty"`
	AltActive      bool     `json:"alt_active,omitempty"`
	AltSavepoints  []Savepoint `json:"alt_savepoints,omitempty"`
	AltLineWrapped map[int]bool `json:"alt_line_wrapped,omitempty"`
	AltWrapNext    bool     `json:"alt_wrap_next,omitempty"`
	AltSavedCursor *Cursor  `json:"alt_saved_cursor,omitempty"`

	Cursor     Cursor       `json:"cursor"`
	Mode       map[int]bool `json:"mode,omitempty"`
	TabStops   map[int]bool `json:"tab_stops,omitempty"`
	SavedModes map[int]bool `json:"saved_modes,omitempty"`
	Margins    *Margins     `json:"margins,omitempty"`

	LeftMargin          int  `json:"left_margin,omitempty"`
	RightMargin         int  `json:"right_margin,omitempty"`
	CursorStyle         int  `json:"cursor_style,omitempty"`
	CharProtectionMode  int  `json:"char_protection_mode,omitempty"`
	StatusDisplay       int  `json:"status_display,omitempty"`
	ConformanceLevel    int  `json:"conformance_level,omitempty"`
	ConformanceExplicit bool `json:"conformance_explicit,omitempty"`
	IndexedColors       int  `json:"indexed_colors,omitempty"`

	Charset int    `json:"charset,omitempty"`
	G0      []rune `json:"g0,omitempty"`
	G1      []rune `json:"g1,omitempty"`

	Savepoints   []Savepoint `json:"savepoints,omitempty"`
	SavedColumns *int        `json:"saved_columns,omitempty"`

	LineWrapped map[int]bool `json:"line_wrapped,omitempty"`
	WrapNext    bool         `json:"wrap_next,omitempty"`
	LastDrawn   string       `json:"last_drawn,omitempty"`

	Title          string   `json:"title,omitempty"`
	IconName       string   `json:"icon_name,omitempty"`
	TitleStack     []string `json:"title_stack,omitempty"`
	IconStack      []string `json:"icon_stack,omitempty"`
	TitleHexInput  bool     `json:"title_hex_input,omitempty"`
	TitleHexOutput bool     `json:"title_hex_output,omitempty"`

	ColorPalette  map[int]string    `json:"color_palette,omitempty"`
	DynamicColors map[int]string    `json:"dynamic_colors,omitempty"`
	SpecialColors map[int]string    `json:"special_colors,omitempty"`
	SelectionData map[string]string `json:"selection_data,omitempty"`

	WindowPosX        int  `json:"window_pos_x,omitempty"`
	WindowPosY        int  `json:"window_pos_y,omitempty"`
	WindowPixelWidth  int  `json:"window_pixel_width,omitempty"`
	WindowPixelHeight int  `json:"window_pixel_height,omitempty"`
	ScreenPixelWidth  int  `json:"screen_pixel_width,omitempty"`
	ScreenPixelHeight int  `json:"screen_pixel_height,omitempty"`
	CharPixelWidth    int  `json:"char_pixel_width,omitempty"`
	CharPixelHeight   int  `json:"char_pixel_height,omitempty"`
	WindowIconified   bool `json:"window_iconified,omitempty"`
}

// MarshalState exports the complete screen state to a serializable struct.
// WriteProcessInput (a function pointer) is not included and must be
// re-set by the caller after UnmarshalState.
func (s *Screen) MarshalState() *ScreenState {
	return &ScreenState{
		Columns: s.Columns,
		Lines:   s.Lines,
		Buffer:  deepCopyBuffer(s.Buffer),

		AltBuffer:      deepCopyBuffer(s.altBuffer),
		AltActive:      s.altActive,
		AltSavepoints:  copySavepoints(s.altSavepoints),
		AltLineWrapped: copyBoolMap(s.altLineWrapped),
		AltWrapNext:    s.altWrapNext,
		AltSavedCursor: copyCursorPtr(s.altSavedCursor),

		Cursor:     s.Cursor,
		Mode:       setToBoolMap(s.Mode),
		TabStops:   setToBoolMap(s.TabStops),
		SavedModes: copyBoolMap(s.savedModes),
		Margins:    copyMarginsPtr(s.Margins),

		LeftMargin:          s.leftMargin,
		RightMargin:         s.rightMargin,
		CursorStyle:         s.cursorStyle,
		CharProtectionMode:  s.charProtectionMode,
		StatusDisplay:       s.statusDisplay,
		ConformanceLevel:    s.conformanceLevel,
		ConformanceExplicit: s.conformanceExplicit,
		IndexedColors:       s.indexedColors,

		Charset: s.Charset,
		G0:      copyRunes(s.G0),
		G1:      copyRunes(s.G1),

		Savepoints:   copySavepoints(s.Savepoints),
		SavedColumns: copyIntPtr(s.SavedColumns),

		LineWrapped: copyBoolMap(s.lineWrapped),
		WrapNext:    s.wrapNext,
		LastDrawn:   s.lastDrawn,

		Title:          s.Title,
		IconName:       s.IconName,
		TitleStack:     copyStrings(s.titleStack),
		IconStack:      copyStrings(s.iconStack),
		TitleHexInput:  s.titleHexInput,
		TitleHexOutput: s.titleHexOutput,

		ColorPalette:  copyStringMap(s.colorPalette),
		DynamicColors: copyStringMap(s.dynamicColors),
		SpecialColors: copyStringMap(s.specialColors),
		SelectionData: copyStringStringMap(s.selectionData),

		WindowPosX:        s.windowPosX,
		WindowPosY:        s.windowPosY,
		WindowPixelWidth:  s.windowPixelWidth,
		WindowPixelHeight: s.windowPixelHeight,
		ScreenPixelWidth:  s.screenPixelWidth,
		ScreenPixelHeight: s.screenPixelHeight,
		CharPixelWidth:    s.charPixelWidth,
		CharPixelHeight:   s.charPixelHeight,
		WindowIconified:   s.windowIconified,
	}
}

// UnmarshalState restores screen state from a serializable struct.
// After calling this, the caller must re-set WriteProcessInput and
// create a new Stream if needed. All rows are marked dirty.
func (s *Screen) UnmarshalState(st *ScreenState) {
	s.Columns = st.Columns
	s.Lines = st.Lines
	s.Buffer = deepCopyBuffer(st.Buffer)

	s.altBuffer = deepCopyBuffer(st.AltBuffer)
	s.altActive = st.AltActive
	s.altSavepoints = copySavepoints(st.AltSavepoints)
	s.altLineWrapped = copyBoolMap(st.AltLineWrapped)
	s.altWrapNext = st.AltWrapNext
	s.altSavedCursor = copyCursorPtr(st.AltSavedCursor)

	s.Cursor = st.Cursor
	s.Mode = boolToSetMap(st.Mode)
	s.TabStops = boolToSetMap(st.TabStops)
	s.savedModes = copyBoolMap(st.SavedModes)
	s.Margins = copyMarginsPtr(st.Margins)

	s.leftMargin = st.LeftMargin
	s.rightMargin = st.RightMargin
	s.cursorStyle = st.CursorStyle
	s.charProtectionMode = st.CharProtectionMode
	s.statusDisplay = st.StatusDisplay
	s.conformanceLevel = st.ConformanceLevel
	s.conformanceExplicit = st.ConformanceExplicit
	s.indexedColors = st.IndexedColors

	s.Charset = st.Charset
	s.G0 = copyRunes(st.G0)
	s.G1 = copyRunes(st.G1)

	s.Savepoints = copySavepoints(st.Savepoints)
	s.SavedColumns = copyIntPtr(st.SavedColumns)

	s.lineWrapped = copyBoolMap(st.LineWrapped)
	s.wrapNext = st.WrapNext
	s.lastDrawn = st.LastDrawn

	s.Title = st.Title
	s.IconName = st.IconName
	s.titleStack = copyStrings(st.TitleStack)
	s.iconStack = copyStrings(st.IconStack)
	s.titleHexInput = st.TitleHexInput
	s.titleHexOutput = st.TitleHexOutput

	s.colorPalette = copyStringMap(st.ColorPalette)
	s.dynamicColors = copyStringMap(st.DynamicColors)
	s.specialColors = copyStringMap(st.SpecialColors)
	s.selectionData = copyStringStringMap(st.SelectionData)

	s.windowPosX = st.WindowPosX
	s.windowPosY = st.WindowPosY
	s.windowPixelWidth = st.WindowPixelWidth
	s.windowPixelHeight = st.WindowPixelHeight
	s.screenPixelWidth = st.ScreenPixelWidth
	s.screenPixelHeight = st.ScreenPixelHeight
	s.charPixelWidth = st.CharPixelWidth
	s.charPixelHeight = st.CharPixelHeight
	s.windowIconified = st.WindowIconified

	// Mark all rows dirty so the next render is a full repaint.
	s.Dirty = make(map[int]struct{}, s.Lines)
	for i := range s.Lines {
		s.Dirty[i] = struct{}{}
	}
}

// --- copy helpers ---

func deepCopyBuffer(buf [][]Cell) [][]Cell {
	if buf == nil {
		return nil
	}
	out := make([][]Cell, len(buf))
	for i, row := range buf {
		out[i] = append([]Cell(nil), row...)
	}
	return out
}

func setToBoolMap(m map[int]struct{}) map[int]bool {
	if m == nil {
		return nil
	}
	out := make(map[int]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func boolToSetMap(m map[int]bool) map[int]struct{} {
	if m == nil {
		return nil
	}
	out := make(map[int]struct{}, len(m))
	for k, v := range m {
		if v {
			out[k] = struct{}{}
		}
	}
	return out
}

func copyBoolMap(m map[int]bool) map[int]bool {
	if m == nil {
		return nil
	}
	out := make(map[int]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyStringMap(m map[int]string) map[int]string {
	if m == nil {
		return nil
	}
	out := make(map[int]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyStringStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyRunes(r []rune) []rune {
	if r == nil {
		return nil
	}
	return append([]rune(nil), r...)
}

func copyStrings(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s...)
}

func copySavepoints(sp []Savepoint) []Savepoint {
	if sp == nil {
		return nil
	}
	out := make([]Savepoint, len(sp))
	for i, s := range sp {
		out[i] = s
		out[i].G0 = copyRunes(s.G0)
		out[i].G1 = copyRunes(s.G1)
		out[i].SavedCol = copyIntPtr(s.SavedCol)
	}
	return out
}

func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func copyCursorPtr(c *Cursor) *Cursor {
	if c == nil {
		return nil
	}
	v := *c
	return &v
}

func copyMarginsPtr(m *Margins) *Margins {
	if m == nil {
		return nil
	}
	v := *m
	return &v
}
