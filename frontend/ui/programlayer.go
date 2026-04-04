package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nxtermd/frontend/protocol"
)

// ProgramPickerLayer shows available programs and lets the user select one.
type ProgramPickerLayer struct {
	cursor   int
	programs []protocol.ProgramInfo
}

func NewProgramPickerLayer(programs []protocol.ProgramInfo) *ProgramPickerLayer {
	return &ProgramPickerLayer{programs: programs}
}

func (p *ProgramPickerLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return p.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true
	}
	return nil, nil, false
}

func (p *ProgramPickerLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "esc":
		return QuitLayerMsg{}, nil, true
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		return nil, nil, true
	case "down", "j":
		if p.cursor < len(p.programs)-1 {
			p.cursor++
		}
		return nil, nil, true
	case "enter":
		name := p.programs[p.cursor].Name
		return QuitLayerMsg{}, cmdMsg(SessionCmd{Name: "open-tab", Args: name}), true
	default:
		return nil, nil, true
	}
}

func (p *ProgramPickerLayer) Activate() tea.Cmd { return nil }
func (p *ProgramPickerLayer) Deactivate()       {}

func (p *ProgramPickerLayer) View(width, height int, active bool) []*lipgloss.Layer {
	var lines []string
	for i, prog := range p.programs {
		line := fmt.Sprintf("  %s", prog.Name)
		if i == p.cursor {
			line = fmt.Sprintf("▸ %s", prog.Name)
			line = helpSelected.Render(line)
		}
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n")

	overlayW := 30
	for _, prog := range p.programs {
		if w := len(prog.Name) + 4; w > overlayW {
			overlayW = w
		}
	}
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := overlayHint.Render("• ↑↓/enter: select • q/esc: close •")
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

func (p *ProgramPickerLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }

func (p *ProgramPickerLayer) Status() (string, lipgloss.Style) {
	return "select program", lipgloss.Style{}
}
