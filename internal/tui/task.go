package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nxtermd/pkg/layer"
)

// TermdHandle wraps a layer.Handle with nxtermd-specific request/response
// capability. Task goroutines use this to make protocol roundtrips.
type TermdHandle struct {
	*layer.Handle[RenderState]
}

// Request sends a protocol request and blocks until the matching response
// arrives. The request is sent via Handle.Send which routes it through
// the bubbletea event loop where it is tagged with a req_id and sent
// to the server. The task goroutine stays blocked until the response
// is delivered via TaskRunner.Deliver.
func (h *TermdHandle) Request(req any) (any, error) {
	return h.Send(req)
}

// ── Overlay: a simple layer.Layer for task-driven dialogs ───────────────────

// Overlay is a simple Layer that displays a bordered dialog.
// Tasks hold a pointer and mutate fields directly between blocking calls.
type Overlay struct {
	Title      string
	Lines      []string
	Help       string
	StatusText string
}

func (o *Overlay) Activate() tea.Cmd { return nil }
func (o *Overlay) Deactivate()       {}

func (o *Overlay) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
		return nil, nil, true // absorb input
	}
	return nil, nil, false
}

func (o *Overlay) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	var lines []string
	if o.Title != "" {
		lines = append(lines, o.Title)
		lines = append(lines, "")
	}
	lines = append(lines, o.Lines...)

	content := strings.Join(lines, "\n")

	overlayW := 50
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := ""
	if o.Help != "" {
		help = overlayHint.Render("• " + o.Help + " •")
	}

	var dialogFull string
	if help != "" {
		dialogLines := strings.Split(dialog, "\n")
		helpPad := (overlayW + overlayBorder.GetHorizontalBorderSize() - lipgloss.Width(help)) / 2
		if helpPad < 0 {
			helpPad = 0
		}
		dialogLines = append(dialogLines, strings.Repeat(" ", helpPad)+help)
		dialogFull = strings.Join(dialogLines, "\n")
	} else {
		dialogFull = dialog
	}

	dialogH := strings.Count(dialogFull, "\n") + 1
	x := (width - overlayW) / 2
	y := (height - dialogH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	return overlayLayers(dialogFull, x, y, 1)
}

func (o *Overlay) WantsKeyboardInput() bool { return true }

func (o *Overlay) Status(rs *RenderState) (string, lipgloss.Style) {
	if o.StatusText != "" {
		return o.StatusText, statusBold
	}
	if o.Title != "" {
		return o.Title, statusBold
	}
	return "", lipgloss.Style{}
}

// IsKeyPress is a WaitFor filter that delivers key press events and consumes them.
func IsKeyPress(msg any) (deliver, handled bool) {
	_, ok := msg.(tea.KeyPressMsg)
	return ok, ok
}

// ShowError sets the overlay to an error state and waits for dismiss.
func ShowError(overlay *Overlay, h *layer.Handle[RenderState], errMsg string) {
	overlay.Lines = []string{"  Error: " + errMsg, "", "  Press any key to close."}
	overlay.Help = "any key: close"
	overlay.StatusText = "error: " + errMsg
	h.WaitFor(IsKeyPress)
}
