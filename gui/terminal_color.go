package main

import (
	"image/color"
	"strconv"
	"strings"
)

// Standard ANSI 16-color palette.
var ansi16Colors = map[string]color.RGBA{
	"black":         {0x00, 0x00, 0x00, 0xff},
	"red":           {0xcd, 0x00, 0x00, 0xff},
	"green":         {0x00, 0xcd, 0x00, 0xff},
	"yellow":        {0xcd, 0xcd, 0x00, 0xff},
	"blue":          {0x00, 0x00, 0xee, 0xff},
	"magenta":       {0xcd, 0x00, 0xcd, 0xff},
	"cyan":          {0x00, 0xcd, 0xcd, 0xff},
	"white":         {0xe5, 0xe5, 0xe5, 0xff},
	"brightblack":   {0x7f, 0x7f, 0x7f, 0xff},
	"brightred":     {0xff, 0x00, 0x00, 0xff},
	"brightgreen":   {0x00, 0xff, 0x00, 0xff},
	"brightyellow":  {0xff, 0xff, 0x00, 0xff},
	"brightblue":    {0x5c, 0x5c, 0xff, 0xff},
	"brightmagenta": {0xff, 0x00, 0xff, 0xff},
	"brightcyan":    {0x00, 0xff, 0xff, 0xff},
	"brightwhite":   {0xff, 0xff, 0xff, 0xff},
}

// ansi256Palette is the standard 256-color xterm palette, built at init.
var ansi256Palette [256]color.RGBA

func init() {
	// 0-7: standard colors
	names := []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}
	for i, name := range names {
		ansi256Palette[i] = ansi16Colors[name]
	}
	// 8-15: bright colors
	brightNames := []string{"brightblack", "brightred", "brightgreen", "brightyellow", "brightblue", "brightmagenta", "brightcyan", "brightwhite"}
	for i, name := range brightNames {
		ansi256Palette[8+i] = ansi16Colors[name]
	}
	// 16-231: 6x6x6 color cube
	for i := 0; i < 216; i++ {
		r := i / 36
		g := (i / 6) % 6
		b := i % 6
		ansi256Palette[16+i] = color.RGBA{
			R: cubeValue(r),
			G: cubeValue(g),
			B: cubeValue(b),
			A: 0xff,
		}
	}
	// 232-255: grayscale ramp
	for i := 0; i < 24; i++ {
		v := uint8(8 + i*10)
		ansi256Palette[232+i] = color.RGBA{v, v, v, 0xff}
	}
}

func cubeValue(i int) uint8 {
	if i == 0 {
		return 0
	}
	return uint8(55 + i*40)
}

var (
	defaultFG = color.RGBA{0xe5, 0xe5, 0xe5, 0xff}
	defaultBG = color.RGBA{0x1e, 0x1e, 0x1e, 0xff}
)

// colorSpecToRGBA converts a protocol color spec to an RGBA color.
// Specs: "" (default), "red" (ANSI16), "5;N" (256-color), "2;rrggbb" (true color).
func colorSpecToRGBA(spec string, isFG bool) color.RGBA {
	if spec == "" {
		if isFG {
			return defaultFG
		}
		return defaultBG
	}

	// ANSI256: "5;N"
	if len(spec) > 2 && spec[0] == '5' && spec[1] == ';' {
		idx, err := strconv.Atoi(spec[2:])
		if err == nil && idx >= 0 && idx < 256 {
			return ansi256Palette[idx]
		}
		if isFG {
			return defaultFG
		}
		return defaultBG
	}

	// TrueColor: "2;rrggbb"
	if len(spec) > 2 && spec[0] == '2' && spec[1] == ';' {
		hex := spec[2:]
		if c, ok := parseHexRGB(hex); ok {
			return c
		}
		if isFG {
			return defaultFG
		}
		return defaultBG
	}

	// ANSI16 name
	if c, ok := ansi16Colors[strings.ToLower(spec)]; ok {
		return c
	}
	if isFG {
		return defaultFG
	}
	return defaultBG
}

func parseHexRGB(hex string) (color.RGBA, bool) {
	if len(hex) != 6 {
		return color.RGBA{}, false
	}
	r, err := strconv.ParseUint(hex[0:2], 16, 8)
	if err != nil {
		return color.RGBA{}, false
	}
	g, err := strconv.ParseUint(hex[2:4], 16, 8)
	if err != nil {
		return color.RGBA{}, false
	}
	b, err := strconv.ParseUint(hex[4:6], 16, 8)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{uint8(r), uint8(g), uint8(b), 0xff}, true
}

// brighten increases RGB values by ~30% to simulate bold text.
func brighten(c color.RGBA) color.RGBA {
	bump := func(v uint8) uint8 {
		n := int(v) + 60
		if n > 255 {
			n = 255
		}
		return uint8(n)
	}
	return color.RGBA{bump(c.R), bump(c.G), bump(c.B), c.A}
}
