package protocol

import "fmt"

// ANSI16 color names used by go-te, mapped to their palette index (0-15).
var ANSI16NameToIndex = map[string]uint8{
	"black": 0, "red": 1, "green": 2, "brown": 3,
	"blue": 4, "magenta": 5, "cyan": 6, "white": 7,
	"brightblack": 8, "brightred": 9, "brightgreen": 10, "brightbrown": 11,
	"brightblue": 12, "brightmagenta": 13, "brightcyan": 14, "brightwhite": 15,
}

// Foreground SGR codes for ANSI16 color names.
var FgSGR = map[string]string{
	"black": "30", "red": "31", "green": "32", "brown": "33",
	"blue": "34", "magenta": "35", "cyan": "36", "white": "37",
	"default": "39",
	"brightblack": "90", "brightred": "91", "brightgreen": "92", "brightbrown": "93",
	"brightblue": "94", "brightmagenta": "95", "brightcyan": "96", "brightwhite": "97",
}

// Background SGR codes for ANSI16 color names.
var BgSGR = map[string]string{
	"black": "40", "red": "41", "green": "42", "brown": "43",
	"blue": "44", "magenta": "45", "cyan": "46", "white": "47",
	"default": "49",
	"brightblack": "100", "brightred": "101", "brightgreen": "102", "brightbrown": "103",
	"brightblue": "104", "brightmagenta": "105", "brightcyan": "106", "brightwhite": "107",
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
		return "\x1b[m"
	}

	params := []string{"0"} // reset first

	if a&1 != 0 {
		params = append(params, "1") // bold
	}
	if a&2 != 0 {
		params = append(params, "3") // italic
	}
	if a&4 != 0 {
		params = append(params, "4") // underline
	}
	if a&8 != 0 {
		params = append(params, "9") // strikethrough
	}
	if a&16 != 0 {
		params = append(params, "7") // reverse
	}
	if a&32 != 0 {
		params = append(params, "5") // blink
	}
	if a&64 != 0 {
		params = append(params, "8") // conceal
	}

	if fg != "" {
		params = append(params, ColorSpecToSGR(fg, false))
	}
	if bg != "" {
		params = append(params, ColorSpecToSGR(bg, true))
	}

	return "\x1b[" + joinParams(params) + "m"
}

func joinParams(params []string) string {
	n := 0
	for _, p := range params {
		n += len(p)
	}
	n += len(params) - 1 // semicolons
	buf := make([]byte, 0, n)
	for i, p := range params {
		if i > 0 {
			buf = append(buf, ';')
		}
		buf = append(buf, p...)
	}
	return string(buf)
}
