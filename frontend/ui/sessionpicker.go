package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sessionInfo describes a session for the picker display.
type sessionInfo struct {
	name        string
	regionCount int
	active      bool
}

// SessionPickerLayer shows available sessions and lets the user select one.
type SessionPickerLayer struct {
	cursor   int
	sessions []sessionInfo
}

func NewSessionPickerLayer(sessions []sessionInfo) *SessionPickerLayer {
	// Place cursor on the active session.
	cursor := 0
	for i, s := range sessions {
		if s.active {
			cursor = i
			break
		}
	}
	return &SessionPickerLayer{cursor: cursor, sessions: sessions}
}

func (p *SessionPickerLayer) Activate() tea.Cmd { return nil }
func (p *SessionPickerLayer) Deactivate()       {}

func (p *SessionPickerLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return p.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true
	}
	return nil, nil, false
}

func (p *SessionPickerLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "esc", "w":
		return QuitLayerMsg{}, nil, true
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		return nil, nil, true
	case "down", "j":
		if p.cursor < len(p.sessions)-1 {
			p.cursor++
		}
		return nil, nil, true
	case "enter":
		return QuitLayerMsg{}, cmdMsg(MainCmd{Name: "switch-session", Args: fmt.Sprintf("%d", p.cursor+1)}), true
	default:
		return nil, nil, true
	}
}

func (p *SessionPickerLayer) View(width, height int, active bool) []*lipgloss.Layer {
	var lines []string
	for i, s := range p.sessions {
		indicator := "  "
		if s.active {
			indicator = "* "
		}
		label := fmt.Sprintf("%s%s (%d regions)", indicator, s.name, s.regionCount)
		if i == p.cursor {
			label = fmt.Sprintf("▸ %s (%d regions)", s.name, s.regionCount)
			label = helpSelected.Render(label)
		}
		lines = append(lines, label)
	}
	content := strings.Join(lines, "\n")

	overlayW := 38
	for _, s := range p.sessions {
		if w := len(s.name) + 20; w > overlayW {
			overlayW = w
		}
	}
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := statusFaint.Render("• ↑↓/enter: select • q/esc: close •")
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

func (p *SessionPickerLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }

func (p *SessionPickerLayer) Status() (string, lipgloss.Style) {
	return "switch session", lipgloss.Style{}
}
