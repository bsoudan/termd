package main

import (
	"image/color"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
	te "github.com/rcarmo/go-te/pkg/te"
)

// TerminalWidget is a custom Fyne widget that renders a monospace terminal grid.
type TerminalWidget struct {
	widget.BaseWidget

	session *session

	mu        sync.Mutex
	grid      [][]RenderedCell
	cursorRow int
	cursorCol int
	gridRows  int
	gridCols  int

	cellWidth  float32
	cellHeight float32
	fontSize   float32

	// prefixMode is true when ctrl+b has been pressed and we're waiting
	// for the next key.
	prefixMode bool

	// onDetach is called when the user presses ctrl+b d.
	onDetach func()
}

// NewTerminalWidget creates a terminal widget connected to the given session.
func NewTerminalWidget(sess *session) *TerminalWidget {
	tw := &TerminalWidget{
		session:  sess,
		gridRows: 24,
		gridCols: 80,
		fontSize: 14,
	}
	tw.ExtendBaseWidget(tw)
	tw.measureCell()
	return tw
}

func (tw *TerminalWidget) measureCell() {
	// Measure a monospace character to determine cell dimensions.
	t := canvas.NewText("M", color.White)
	t.TextSize = tw.fontSize
	t.TextStyle = fyne.TextStyle{Monospace: true}
	size := t.MinSize()
	tw.cellWidth = size.Width
	tw.cellHeight = size.Height
}

func (tw *TerminalWidget) MinSize() fyne.Size {
	return fyne.NewSize(tw.cellWidth*float32(tw.gridCols), tw.cellHeight*float32(tw.gridRows))
}

func (tw *TerminalWidget) CreateRenderer() fyne.WidgetRenderer {
	return newTerminalRenderer(tw)
}

// setScreen sets the terminal content from a go-te Screen directly.
// Used for testing without a session.
func (tw *TerminalWidget) setScreen(screen *te.Screen) {
	grid := renderScreen(screen)
	tw.mu.Lock()
	tw.grid = grid
	tw.cursorRow = screen.Cursor.Row
	tw.cursorCol = screen.Cursor.Col
	tw.mu.Unlock()
	tw.Refresh()
}

// updateFromSession reads the current screen from the session and refreshes.
func (tw *TerminalWidget) updateFromSession() {
	screen := tw.session.getScreen()
	if screen == nil {
		return
	}

	tw.session.mu.Lock()
	cursorRow := screen.Cursor.Row
	cursorCol := screen.Cursor.Col
	tw.session.mu.Unlock()

	grid := renderScreen(screen)

	tw.mu.Lock()
	tw.grid = grid
	tw.cursorRow = cursorRow
	tw.cursorCol = cursorCol
	tw.mu.Unlock()

	tw.Refresh()
}

// terminalRenderer renders the terminal grid using canvas objects.
type terminalRenderer struct {
	tw       *TerminalWidget
	rects    []*canvas.Rectangle
	texts    []*canvas.Text
	cursor   *canvas.Rectangle
	poolRows int
	poolCols int
}

func newTerminalRenderer(tw *TerminalWidget) *terminalRenderer {
	r := &terminalRenderer{tw: tw}
	r.buildPool(tw.gridRows, tw.gridCols)
	return r
}

func (r *terminalRenderer) buildPool(rows, cols int) {
	r.poolRows = rows
	r.poolCols = cols
	total := rows * cols
	r.rects = make([]*canvas.Rectangle, total)
	r.texts = make([]*canvas.Text, total)
	for i := 0; i < total; i++ {
		rect := canvas.NewRectangle(defaultBG)
		r.rects[i] = rect

		t := canvas.NewText(" ", defaultFG)
		t.TextSize = r.tw.fontSize
		t.TextStyle = fyne.TextStyle{Monospace: true}
		r.texts[i] = t
	}
	r.cursor = canvas.NewRectangle(color.RGBA{0xff, 0xff, 0xff, 0xcc})
}

func (r *terminalRenderer) Layout(size fyne.Size) {
	tw := r.tw
	// Recalculate grid dimensions from available space.
	newCols := int(size.Width / tw.cellWidth)
	newRows := int(size.Height / tw.cellHeight)
	if newCols < 1 {
		newCols = 1
	}
	if newRows < 1 {
		newRows = 1
	}

	if newCols != tw.gridCols || newRows != tw.gridRows {
		tw.gridCols = newCols
		tw.gridRows = newRows
		r.buildPool(newRows, newCols)
		if tw.session != nil {
			tw.session.resize(newCols, newRows)
		}
	}

	for row := 0; row < r.poolRows; row++ {
		for col := 0; col < r.poolCols; col++ {
			idx := row*r.poolCols + col
			x := float32(col) * tw.cellWidth
			y := float32(row) * tw.cellHeight
			r.rects[idx].Move(fyne.NewPos(x, y))
			r.rects[idx].Resize(fyne.NewSize(tw.cellWidth, tw.cellHeight))
			r.texts[idx].Move(fyne.NewPos(x, y))
			r.texts[idx].Resize(fyne.NewSize(tw.cellWidth, tw.cellHeight))
		}
	}
}

func (r *terminalRenderer) Refresh() {
	tw := r.tw
	tw.mu.Lock()
	grid := tw.grid
	cursorRow := tw.cursorRow
	cursorCol := tw.cursorCol
	tw.mu.Unlock()

	for row := 0; row < r.poolRows; row++ {
		for col := 0; col < r.poolCols; col++ {
			idx := row*r.poolCols + col
			if idx >= len(r.rects) {
				continue
			}

			var cell RenderedCell
			if grid != nil && row < len(grid) && col < len(grid[row]) {
				cell = grid[row][col]
			} else {
				cell = RenderedCell{Char: " ", FG: defaultFG, BG: defaultBG}
			}

			r.rects[idx].FillColor = cell.BG
			r.rects[idx].Refresh()

			r.texts[idx].Text = cell.Char
			fg := cell.FG
			if cell.Bold {
				// Brighten the foreground for bold (terminal convention)
				fg = brighten(fg)
			}
			r.texts[idx].Color = fg
			r.texts[idx].TextStyle = fyne.TextStyle{Monospace: true}
			r.texts[idx].Refresh()
		}
	}

	// Position cursor
	if cursorRow >= 0 && cursorRow < r.poolRows && cursorCol >= 0 && cursorCol < r.poolCols {
		r.cursor.Move(fyne.NewPos(float32(cursorCol)*tw.cellWidth, float32(cursorRow)*tw.cellHeight))
		r.cursor.Resize(fyne.NewSize(tw.cellWidth, tw.cellHeight))
		r.cursor.Show()
	} else {
		r.cursor.Hide()
	}
	r.cursor.Refresh()
}

func (r *terminalRenderer) Objects() []fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, len(r.rects)+len(r.texts)+1)
	for _, rect := range r.rects {
		objs = append(objs, rect)
	}
	for _, t := range r.texts {
		objs = append(objs, t)
	}
	objs = append(objs, r.cursor)
	return objs
}

func (r *terminalRenderer) MinSize() fyne.Size {
	return r.tw.MinSize()
}

func (r *terminalRenderer) Destroy() {}
