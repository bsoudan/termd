package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	te "github.com/rcarmo/go-te/pkg/te"
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
	showCursor := !m.showLogView

	if m.localScreen != nil {
		cells := m.localScreen.LinesCells()
		for i := range contentHeight {
			var row []te.Cell
			if i < len(cells) {
				row = cells[i]
			}
			renderCellLine(&sb, row, width, i, m.cursorRow, m.cursorCol, showCursor)
			if i < contentHeight-1 {
				sb.WriteByte('\n')
			}
		}
	} else {
		for i := range contentHeight {
			line := ""
			if i < len(m.lines) {
				line = m.lines[i]
			}
			runes := []rune(line)
			if len(runes) > width {
				runes = runes[:width]
			}
			for col := range width {
				ch := ' '
				if col < len(runes) {
					ch = runes[col]
				}
				if showCursor && i == m.cursorRow && col == m.cursorCol {
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
	}

	base := sb.String()

	if m.showLogView {
		return renderLogOverlay(m, base, width, height)
	}
	return base
}

// renderCellLine writes one row of cells with ANSI color/attribute sequences.
func renderCellLine(sb *strings.Builder, row []te.Cell, width, rowIdx, cursorRow, cursorCol int, showCursor bool) {
	var cur te.Attr // tracks current SGR state (zero = default)
	for col := range width {
		var cell te.Cell
		if col < len(row) {
			cell = row[col]
		} else {
			cell.Data = " "
		}

		isCursor := showCursor && rowIdx == cursorRow && col == cursorCol

		// Determine target attributes for this cell
		target := cell.Attr
		if isCursor {
			target.Reverse = !target.Reverse
		}

		if target != cur {
			sb.WriteString(sgrTransition(cur, target))
			cur = target
		}

		ch := cell.Data
		if ch == "" || ch == "\x00" {
			ch = " "
		}
		sb.WriteString(ch)
	}

	// Reset at end of line so state doesn't leak
	if cur != (te.Attr{}) {
		sb.WriteString("\x1b[m")
	}
}

// sgrTransition emits the SGR sequence to move from one attribute set to another.
func sgrTransition(from, to te.Attr) string {
	// If going back to default, just reset
	if to == (te.Attr{}) {
		return "\x1b[m"
	}

	var params []string

	// If any attribute was turned OFF that can't be individually disabled,
	// or if it's simpler, do a full reset first.
	needsReset := (from.Bold && !to.Bold) ||
		(from.Blink && !to.Blink) ||
		(from.Conceal && !to.Conceal)

	if needsReset {
		params = append(params, "0")
		from = te.Attr{} // reset baseline
	}

	if to.Bold && !from.Bold {
		params = append(params, "1")
	}
	if to.Italics && !from.Italics {
		params = append(params, "3")
	} else if !to.Italics && from.Italics {
		params = append(params, "23")
	}
	if to.Underline && !from.Underline {
		params = append(params, "4")
	} else if !to.Underline && from.Underline {
		params = append(params, "24")
	}
	if to.Blink && !from.Blink {
		params = append(params, "5")
	}
	if to.Reverse && !from.Reverse {
		params = append(params, "7")
	} else if !to.Reverse && from.Reverse {
		params = append(params, "27")
	}
	if to.Conceal && !from.Conceal {
		params = append(params, "8")
	}
	if to.Strikethrough && !from.Strikethrough {
		params = append(params, "9")
	} else if !to.Strikethrough && from.Strikethrough {
		params = append(params, "29")
	}

	if to.Fg != from.Fg {
		params = append(params, colorSGR(to.Fg, false))
	}
	if to.Bg != from.Bg {
		params = append(params, colorSGR(to.Bg, true))
	}

	if len(params) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(params, ";") + "m"
}

// ANSI16 color name → SGR code mappings
var fgColorCode = map[string]string{
	"black": "30", "red": "31", "green": "32", "brown": "33",
	"blue": "34", "magenta": "35", "cyan": "36", "white": "37",
	"default": "39",
	"brightblack": "90", "brightred": "91", "brightgreen": "92", "brightbrown": "93",
	"brightblue": "94", "brightmagenta": "95", "brightcyan": "96", "brightwhite": "97",
}

var bgColorCode = map[string]string{
	"black": "40", "red": "41", "green": "42", "brown": "43",
	"blue": "44", "magenta": "45", "cyan": "46", "white": "47",
	"default": "49",
	"brightblack": "100", "brightred": "101", "brightgreen": "102", "brightbrown": "103",
	"brightblue": "104", "brightmagenta": "105", "brightcyan": "106", "brightwhite": "107",
}

// colorSGR returns the SGR parameter string for a go-te Color.
func colorSGR(c te.Color, isBg bool) string {
	switch c.Mode {
	case te.ColorDefault:
		if isBg {
			return "49"
		}
		return "39"
	case te.ColorANSI16:
		if isBg {
			if code, ok := bgColorCode[c.Name]; ok {
				return code
			}
			return "49"
		}
		if code, ok := fgColorCode[c.Name]; ok {
			return code
		}
		return "39"
	case te.ColorANSI256:
		if isBg {
			return fmt.Sprintf("48;5;%d", c.Index)
		}
		return fmt.Sprintf("38;5;%d", c.Index)
	case te.ColorTrueColor:
		r, g, b := parseHexColor(c.Name)
		if isBg {
			return fmt.Sprintf("48;2;%d;%d;%d", r, g, b)
		}
		return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
	}
	if isBg {
		return "49"
	}
	return "39"
}

func parseHexColor(hex string) (r, g, b uint8) {
	if len(hex) >= 6 {
		fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	}
	return
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

