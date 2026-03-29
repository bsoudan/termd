package ui

import "charm.land/lipgloss/v2"

var (
	overlayBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1)

	helpSelected = lipgloss.NewStyle().Reverse(true)
)
