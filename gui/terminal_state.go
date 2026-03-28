package main

import (
	"image/color"

	te "github.com/rcarmo/go-te/pkg/te"
)

// RenderedCell is the pure-data representation of a single terminal cell,
// ready for rendering. Produced from a go-te Screen without any Fyne
// dependency so it can be tested independently.
type RenderedCell struct {
	Char          string
	FG            color.RGBA
	BG            color.RGBA
	Bold          bool
	Italic        bool
	Underline     bool
	Strikethrough bool
}

// renderScreen converts a go-te Screen into a grid of RenderedCells.
// Returns nil if screen is nil.
func renderScreen(screen *te.Screen) [][]RenderedCell {
	if screen == nil {
		return nil
	}
	rows := len(screen.Buffer)
	if rows == 0 {
		return nil
	}
	cols := len(screen.Buffer[0])

	grid := make([][]RenderedCell, rows)
	for r := 0; r < rows; r++ {
		grid[r] = make([]RenderedCell, cols)
		for c := 0; c < cols; c++ {
			cell := screen.Buffer[r][c]
			ch := cell.Data
			if ch == "" || ch == "\x00" {
				ch = " "
			}

			fg := teColorToRGBA(cell.Attr.Fg, true)
			bg := teColorToRGBA(cell.Attr.Bg, false)

			if cell.Attr.Reverse {
				fg, bg = bg, fg
			}
			if cell.Attr.Conceal {
				fg = bg
			}

			grid[r][c] = RenderedCell{
				Char:          ch,
				FG:            fg,
				BG:            bg,
				Bold:          cell.Attr.Bold,
				Italic:        cell.Attr.Italics,
				Underline:     cell.Attr.Underline,
				Strikethrough: cell.Attr.Strikethrough,
			}
		}
	}
	return grid
}

// teColorToRGBA converts a go-te Color to an RGBA value.
func teColorToRGBA(c te.Color, isFG bool) color.RGBA {
	switch c.Mode {
	case te.ColorDefault:
		if isFG {
			return defaultFG
		}
		return defaultBG
	case te.ColorANSI16:
		if rgba, ok := ansi16Colors[c.Name]; ok {
			return rgba
		}
	case te.ColorANSI256:
		if int(c.Index) < 256 {
			return ansi256Palette[c.Index]
		}
	case te.ColorTrueColor:
		if rgba, ok := parseHexRGB(c.Name); ok {
			return rgba
		}
	}
	if isFG {
		return defaultFG
	}
	return defaultBG
}
