package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	overlayBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("14")).
			Padding(0, 1)

	overlayHint = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

	helpSelected = lipgloss.NewStyle().Reverse(true)
)

// overlayLayers returns two lipgloss layers: a clearing rectangle of spaces
// one row/column larger than the content on each side, plus the content itself
// on top. This creates a visual margin around the overlay border.
func overlayLayers(content string, x, y, z int) []*lipgloss.Layer {
	w := lipgloss.Width(content)
	h := lipgloss.Height(content)

	clearW := w + 2
	clearH := h + 2
	var sb strings.Builder
	row := strings.Repeat(" ", clearW)
	for i := range clearH {
		sb.WriteString(row)
		if i < clearH-1 {
			sb.WriteByte('\n')
		}
	}

	clearX := max(x-1, 0)
	clearY := max(y-1, 0)

	return []*lipgloss.Layer{
		lipgloss.NewLayer(sb.String()).X(clearX).Y(clearY).Z(z),
		lipgloss.NewLayer(content).X(x).Y(y).Z(z),
	}
}
