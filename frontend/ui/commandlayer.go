package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// CommandLayer is a temporary layer pushed when ctrl+b is detected.
// It captures the next KeyPressMsg, dispatches it as a specific message
// for session to handle, and pops itself. It has no reference to
// session — all communication is via message passing.
type CommandLayer struct{}

func (c *CommandLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, nil, false
	}

	cmd := c.dispatch(key)
	return QuitLayerMsg{}, cmd, true
}

func (c *CommandLayer) dispatch(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "d":
		return cmdMsg(DetachRequestMsg{})
	case "ctrl+b":
		return cmdMsg(SendLiteralPrefixMsg{})
	case "l":
		return cmdMsg(OpenOverlayMsg{Name: "logviewer"})
	case "?":
		return cmdMsg(OpenOverlayMsg{Name: "help"})
	case "s":
		return cmdMsg(OpenOverlayMsg{Name: "status"})
	case "n":
		return cmdMsg(OpenOverlayMsg{Name: "release notes"})
	case "[":
		return cmdMsg(EnterScrollbackMsg{})
	case "r":
		return cmdMsg(RefreshScreenMsg{})
	case "c":
		return cmdMsg(SpawnRegionMsg{})
	case "x":
		return cmdMsg(CloseTabMsg{})
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx, _ := strconv.Atoi(msg.String())
		return cmdMsg(SwitchTabMsg{Index: idx - 1})
	default:
		return nil
	}
}

func cmdMsg(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}

func (c *CommandLayer) Activate() tea.Cmd                            { return nil }
func (c *CommandLayer) Deactivate()                                   {}
func (c *CommandLayer) View(width, height int, active bool) []*lipgloss.Layer { return nil }
func (c *CommandLayer) Status() (string, lipgloss.Style)              { return "?", lipgloss.Style{} }


// HintLayer is a temporary layer pushed after startup to show
// "ctrl+b ? for help" in the status bar and the termd logo in the
// upper right corner. It pops itself on hideHintMsg.
type HintLayer struct{}

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
		pad + logoStyle.Render(logoLines[2]) + pad + "\n" +
		blank

	x := width - logoWidth - 3
	if x < 0 {
		x = 0
	}

	return []*lipgloss.Layer{lipgloss.NewLayer(logo).X(x).Y(2).Z(1)}
}

var logoStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))

func (h *HintLayer) Status() (string, lipgloss.Style) { return "ctrl+b ? for help", lipgloss.Style{} }
