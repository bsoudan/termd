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

func renderView(s *SessionLayer, layerStatus string, layerStatusBold, layerStatusRed, hideCursor bool) string {
	if s.err != "" {
		return "error: " + s.err + "\n"
	}

	width := s.termWidth
	if width <= 0 {
		width = 80
	}
	height := s.termHeight
	if height <= 0 {
		height = 24
	}

	var sb strings.Builder

	// Right side of tab bar: status from layer stack or session state.
	rightInfo := s.endpoint
	rightBold := false
	rightRed := false
	if s.connStatus != "connected" && s.connStatus != "" {
		rightInfo = s.connStatus
	}
	if s.status != "" {
		rightInfo = s.status
	}
	if s.term != nil && s.term.ScrollbackActive() {
		text, _, _ := s.term.Status()
		rightInfo = text
		rightBold = true
	} else if layerStatus != "" {
		rightInfo = layerStatus
		rightBold = layerStatusBold
		rightRed = layerStatusRed
	} else if s.connStatus == "reconnecting" {
		secs := int(time.Until(s.retryAt).Seconds()) + 1
		rightInfo = fmt.Sprintf("reconnecting to %s in %ds...", s.endpoint, secs)
		rightBold = true
		rightRed = true
	}
	suffix := "termd-tui"
	if s.version != "" && layerStatus != "" {
		suffix = "termd-tui " + s.version
	}
	sb.WriteString(renderTabBar(s.regionName, rightInfo, suffix, rightBold, rightRed, width))
	sb.WriteByte('\n')

	contentHeight := height - 1 // tab bar only
	if contentHeight < 1 {
		contentHeight = 1
	}
	scrollbackActive := s.term != nil && s.term.ScrollbackActive()
	showCursor := !hideCursor && !scrollbackActive
	disconnected := s.connStatus == "reconnecting"

	if s.term != nil {
		s.term.View(&sb, width, contentHeight, showCursor, disconnected)
	} else {
		// No terminal yet — render blank content area
		for i := range contentHeight {
			for range width {
				sb.WriteByte(' ')
			}
			if i < contentHeight-1 {
				sb.WriteByte('\n')
			}
		}
	}

	return sb.String()
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
