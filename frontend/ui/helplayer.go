package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HelpLayer shows available ctrl+b commands and dispatches selections.
// It has no reference to session — actions produce command messages that
// session handles via the normal layer stack.
type HelpLayer struct {
	cursor int
	items  []helpItem
}

func NewHelpLayer(items []helpItem) *HelpLayer {
	return &HelpLayer{items: items}
}

func (h *HelpLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return h.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true // absorb mouse events
	}
	return nil, nil, false
}

func (h *HelpLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "esc", "?":
		return QuitLayerMsg{}, nil, true
	case "up", "k":
		if h.cursor > 0 {
			h.cursor--
		}
		return nil, nil, true
	case "down", "j":
		if h.cursor < len(h.items)-1 {
			h.cursor++
		}
		return nil, nil, true
	case "enter":
		return QuitLayerMsg{}, h.items[h.cursor].action(), true
	default:
		for _, item := range h.items {
			if msg.String() == item.key {
				return QuitLayerMsg{}, item.action(), true
			}
		}
		return nil, nil, true
	}
}

func (h *HelpLayer) Activate() tea.Cmd { return nil }
func (h *HelpLayer) Deactivate()       {}

// View returns a positioned dialog layer for compositing.
func (h *HelpLayer) View(width, height int, active bool) []*lipgloss.Layer {
	var lines []string
	for i, item := range h.items {
		line := fmt.Sprintf("  ctrl+b %s   %s", item.key, item.label)
		if i == h.cursor {
			line = fmt.Sprintf("▸ ctrl+b %s   %s", item.key, item.label)
			line = helpSelected.Render(line)
		}
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n")

	overlayW := 38
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := statusFaint.Render("• ↑↓/enter: select • q/esc: close •")
	dialogLines := strings.Split(dialog, "\n")
	helpPad := (overlayW + 2 - lipgloss.Width(help)) / 2
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

	return []*lipgloss.Layer{lipgloss.NewLayer(dialog).X(x).Y(y).Z(1)}
}

var helpSelected = lipgloss.NewStyle().Reverse(true)

func (h *HelpLayer) Status() (string, lipgloss.Style) { return "help", lipgloss.Style{} }
