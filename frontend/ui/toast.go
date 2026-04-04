package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// hideToastMsg is sent after the toast duration expires.
type hideToastMsg struct{ id int }

var nextToastID int

// ToastLayer displays a brief floating notification in the upper-right
// corner. It auto-dismisses after the given duration.
type ToastLayer struct {
	id   int
	text string
}

func (t *ToastLayer) Activate() tea.Cmd   { return nil }
func (t *ToastLayer) Deactivate()         {}

func (t *ToastLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	if h, ok := msg.(hideToastMsg); ok && h.id == t.id {
		return QuitLayerMsg{}, nil, true
	}
	return nil, nil, false
}

var toastStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("8")).
	Padding(0, 1)

func (t *ToastLayer) View(width, height int, active bool) []*lipgloss.Layer {
	content := toastStyle.Render(t.text)
	contentWidth := lipgloss.Width(content)
	x := max(width-contentWidth-1, 0)
	return []*lipgloss.Layer{lipgloss.NewLayer(content).X(x).Y(2).Z(3)}
}

func (t *ToastLayer) WantsKeyboardInput() *KeyboardFilter { return nil }

func (t *ToastLayer) Status() (string, lipgloss.Style) {
	return "", lipgloss.Style{}
}

// ShowToast returns a tea.Cmd that pushes a toast notification which
// auto-dismisses after the given duration.
func ShowToast(text string, duration time.Duration) tea.Cmd {
	text = strings.TrimRight(text, "\n")
	nextToastID++
	id := nextToastID
	toast := &ToastLayer{id: id, text: text}
	pushCmd := func() tea.Msg { return PushLayerMsg{Layer: toast} }
	timerCmd := tea.Tick(duration, func(time.Time) tea.Msg {
		return hideToastMsg{id: id}
	})
	return tea.Batch(pushCmd, timerCmd)
}
