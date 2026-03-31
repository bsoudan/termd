package te

// ColorMode identifies a color encoding mode.
type ColorMode uint8

const (
	// ColorDefault selects default terminal colors.
	ColorDefault ColorMode = iota
	// ColorANSI16 selects the 16-color palette.
	ColorANSI16
	// ColorANSI256 selects the 256-color palette.
	ColorANSI256
	// ColorTrueColor selects 24-bit true color.
	ColorTrueColor
)

// Color describes a terminal color.
type Color struct {
	Mode  ColorMode `json:"Mode,omitempty"`
	Index uint8     `json:"Index,omitempty"`
	Name  string    `json:"Name,omitempty"`
}

// Attr describes character attributes and colors.
type Attr struct {
	Fg            Color `json:"Fg,omitzero"`
	Bg            Color `json:"Bg,omitzero"`
	Bold          bool  `json:"Bold,omitempty"`
	Italics       bool  `json:"Italics,omitempty"`
	Underline     bool  `json:"Underline,omitempty"`
	Strikethrough bool  `json:"Strikethrough,omitempty"`
	Reverse       bool  `json:"Reverse,omitempty"`
	Blink         bool  `json:"Blink,omitempty"`
	Conceal       bool  `json:"Conceal,omitempty"`
	Protected     bool  `json:"Protected,omitempty"`
	ISOProtected  bool  `json:"ISOProtected,omitempty"`
}

// Cell represents a single screen cell.
type Cell struct {
	Data string `json:"Data,omitempty"`
	Attr Attr   `json:"Attr,omitzero"`
}

// Cursor tracks cursor position and attributes.
type Cursor struct {
	Row    int
	Col    int
	Attr   Attr
	Hidden bool
}
