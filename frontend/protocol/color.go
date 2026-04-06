package protocol

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
)

// ANSI16 color names used by go-te, mapped to their palette index (0-15).
var ANSI16NameToIndex = map[string]uint8{
	"black": 0, "red": 1, "green": 2, "brown": 3,
	"blue": 4, "magenta": 5, "cyan": 6, "white": 7,
	"brightblack": 8, "brightred": 9, "brightgreen": 10, "brightbrown": 11,
	"brightblue": 12, "brightmagenta": 13, "brightcyan": 14, "brightwhite": 15,
}

// Foreground SGR codes for ANSI16 color names (string form, used by ColorSpecToSGR).
var FgSGR = map[string]string{
	"black": "30", "red": "31", "green": "32", "brown": "33",
	"blue": "34", "magenta": "35", "cyan": "36", "white": "37",
	"default": "39",
	"brightblack": "90", "brightred": "91", "brightgreen": "92", "brightbrown": "93",
	"brightblue": "94", "brightmagenta": "95", "brightcyan": "96", "brightwhite": "97",
}

// Background SGR codes for ANSI16 color names (string form, used by ColorSpecToSGR).
var BgSGR = map[string]string{
	"black": "40", "red": "41", "green": "42", "brown": "43",
	"blue": "44", "magenta": "45", "cyan": "46", "white": "47",
	"default": "49",
	"brightblack": "100", "brightred": "101", "brightgreen": "102", "brightbrown": "103",
	"brightblue": "104", "brightmagenta": "105", "brightcyan": "106", "brightwhite": "107",
}

// FgSGRCode maps ANSI16 color names to integer SGR codes (used by teColorAttrs).
var FgSGRCode = map[string]int{
	"black": 30, "red": 31, "green": 32, "brown": 33,
	"blue": 34, "magenta": 35, "cyan": 36, "white": 37,
	"default": 39,
	"brightblack": 90, "brightred": 91, "brightgreen": 92, "brightbrown": 93,
	"brightblue": 94, "brightmagenta": 95, "brightcyan": 96, "brightwhite": 97,
}

// BgSGRCode maps ANSI16 color names to integer SGR codes (used by teColorAttrs).
var BgSGRCode = map[string]int{
	"black": 40, "red": 41, "green": 42, "brown": 43,
	"blue": 44, "magenta": 45, "cyan": 46, "white": 47,
	"default": 49,
	"brightblack": 100, "brightred": 101, "brightgreen": 102, "brightbrown": 103,
	"brightblue": 104, "brightmagenta": 105, "brightcyan": 106, "brightwhite": 107,
}

// ColorSpecToSGR converts a color spec string to its SGR parameter string.
// Specs: "" for default, "red" for ANSI16, "5;208" for 256-color, "2;ff8700" for true color.
func ColorSpecToSGR(spec string, isBg bool) string {
	if spec == "" {
		if isBg {
			return "49"
		}
		return "39"
	}

	// ANSI16 name
	if isBg {
		if code, ok := BgSGR[spec]; ok {
			return code
		}
	} else {
		if code, ok := FgSGR[spec]; ok {
			return code
		}
	}

	// "5;N" → 256-color
	if len(spec) > 2 && spec[0] == '5' && spec[1] == ';' {
		if isBg {
			return "48;" + spec
		}
		return "38;" + spec
	}

	// "2;rrggbb" → true color
	if len(spec) > 2 && spec[0] == '2' && spec[1] == ';' {
		r, g, b := ParseHexColor(spec[2:])
		if isBg {
			return fmt.Sprintf("48;2;%d;%d;%d", r, g, b)
		}
		return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
	}

	if isBg {
		return "49"
	}
	return "39"
}

// ColorSpecToAttrs converts a color spec string to ansi.Attr values.
func ColorSpecToAttrs(spec string, isBg bool) []ansi.Attr {
	if spec == "" {
		if isBg {
			return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
		}
		return []ansi.Attr{ansi.AttrDefaultForegroundColor}
	}

	// ANSI16 name
	if isBg {
		if code, ok := BgSGRCode[spec]; ok {
			return []ansi.Attr{code}
		}
	} else {
		if code, ok := FgSGRCode[spec]; ok {
			return []ansi.Attr{code}
		}
	}

	// "5;N" → 256-color
	if len(spec) > 2 && spec[0] == '5' && spec[1] == ';' {
		var idx uint8
		fmt.Sscanf(spec[2:], "%d", &idx)
		if isBg {
			return []ansi.Attr{ansi.AttrExtendedBackgroundColor, 5, ansi.Attr(idx)}
		}
		return []ansi.Attr{ansi.AttrExtendedForegroundColor, 5, ansi.Attr(idx)}
	}

	// "2;rrggbb" → true color
	if len(spec) > 2 && spec[0] == '2' && spec[1] == ';' {
		r, g, b := ParseHexColor(spec[2:])
		if isBg {
			return []ansi.Attr{ansi.AttrExtendedBackgroundColor, 2, ansi.Attr(r), ansi.Attr(g), ansi.Attr(b)}
		}
		return []ansi.Attr{ansi.AttrExtendedForegroundColor, 2, ansi.Attr(r), ansi.Attr(g), ansi.Attr(b)}
	}

	if isBg {
		return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
	}
	return []ansi.Attr{ansi.AttrDefaultForegroundColor}
}

// ParseHexColor parses a 6-digit hex color string into RGB components.
func ParseHexColor(hex string) (r, g, b uint8) {
	if len(hex) >= 6 {
		fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	}
	return
}

// CellSGR builds a full SGR escape sequence from a ScreenCell's color specs
// and attribute bitfield. Always resets first, then sets the active attributes.
func CellSGR(fg, bg string, a uint8) string {
	if fg == "" && bg == "" && a == 0 {
		return ansi.ResetStyle
	}

	attrs := []ansi.Attr{ansi.AttrReset}

	if a&1 != 0 {
		attrs = append(attrs, ansi.AttrBold)
	}
	if a&2 != 0 {
		attrs = append(attrs, ansi.AttrItalic)
	}
	if a&4 != 0 {
		attrs = append(attrs, ansi.AttrUnderline)
	}
	if a&8 != 0 {
		attrs = append(attrs, ansi.AttrStrikethrough)
	}
	if a&16 != 0 {
		attrs = append(attrs, ansi.AttrReverse)
	}
	if a&32 != 0 {
		attrs = append(attrs, ansi.AttrBlink)
	}
	if a&64 != 0 {
		attrs = append(attrs, ansi.AttrConceal)
	}
	if a&128 != 0 {
		attrs = append(attrs, ansi.AttrFaint)
	}

	if fg != "" {
		attrs = append(attrs, ColorSpecToAttrs(fg, false)...)
	}
	if bg != "" {
		attrs = append(attrs, ColorSpecToAttrs(bg, true)...)
	}

	return ansi.SGR(attrs...)
}
