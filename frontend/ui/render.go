package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	tabActiveStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	statusStyle    = lipgloss.NewStyle().Italic(true).Faint(true)
	overlayBorder  = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)
	overlayHelp = lipgloss.NewStyle().
			Faint(true).
			Italic(true)
)

func renderView(m Model) string {
	if m.err != "" {
		return "error: " + m.err + "\n"
	}

	width := m.termWidth
	if width <= 0 {
		width = 80
	}
	height := m.termHeight
	if height <= 0 {
		height = 24
	}

	var sb strings.Builder

	status := m.status
	if m.prefixMode && !m.showLogView {
		status = "ctrl+b ..."
	}
	sb.WriteString(renderTabBar(m.regionName, status, width))
	sb.WriteByte('\n')

	contentHeight := height - 1
	for i := 0; i < contentHeight; i++ {
		line := ""
		if i < len(m.lines) {
			line = m.lines[i]
		}
		runes := []rune(line)
		if len(runes) > width {
			runes = runes[:width]
		}
		for col := 0; col < width; col++ {
			ch := ' '
			if col < len(runes) {
				ch = runes[col]
			}
			if i == m.cursorRow && col == m.cursorCol && !m.showLogView {
				sb.WriteString("\x1b[7m")
				sb.WriteRune(ch)
				sb.WriteString("\x1b[27m")
			} else {
				sb.WriteRune(ch)
			}
		}
		if i < contentHeight-1 {
			sb.WriteByte('\n')
		}
	}

	base := sb.String()

	if m.showLogView {
		return renderLogOverlay(m, base, width, height)
	}
	return base
}

func renderLogOverlay(m Model, base string, width, height int) string {
	overlayW := width * 80 / 100
	overlayH := height * 80 / 100
	if overlayW < 20 {
		overlayW = 20
	}
	if overlayH < 5 {
		overlayH = 5
	}

	content := m.logViewport.View()

	// overlayH = border(2) + log content + help(1)
	maxLogLines := overlayH - 3
	maxContentWidth := overlayW - 4

	contentLines := strings.Split(content, "\n")
	if len(contentLines) > maxLogLines {
		contentLines = contentLines[:maxLogLines]
	}
	for i, line := range contentLines {
		runes := []rune(line)
		if m.logHScroll > 0 && m.logHScroll < len(runes) {
			runes = runes[m.logHScroll:]
		} else if m.logHScroll >= len(runes) {
			runes = nil
		}
		if len(runes) > maxContentWidth {
			runes = runes[:maxContentWidth]
		}
		contentLines[i] = string(runes)
	}

	content = strings.Join(contentLines, "\n")

	dialog := overlayBorder.
		Width(overlayW).
		Height(maxLogLines).
		Render(content)

	// lipgloss Height doesn't truncate — hard-clamp and re-add bottom border
	dialogLines := strings.Split(dialog, "\n")
	maxBoxLines := maxLogLines + 2 // content + border top/bottom
	if len(dialogLines) > maxBoxLines {
		lastLine := dialogLines[len(dialogLines)-1]
		dialogLines = dialogLines[:maxBoxLines-1]
		dialogLines = append(dialogLines, lastLine)
	}

	// Add help text below the border
	help := overlayHelp.Render(" q/esc: close  ↑↓/pgup/pgdn: scroll  ←→: pan  home: top ")
	helpPad := (overlayW - lipgloss.Width(help)) / 2
	if helpPad < 0 {
		helpPad = 0
	}
	dialogLines = append(dialogLines, strings.Repeat(" ", helpPad)+help)
	dialog = strings.Join(dialogLines, "\n")

	x := (width - overlayW) / 2
	y := (height - overlayH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	baseLayer := lipgloss.NewLayer(base)
	dialogLayer := lipgloss.NewLayer(dialog).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, dialogLayer).Render()
}

func renderTabBar(regionName, status string, width int) string {
	tab := ""
	if regionName != "" {
		tab = tabActiveStyle.Render(" " + regionName + " ")
	}

	if len(status) > 20 {
		status = status[:20]
	}
	styledStatus := ""
	if status != "" {
		styledStatus = statusStyle.Render(status)
	}

	tabWidth := lipgloss.Width(tab)
	statusWidth := lipgloss.Width(styledStatus)
	fill := width - tabWidth - statusWidth
	if fill < 0 {
		fill = 0
	}

	return tab + strings.Repeat(" ", fill) + styledStatus
}

