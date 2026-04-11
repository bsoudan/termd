package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HintLayer is a temporary layer pushed after startup to show
// "ctrl+b ? for help" in the status bar and the nxtermd logo in the
// upper right corner. It pops itself on hideHintMsg.
type HintLayer struct {
	registry *Registry
}

func (h *HintLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	if _, ok := msg.(hideHintMsg); ok {
		return QuitLayerMsg{}, nil, true
	}
	return nil, nil, false
}

var logoLines = [4]string{
	"             ██                       ",
	"████▄ ██ ██ ▀██▀▀ ▄█▀█▄ ████▄ ███▄███▄",
	"██ ██  ███   ██   ██▄█▀ ██ ▀▀ ██ ██ ██",
	"██ ██ ██ ██  ██   ▀█▄▄▄ ██    ██ ██ ██",
}

func (h *HintLayer) Activate() tea.Cmd { return nil }
func (h *HintLayer) Deactivate()       {}

func (h *HintLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	inner := logoStyle.Render(logoLines[0]) + "\n" +
		logoStyle.Render(logoLines[1]) + "\n" +
		logoStyle.Render(logoLines[2]) + "\n" +
		logoStyle.Render(logoLines[3])
	logo := logoBorder.Render(inner)

	boxW := lipgloss.Width(logo)
	x := width - boxW - 2
	if x < 0 {
		x = 0
	}

	return overlayLayers(logo, x, 2, 1)
}

var logoStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))

var logoBorder = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("14")).
	Padding(0, 1)

func (h *HintLayer) WantsKeyboardInput() *KeyboardFilter { return nil }

func (h *HintLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	rs.HasHint = true
	prefix := "ctrl+b"
	if h.registry != nil {
		prefix = h.registry.PrefixStr
	}
	return prefix + ", ? for help", lipgloss.Style{}
}
