package main

import (
	"image/color"
	"testing"
)

func TestColorSpecToRGBA_ANSI16(t *testing.T) {
	tests := []struct {
		spec string
		want color.RGBA
	}{
		{"red", color.RGBA{0xcd, 0x00, 0x00, 0xff}},
		{"green", color.RGBA{0x00, 0xcd, 0x00, 0xff}},
		{"brightwhite", color.RGBA{0xff, 0xff, 0xff, 0xff}},
		{"black", color.RGBA{0x00, 0x00, 0x00, 0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got := colorSpecToRGBA(tt.spec, true)
			if got != tt.want {
				t.Errorf("colorSpecToRGBA(%q, true) = %v, want %v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestColorSpecToRGBA_ANSI256(t *testing.T) {
	tests := []struct {
		spec string
		want color.RGBA
	}{
		{"5;0", color.RGBA{0x00, 0x00, 0x00, 0xff}},   // black
		{"5;9", color.RGBA{0xff, 0x00, 0x00, 0xff}},   // bright red
		{"5;232", color.RGBA{0x08, 0x08, 0x08, 0xff}}, // grayscale dark
		{"5;255", color.RGBA{0xee, 0xee, 0xee, 0xff}}, // grayscale light
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got := colorSpecToRGBA(tt.spec, true)
			if got != tt.want {
				t.Errorf("colorSpecToRGBA(%q, true) = %v, want %v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestColorSpecToRGBA_TrueColor(t *testing.T) {
	tests := []struct {
		spec string
		want color.RGBA
	}{
		{"2;ff8700", color.RGBA{0xff, 0x87, 0x00, 0xff}},
		{"2;000000", color.RGBA{0x00, 0x00, 0x00, 0xff}},
		{"2;ffffff", color.RGBA{0xff, 0xff, 0xff, 0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got := colorSpecToRGBA(tt.spec, true)
			if got != tt.want {
				t.Errorf("colorSpecToRGBA(%q, true) = %v, want %v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestColorSpecToRGBA_Default(t *testing.T) {
	gotFG := colorSpecToRGBA("", true)
	if gotFG != defaultFG {
		t.Errorf("empty spec FG = %v, want %v", gotFG, defaultFG)
	}
	gotBG := colorSpecToRGBA("", false)
	if gotBG != defaultBG {
		t.Errorf("empty spec BG = %v, want %v", gotBG, defaultBG)
	}
}

func TestColorSpecToRGBA_Invalid(t *testing.T) {
	got := colorSpecToRGBA("notacolor", true)
	if got != defaultFG {
		t.Errorf("invalid spec = %v, want defaultFG %v", got, defaultFG)
	}
}

func TestAnsi256Palette_CubeColor(t *testing.T) {
	// Index 196 = 6x6x6 cube: r=5, g=0, b=0 → (255, 0, 0)
	// 196 - 16 = 180, 180/36 = 5, (180%36)/6 = 0, 180%6 = 0
	got := ansi256Palette[196]
	want := color.RGBA{0xff, 0x00, 0x00, 0xff}
	if got != want {
		t.Errorf("palette[196] = %v, want %v", got, want)
	}
}
