package uv

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTransformLineRewritesWideCellRows(t *testing.T) {
	var out bytes.Buffer
	r := NewTerminalRenderer(&out, []string{"TERM=xterm-256color"})

	oldscr := NewScreenBuffer(8, 1)
	oldscr.Method = ansi.GraphemeWidth
	NewStyledString("a🙂bcdef").Draw(oldscr, oldscr.Bounds())
	r.curbuf = oldscr.RenderBuffer
	r.cur = cursor{Cell: EmptyCell, Position: Pos(-1, -1)}

	newscr := NewScreenBuffer(8, 1)
	newscr.Method = ansi.GraphemeWidth
	NewStyledString("a🙂bXYef").Draw(newscr, newscr.Bounds())

	r.transformLine(newscr.RenderBuffer, 0)

	got := r.buf.String()
	if !strings.Contains(got, "a🙂bXYef") {
		t.Fatalf("transformLine output = %q, want full rewritten line", got)
	}
	if strings.Contains(got, "XYef") && !strings.Contains(got, "a🙂bXYef") {
		t.Fatalf("transformLine output = %q, rewrote only the changed suffix on a wide-cell line", got)
	}
}
