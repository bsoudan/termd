package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	te "github.com/rcarmo/go-te/pkg/te"
	"termd/frontend/protocol"
)

var (
	overlayBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1)
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

	// Right side of tab bar: connection info, or mode indicator when active
	rightInfo := m.Endpoint
	rightBold := false
	rightRed := false
	if m.connStatus != "connected" && m.connStatus != "" {
		rightInfo = m.connStatus
	}
	if m.status != "" {
		rightInfo = m.status
	}
	if m.overlay != nil {
		rightInfo = m.overlay.Label()
		rightBold = true
	} else if m.scrollback.Active() {
		rightInfo = m.scrollback.StatusText()
		rightBold = true
	} else if m.prefixMode {
		rightInfo = "?"
		rightBold = true
	} else if m.showHint {
		rightInfo = "ctrl+b ? for help"
		rightBold = true
	} else if m.connStatus == "reconnecting" {
		secs := int(time.Until(m.retryAt).Seconds()) + 1
		rightInfo = fmt.Sprintf("reconnecting to %s in %ds...", m.Endpoint, secs)
		rightBold = true
		rightRed = true
	}
	suffix := "termd-tui"
	if m.Version != "" && (m.showHint || m.overlay != nil) {
		suffix = "termd-tui " + m.Version
	}
	sb.WriteString(renderTabBar(m.regionName, rightInfo, suffix, rightBold, rightRed, width))
	sb.WriteByte('\n')

	contentHeight := height - 1 // tab bar only
	if contentHeight < 1 {
		contentHeight = 1
	}
	showCursor := m.overlay == nil && !m.scrollback.Active()
	disconnected := m.connStatus == "reconnecting"

	if m.scrollback.Active() && m.terminal.Screen != nil {
		m.scrollback.View(&sb, m.terminal.ScreenCells(), width, contentHeight)
	} else {
		m.terminal.View(&sb, width, contentHeight, showCursor, disconnected)
	}

	base := sb.String()

	if m.overlay != nil {
		return m.overlay.View(base, width, height)
	}
	return base
}

func renderCellLine(sb *strings.Builder, row []te.Cell, width, rowIdx, cursorRow, cursorCol int, showCursor bool, disconnected bool) {
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
			if disconnected {
				// Red inverse X to show the cursor is inactive
				target = te.Attr{
					Reverse: true,
					Fg:      te.Color{Mode: te.ColorANSI16, Name: "red"},
				}
				cell.Data = "X"
			} else {
				target.Reverse = !target.Reverse
			}
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
		sb.WriteString(ansi.ResetStyle)
	}
}

// sgrTransition emits the SGR sequence to move from one attribute set to another.
func sgrTransition(from, to te.Attr) string {
	// If going back to default, just reset
	if to == (te.Attr{}) {
		return ansi.ResetStyle
	}

	var attrs []ansi.Attr

	// If any attribute was turned OFF that can't be individually disabled,
	// or if it's simpler, do a full reset first.
	needsReset := (from.Bold && !to.Bold) ||
		(from.Blink && !to.Blink) ||
		(from.Conceal && !to.Conceal)

	if needsReset {
		attrs = append(attrs, ansi.AttrReset)
		from = te.Attr{} // reset baseline
	}

	if to.Bold && !from.Bold {
		attrs = append(attrs, ansi.AttrBold)
	}
	if to.Italics && !from.Italics {
		attrs = append(attrs, ansi.AttrItalic)
	} else if !to.Italics && from.Italics {
		attrs = append(attrs, ansi.AttrNoItalic)
	}
	if to.Underline && !from.Underline {
		attrs = append(attrs, ansi.AttrUnderline)
	} else if !to.Underline && from.Underline {
		attrs = append(attrs, ansi.AttrNoUnderline)
	}
	if to.Blink && !from.Blink {
		attrs = append(attrs, ansi.AttrBlink)
	}
	if to.Reverse && !from.Reverse {
		attrs = append(attrs, ansi.AttrReverse)
	} else if !to.Reverse && from.Reverse {
		attrs = append(attrs, ansi.AttrNoReverse)
	}
	if to.Conceal && !from.Conceal {
		attrs = append(attrs, ansi.AttrConceal)
	}
	if to.Strikethrough && !from.Strikethrough {
		attrs = append(attrs, ansi.AttrStrikethrough)
	} else if !to.Strikethrough && from.Strikethrough {
		attrs = append(attrs, ansi.AttrNoStrikethrough)
	}

	if to.Fg != from.Fg {
		attrs = append(attrs, teColorAttrs(to.Fg, false)...)
	}
	if to.Bg != from.Bg {
		attrs = append(attrs, teColorAttrs(to.Bg, true)...)
	}

	if len(attrs) == 0 {
		return ""
	}

	return ansi.SGR(attrs...)
}

// teColorAttrs converts a go-te Color to ansi.Attr values for use with ansi.SGR.
func teColorAttrs(c te.Color, isBg bool) []ansi.Attr {
	switch c.Mode {
	case te.ColorDefault:
		if isBg {
			return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
		}
		return []ansi.Attr{ansi.AttrDefaultForegroundColor}
	case te.ColorANSI16:
		if isBg {
			if code, ok := protocol.BgSGRCode[c.Name]; ok {
				return []ansi.Attr{code}
			}
			return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
		}
		if code, ok := protocol.FgSGRCode[c.Name]; ok {
			return []ansi.Attr{code}
		}
		return []ansi.Attr{ansi.AttrDefaultForegroundColor}
	case te.ColorANSI256:
		if isBg {
			return []ansi.Attr{ansi.AttrExtendedBackgroundColor, 5, ansi.Attr(c.Index)}
		}
		return []ansi.Attr{ansi.AttrExtendedForegroundColor, 5, ansi.Attr(c.Index)}
	case te.ColorTrueColor:
		r, g, b := protocol.ParseHexColor(c.Name)
		if isBg {
			return []ansi.Attr{ansi.AttrExtendedBackgroundColor, 2, ansi.Attr(r), ansi.Attr(g), ansi.Attr(b)}
		}
		return []ansi.Attr{ansi.AttrExtendedForegroundColor, 2, ansi.Attr(r), ansi.Attr(g), ansi.Attr(b)}
	}
	if isBg {
		return []ansi.Attr{ansi.AttrDefaultBackgroundColor}
	}
	return []ansi.Attr{ansi.AttrDefaultForegroundColor}
}

func renderScrollableOverlay(vpContent string, hScroll int, base string, width, height int) string {
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

	contentLines := strings.Split(vpContent, "\n")
	if len(contentLines) > maxLines {
		contentLines = contentLines[:maxLines]
	}
	for i, line := range contentLines {
		runes := []rune(line)
		if hScroll > 0 && hScroll < len(runes) {
			runes = runes[hScroll:]
		} else if hScroll >= len(runes) {
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

	help := barStyle.Render("• q/esc: close • ↑↓/pgup/pgdn: scroll • ←→: pan • home: top •")
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

	baseLayer := lipgloss.NewLayer(base)
	dialogLayer := lipgloss.NewLayer(dialog).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, dialogLayer).Render()
}

func renderStatusOverlay(base string, caps StatusCaps, status *protocol.StatusResponse, width, height int) string {
	var lines []string

	lines = append(lines, "termd-tui:")
	lines = append(lines, fmt.Sprintf("  Hostname:  %s", caps.Hostname))
	lines = append(lines, fmt.Sprintf("  Version:   %s", caps.Version))
	endpointStr := caps.Endpoint
	if caps.ConnStatus == "reconnecting" {
		endpointStr += " (disconnected)"
	}
	lines = append(lines, fmt.Sprintf("  Endpoint:  %s", endpointStr))
	lines = append(lines, "")

	lines = append(lines, "terminal:")
	if term, ok := caps.TermEnv["TERM"]; ok {
		lines = append(lines, fmt.Sprintf("  TERM:      %s", term))
	}
	if ct, ok := caps.TermEnv["COLORTERM"]; ok {
		lines = append(lines, fmt.Sprintf("  COLORTERM: %s", ct))
	}
	if tp, ok := caps.TermEnv["TERM_PROGRAM"]; ok {
		lines = append(lines, fmt.Sprintf("  Program:   %s", tp))
	}
	if caps.KeyboardFlags > 0 {
		var kbCaps []string
		if caps.KeyboardFlags&1 != 0 {
			kbCaps = append(kbCaps, "disambiguate")
		}
		if caps.KeyboardFlags&2 != 0 {
			kbCaps = append(kbCaps, "event-types")
		}
		if caps.KeyboardFlags&4 != 0 {
			kbCaps = append(kbCaps, "alt-keys")
		}
		if caps.KeyboardFlags&8 != 0 {
			kbCaps = append(kbCaps, "all-as-escapes")
		}
		lines = append(lines, fmt.Sprintf("  Keyboard:  kitty (%s)", strings.Join(kbCaps, ", ")))
	} else {
		lines = append(lines, "  Keyboard:  legacy")
	}
	if caps.BgDark != nil {
		if *caps.BgDark {
			lines = append(lines, "  Background: dark")
		} else {
			lines = append(lines, "  Background: light")
		}
	}
	if caps.MouseModes != "" {
		lines = append(lines, fmt.Sprintf("  Mouse:     %s", caps.MouseModes))
	}
	lines = append(lines, "")

	lines = append(lines, "termd:")
	if status != nil {
		d := time.Duration(status.UptimeSeconds) * time.Second
		lines = append(lines, fmt.Sprintf("  Hostname:  %s", status.Hostname))
		lines = append(lines, fmt.Sprintf("  Version:   %s", status.Version))
		lines = append(lines, fmt.Sprintf("  PID:       %d", status.Pid))
		lines = append(lines, fmt.Sprintf("  Uptime:    %s", d.String()))
		lines = append(lines, fmt.Sprintf("  Listeners: %s", status.SocketPath))
		lines = append(lines, fmt.Sprintf("  Clients:   %d", status.NumClients))
		lines = append(lines, fmt.Sprintf("  Regions:   %d", status.NumRegions))
	} else {
		lines = append(lines, "  loading...")
	}

	content := strings.Join(lines, "\n")

	overlayW := 50
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := barStyle.Render("• q/esc: close •")
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

	baseLayer := lipgloss.NewLayer(base)
	dialogLayer := lipgloss.NewLayer(dialog).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, dialogLayer).Render()
}

var helpSelected = lipgloss.NewStyle().Reverse(true)

func renderHelpOverlay(base string, cursor int, items []helpItem, width, height int) string {
	var lines []string
	for i, item := range items {
		line := fmt.Sprintf("  ctrl+b %s   %s", item.key, item.label)
		if i == cursor {
			line = fmt.Sprintf("▸ ctrl+b %s   %s", item.key, item.label)
			line = helpSelected.Render(line)
		}
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n")

	overlayW := 38
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := barStyle.Render("• ↑↓/enter: select • q/esc: close •")
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

	baseLayer := lipgloss.NewLayer(base)
	dialogLayer := lipgloss.NewLayer(dialog).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, dialogLayer).Render()
}

var (
	barStyle        = lipgloss.NewStyle().Faint(true)
	barBoldStyle    = lipgloss.NewStyle().Bold(true)
	barRedBoldStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
)

// renderChromeBar renders a line like: ─ left ──── right ─ suffix ─
// left, right, and suffix are optional. suffix is rendered bold (not faint).
// The line fills to width with ─ characters.
func renderChromeBar(left, right, suffix string, rightBold, rightRed bool, width int) string {
	var sb strings.Builder
	used := 0

	// Leading: "• "
	sb.WriteString("• ")
	used += 2

	// Left content: "left •"
	if left != "" {
		sb.WriteString(left)
		sb.WriteString(" •")
		used += len([]rune(left)) + 2
	}

	// Compute space needed for right side: "• right " or trailing "•"
	rightTotal := 0
	if right != "" {
		rightTotal = len([]rune(right)) + 4 // "• right •"
	} else {
		rightTotal = 1 // trailing "•"
	}

	suffixTotal := 0
	if suffix != "" {
		suffixTotal = len([]rune(suffix)) + 3 // " suffix •"
	}

	// Fill with middle dots
	fillCount := width - used - rightTotal - suffixTotal
	if fillCount < 1 {
		fillCount = 1
	}
	for range fillCount {
		sb.WriteString("·")
	}

	// Right content
	var result string
	if right != "" && rightBold {
		// Faint everything up to here, then bold (or red+bold) "• right •"
		result = barStyle.Render(sb.String())
		style := barBoldStyle
		if rightRed {
			style = barRedBoldStyle
		}
		result += style.Render("• " + right + " •")
	} else {
		if right != "" {
			sb.WriteString("• ")
			sb.WriteString(right)
			sb.WriteString(" •")
		} else {
			sb.WriteString("•")
		}
		result = barStyle.Render(sb.String())
	}

	// Bold suffix appended outside the faint span
	if suffix != "" {
		result += barStyle.Render(" ") + barBoldStyle.Render(suffix) + barStyle.Render(" •")
	}

	return result
}

func renderTabBar(regionName, status, suffix string, rightBold, rightRed bool, width int) string {
	return renderChromeBar(regionName, status, suffix, rightBold, rightRed, width)
}

