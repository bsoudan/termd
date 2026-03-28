package main

import (
	"image/color"
	"testing"

	te "github.com/rcarmo/go-te/pkg/te"
)

func TestRenderScreen_BasicText(t *testing.T) {
	screen := te.NewScreen(10, 2)
	screen.Draw("Hello")

	grid := renderScreen(screen)
	if grid == nil {
		t.Fatal("renderScreen returned nil")
	}
	if len(grid) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(grid))
	}
	if len(grid[0]) != 10 {
		t.Fatalf("expected 10 cols, got %d", len(grid[0]))
	}

	// Check the text content
	want := "Hello"
	for i, ch := range want {
		if grid[0][i].Char != string(ch) {
			t.Errorf("grid[0][%d].Char = %q, want %q", i, grid[0][i].Char, string(ch))
		}
	}
	// Remaining cells should be spaces
	for i := len(want); i < 10; i++ {
		if grid[0][i].Char != " " {
			t.Errorf("grid[0][%d].Char = %q, want space", i, grid[0][i].Char)
		}
	}
}

func TestRenderScreen_Colors(t *testing.T) {
	screen := te.NewScreen(5, 1)
	screen.SelectGraphicRendition([]int{31}, false) // red foreground
	screen.Draw("R")

	grid := renderScreen(screen)
	if grid == nil {
		t.Fatal("renderScreen returned nil")
	}

	cell := grid[0][0]
	if cell.Char != "R" {
		t.Errorf("cell.Char = %q, want %q", cell.Char, "R")
	}
	wantFG := ansi16Colors["red"]
	if cell.FG != wantFG {
		t.Errorf("cell.FG = %v, want %v", cell.FG, wantFG)
	}
}

func TestRenderScreen_Bold(t *testing.T) {
	screen := te.NewScreen(5, 1)
	screen.SelectGraphicRendition([]int{1}, false) // bold
	screen.Draw("B")

	grid := renderScreen(screen)
	cell := grid[0][0]
	if !cell.Bold {
		t.Error("expected bold")
	}
}

func TestRenderScreen_Reverse(t *testing.T) {
	screen := te.NewScreen(5, 1)
	screen.SelectGraphicRendition([]int{7}, false) // reverse video
	screen.Draw("X")

	grid := renderScreen(screen)
	cell := grid[0][0]
	// Reverse should swap fg and bg
	if cell.FG != defaultBG {
		t.Errorf("reversed FG = %v, want defaultBG %v", cell.FG, defaultBG)
	}
	if cell.BG != defaultFG {
		t.Errorf("reversed BG = %v, want defaultFG %v", cell.BG, defaultFG)
	}
}

func TestRenderScreen_Nil(t *testing.T) {
	grid := renderScreen(nil)
	if grid != nil {
		t.Error("expected nil for nil screen")
	}
}

func TestTeColorToRGBA_Default(t *testing.T) {
	c := te.Color{Mode: te.ColorDefault}
	gotFG := teColorToRGBA(c, true)
	if gotFG != defaultFG {
		t.Errorf("default FG = %v, want %v", gotFG, defaultFG)
	}
	gotBG := teColorToRGBA(c, false)
	if gotBG != defaultBG {
		t.Errorf("default BG = %v, want %v", gotBG, defaultBG)
	}
}

func TestTeColorToRGBA_ANSI16(t *testing.T) {
	c := te.Color{Mode: te.ColorANSI16, Name: "green"}
	got := teColorToRGBA(c, true)
	want := color.RGBA{0x00, 0xcd, 0x00, 0xff}
	if got != want {
		t.Errorf("ANSI16 green = %v, want %v", got, want)
	}
}

func TestTeColorToRGBA_ANSI256(t *testing.T) {
	c := te.Color{Mode: te.ColorANSI256, Index: 9} // bright red
	got := teColorToRGBA(c, true)
	want := color.RGBA{0xff, 0x00, 0x00, 0xff}
	if got != want {
		t.Errorf("ANSI256 idx 9 = %v, want %v", got, want)
	}
}

func TestTeColorToRGBA_TrueColor(t *testing.T) {
	c := te.Color{Mode: te.ColorTrueColor, Name: "abcdef"}
	got := teColorToRGBA(c, true)
	want := color.RGBA{0xab, 0xcd, 0xef, 0xff}
	if got != want {
		t.Errorf("TrueColor abcdef = %v, want %v", got, want)
	}
}
