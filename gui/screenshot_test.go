package main

import (
	"testing"

	"fyne.io/fyne/v2/test"
	te "github.com/rcarmo/go-te/pkg/te"
)

func newTestWidget(cols, rows int) *TerminalWidget {
	tw := &TerminalWidget{
		gridRows: rows,
		gridCols: cols,
		fontSize: 14,
	}
	tw.ExtendBaseWidget(tw)
	tw.measureCell()
	return tw
}

func TestScreenshot_BasicText(t *testing.T) {
	_ = test.NewApp()
	tw := newTestWidget(20, 3)

	screen := te.NewScreen(20, 3)
	screen.Draw("Hello, world!")
	tw.setScreen(screen)

	test.AssertObjectRendersToImage(t, "basic_text.png", tw)
}

func TestScreenshot_ColoredText(t *testing.T) {
	_ = test.NewApp()
	tw := newTestWidget(20, 3)

	screen := te.NewScreen(20, 3)
	screen.SelectGraphicRendition([]int{31}, false) // red
	screen.Draw("RED")
	screen.SelectGraphicRendition([]int{32}, false) // green
	screen.Draw(" GREEN")
	screen.SelectGraphicRendition([]int{34}, false) // blue
	screen.Draw(" BLUE")
	tw.setScreen(screen)

	test.AssertObjectRendersToImage(t, "colored_text.png", tw)
}

func TestScreenshot_BoldItalic(t *testing.T) {
	_ = test.NewApp()
	tw := newTestWidget(20, 3)

	screen := te.NewScreen(20, 3)
	screen.SelectGraphicRendition([]int{1}, false) // bold
	screen.Draw("BOLD")
	screen.SelectGraphicRendition([]int{0}, false) // reset
	screen.Draw(" ")
	screen.SelectGraphicRendition([]int{3}, false) // italic
	screen.Draw("ITALIC")
	tw.setScreen(screen)

	test.AssertObjectRendersToImage(t, "bold_italic.png", tw)
}

func TestScreenshot_Cursor(t *testing.T) {
	_ = test.NewApp()
	tw := newTestWidget(10, 3)

	screen := te.NewScreen(10, 3)
	screen.Draw("abc")
	tw.setScreen(screen)

	test.AssertObjectRendersToImage(t, "cursor.png", tw)
}

func TestScreenshot_ReverseVideo(t *testing.T) {
	_ = test.NewApp()
	tw := newTestWidget(20, 3)

	screen := te.NewScreen(20, 3)
	screen.Draw("normal ")
	screen.SelectGraphicRendition([]int{7}, false) // reverse
	screen.Draw("REVERSED")
	tw.setScreen(screen)

	test.AssertObjectRendersToImage(t, "reverse_video.png", tw)
}

func TestScreenshot_256Color(t *testing.T) {
	_ = test.NewApp()
	tw := newTestWidget(20, 3)

	screen := te.NewScreen(20, 3)
	screen.SelectGraphicRendition([]int{38, 5, 208}, false) // 256-color orange fg
	screen.Draw("ORANGE")
	screen.SelectGraphicRendition([]int{0}, false) // reset
	screen.SelectGraphicRendition([]int{48, 5, 21}, false) // 256-color blue bg
	screen.Draw(" BG")
	tw.setScreen(screen)

	test.AssertObjectRendersToImage(t, "256_color.png", tw)
}

func TestScreenshot_MultiLine(t *testing.T) {
	_ = test.NewApp()
	tw := newTestWidget(20, 5)

	screen := te.NewScreen(20, 5)
	screen.Draw("Line 1")
	screen.LineFeed()
	screen.CarriageReturn()
	screen.Draw("Line 2")
	screen.LineFeed()
	screen.CarriageReturn()
	screen.SelectGraphicRendition([]int{33}, false) // yellow
	screen.Draw("Line 3 yellow")
	tw.setScreen(screen)

	test.AssertObjectRendersToImage(t, "multi_line.png", tw)
}
