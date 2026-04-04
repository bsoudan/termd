package ui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// SessionNameLayer prompts the user for a session name via a text input dialog.
type SessionNameLayer struct {
	input  []rune
	cursor int
}

func (l *SessionNameLayer) Activate() tea.Cmd { return nil }
func (l *SessionNameLayer) Deactivate()       {}

func (l *SessionNameLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return l.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true
	}
	return nil, nil, false
}

func (l *SessionNameLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		return QuitLayerMsg{}, nil, true
	case "enter":
		name := strings.TrimSpace(string(l.input))
		if name == "" {
			return QuitLayerMsg{}, nil, true
		}
		return QuitLayerMsg{}, cmdMsg(MainCmd{Name: "open-session", Args: name}), true
	case "backspace":
		if l.cursor > 0 {
			l.input = append(l.input[:l.cursor-1], l.input[l.cursor:]...)
			l.cursor--
		}
		return nil, nil, true
	case "left":
		if l.cursor > 0 {
			l.cursor--
		}
		return nil, nil, true
	case "right":
		if l.cursor < len(l.input) {
			l.cursor++
		}
		return nil, nil, true
	default:
		// Insert printable runes
		for _, r := range msg.Text {
			if unicode.IsPrint(r) {
				l.input = append(l.input, 0)
				copy(l.input[l.cursor+1:], l.input[l.cursor:])
				l.input[l.cursor] = r
				l.cursor++
			}
		}
		return nil, nil, true
	}
}

func (l *SessionNameLayer) View(width, height int, active bool) []*lipgloss.Layer {
	label := "Session name: "
	inputStr := string(l.input)

	// Build input display with cursor
	var display string
	if l.cursor < len(l.input) {
		display = fmt.Sprintf("%s%s%s%s%s",
			label,
			string(l.input[:l.cursor]),
			helpSelected.Render(string(l.input[l.cursor])),
			string(l.input[l.cursor+1:]),
			strings.Repeat(" ", max(20-len(inputStr), 1)),
		)
	} else {
		display = fmt.Sprintf("%s%s%s%s",
			label,
			inputStr,
			helpSelected.Render(" "),
			strings.Repeat(" ", max(20-len(inputStr)-1, 0)),
		)
	}

	overlayW := max(len(label)+20, 38)
	dialog := overlayBorder.Width(overlayW).Render(display)

	help := statusFaint.Render("• enter: create • esc: cancel •")
	dialogLines := strings.Split(dialog, "\n")
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

func (l *SessionNameLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }

func (l *SessionNameLayer) Status() (string, lipgloss.Style) {
	return "new session", lipgloss.Style{}
}
