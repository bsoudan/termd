package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HintLayer is a temporary layer pushed after startup to show
// "ctrl+b ? for help" in the status bar and the termd logo in the
// upper right corner. It pops itself on hideHintMsg.
type HintLayer struct {
	registry *Registry
	version  string
}

func (h *HintLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	if _, ok := msg.(hideHintMsg); ok {
		return QuitLayerMsg{}, nil, true
	}
	return nil, nil, false
}

var logoLines = [3]string{
	"▀█▀ █▀▀ █▀▀▄ █▀▄▀█ █▀▄",
	" █  █▀▀ █▀▀▘ █ ▀ █ █ █",
	" ▀  ▀▀▀ ▀  ▀ ▀   ▀ ▀▀ ",
}

const logoWidth = 24 // display width of each logo line

func (h *HintLayer) Activate() tea.Cmd { return nil }
func (h *HintLayer) Deactivate()       {}

func (h *HintLayer) View(width, height int, active bool) []*lipgloss.Layer {
	pad := " "
	blank := pad + strings.Repeat(" ", logoWidth) + pad
	logo := blank + "\n" +
		pad + logoStyle.Render(logoLines[0]) + pad + "\n" +
		pad + logoStyle.Render(logoLines[1]) + pad + "\n" +
		pad + logoStyle.Render(logoLines[2]) + pad + "\n"
	if h.version != "" {
		ver := versionStyle.Render(h.version)
		verPad := (logoWidth + 2 - lipgloss.Width(ver)) / 2
		if verPad < 1 {
			verPad = 1
		}
		logo += strings.Repeat(" ", verPad) + ver + "\n"
	}
	logo += blank

	x := width - logoWidth - 3
	if x < 0 {
		x = 0
	}

	return []*lipgloss.Layer{lipgloss.NewLayer(logo).X(x).Y(2).Z(1)}
}

var versionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

var logoStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))

func (h *HintLayer) WantsKeyboardInput() *KeyboardFilter { return nil }

func (h *HintLayer) Status() (string, lipgloss.Style) {
	prefix := "ctrl+b"
	if h.registry != nil {
		prefix = h.registry.PrefixStr
	}
	return prefix + ", ? for help", lipgloss.Style{}
}
