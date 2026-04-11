package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	termlog "nxtermd/internal/frontendlog"
)

// ScrollableLayer is a layer that displays scrollable content in a centered
// dialog. Used for the log viewer (with live logRing updates) and release notes.
type ScrollableLayer struct {
	label   string
	vp      viewport.Model
	hScroll int
	logRing *termlog.LogRingBuffer // non-nil for logviewer (live content)
}

func NewScrollableLayer(label, content string, gotoBottom bool, logRing *termlog.LogRingBuffer, termWidth, termHeight int) *ScrollableLayer {
	h := termHeight * 80 / 100
	if h < 5 {
		h = 5
	}
	vp := viewport.New(viewport.WithWidth(10000), viewport.WithHeight(h-3))
	vp.SetContent(content)
	if gotoBottom {
		vp.GotoBottom()
	}
	return &ScrollableLayer{label: label, vp: vp, logRing: logRing}
}

func (l *ScrollableLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return l.handleKey(msg)
	case tea.MouseMsg:
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			var cmd tea.Cmd
			l.vp, cmd = l.vp.Update(wheel)
			return nil, cmd, true
		}
		return nil, nil, true
	}
	return nil, nil, false // pass through
}

func (l *ScrollableLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "esc":
		return QuitLayerMsg{}, nil, true
	case "left":
		if l.hScroll > 0 {
			l.hScroll--
		}
		return nil, nil, true
	case "right":
		l.hScroll++
		return nil, nil, true
	case "home":
		l.hScroll = 0
		l.vp.GotoTop()
		return nil, nil, true
	default:
		var cmd tea.Cmd
		l.vp, cmd = l.vp.Update(msg)
		return nil, cmd, true
	}
}

func (l *ScrollableLayer) Activate() tea.Cmd { return nil }
func (l *ScrollableLayer) Deactivate()       {}

// View returns a positioned dialog layer for compositing.
func (l *ScrollableLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	// For logviewer, refresh content from logRing on each render.
	if l.logRing != nil {
		atBottom := l.vp.AtBottom()
		l.vp.SetContent(l.logRing.String())
		if atBottom {
			l.vp.GotoBottom()
		}
	}

	overlayW := width * 80 / 100
	overlayH := height * 80 / 100
	if overlayW < 20 {
		overlayW = 20
	}
	if overlayH < 5 {
		overlayH = 5
	}

	maxLines := overlayH - 3
	maxContentWidth := overlayW - 4

	contentLines := strings.Split(l.vp.View(), "\n")
	if len(contentLines) > maxLines {
		contentLines = contentLines[:maxLines]
	}
	for i, line := range contentLines {
		runes := []rune(line)
		if l.hScroll > 0 && l.hScroll < len(runes) {
			runes = runes[l.hScroll:]
		} else if l.hScroll >= len(runes) {
			runes = nil
		}
		if len(runes) > maxContentWidth {
			runes = runes[:maxContentWidth]
		}
		contentLines[i] = string(runes)
	}

	content := strings.Join(contentLines, "\n")

	dialog := overlayBorder.
		Width(overlayW).
		Height(maxLines).
		Render(content)

	dialogLines := strings.Split(dialog, "\n")
	maxBoxLines := maxLines + 2
	if len(dialogLines) > maxBoxLines {
		lastLine := dialogLines[len(dialogLines)-1]
		dialogLines = dialogLines[:maxBoxLines-1]
		dialogLines = append(dialogLines, lastLine)
	}

	help := overlayHint.Render("• q/esc: close • ↑↓/pgup/pgdn: scroll • ←→: pan • home: top •")
	helpPad := (overlayW + overlayBorder.GetHorizontalBorderSize() - lipgloss.Width(help)) / 2
	if helpPad < 0 {
		helpPad = 0
	}
	dialogLines = append(dialogLines, strings.Repeat(" ", helpPad)+help)
	dialog = strings.Join(dialogLines, "\n")

	dialogH := strings.Count(dialog, "\n") + 1
	x := (width - overlayW) / 2
	y := (height - dialogH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	return overlayLayers(dialog, x, y, 1)
}

func (l *ScrollableLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }
func (l *ScrollableLayer) Status(rs *RenderState) (string, lipgloss.Style) { return l.label, lipgloss.Style{} }
