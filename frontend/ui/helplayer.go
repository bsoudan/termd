package ui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HelpLayer shows available keybindings in a table, grouped by category.
type HelpLayer struct {
	table   table.Model
	entries []displayEntry // parallel to table rows for dispatch
}

func NewHelpLayer(registry *Registry) *HelpLayer {
	entries := registry.DisplayEntries()

	// Compute column widths from content.
	maxKey := 0
	maxDesc := 0
	for _, e := range entries {
		if e.isHeader {
			continue
		}
		if len(e.keyDisplay) > maxKey {
			maxKey = len(e.keyDisplay)
		}
		if len(e.description) > maxDesc {
			maxDesc = len(e.description)
		}
	}
	if maxKey < 10 {
		maxKey = 10
	}
	if maxDesc < 20 {
		maxDesc = 20
	}

	cols := []table.Column{
		{Title: "", Width: maxKey},
		{Title: "", Width: maxDesc},
	}

	rows := make([]table.Row, len(entries))
	for i, e := range entries {
		if e.isHeader {
			rows[i] = table.Row{"── " + e.keyDisplay + " ──", ""}
		} else {
			rows[i] = table.Row{e.keyDisplay, e.description}
		}
	}

	s := table.DefaultStyles()
	s.Header = lipgloss.Style{}
	s.Selected = lipgloss.NewStyle().Bold(true).Reverse(true)
	s.Cell = lipgloss.NewStyle().PaddingRight(1)

	km := table.KeyMap{
		LineUp:   key.NewBinding(key.WithKeys("up", "k")),
		LineDown: key.NewBinding(key.WithKeys("down", "j")),
	}

	// Total width: column widths + cell padding (1 right pad per cell).
	totalW := maxKey + maxDesc + 2

	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithStyles(s),
		table.WithKeyMap(km),
		table.WithWidth(totalW),
		table.WithHeight(len(rows)),
	)

	return &HelpLayer{table: t, entries: entries}
}

func (h *HelpLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return h.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true
	}
	return nil, nil, false
}

func (h *HelpLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "esc", "?":
		return QuitLayerMsg{}, nil, true
	case "enter":
		idx := h.table.Cursor()
		if idx >= 0 && idx < len(h.entries) {
			if e := h.entries[idx]; e.cmdFn != nil {
				return QuitLayerMsg{}, e.cmdFn(), true
			}
		}
		return nil, nil, true
	default:
		// Check for direct chord key shortcut.
		for _, e := range h.entries {
			if e.chordKey != "" && msg.String() == e.chordKey && e.cmdFn != nil {
				return QuitLayerMsg{}, e.cmdFn(), true
			}
		}
		// Pass navigation keys to the table.
		h.table, _ = h.table.Update(msg)
		return nil, nil, true
	}
}

func (h *HelpLayer) Activate() tea.Cmd { return nil }
func (h *HelpLayer) Deactivate()       {}

func (h *HelpLayer) View(width, height int, active bool) []*lipgloss.Layer {
	// Size the table to fit within the terminal.
	maxH := height - 6 // room for border + help text
	if maxH < 5 {
		maxH = 5
	}
	if len(h.entries) < maxH {
		maxH = len(h.entries)
	}
	h.table.SetHeight(maxH)

	content := h.table.View()

	overlayW := h.table.Width()
	if overlayW < 38 {
		overlayW = 38
	}
	// Cap to 80% of terminal width.
	if cap := width * 4 / 5; overlayW > cap && cap >= 38 {
		overlayW = cap
	}

	dialog := overlayBorder.Width(overlayW).Render(content)

	help := statusFaint.Render("• ↑↓/enter: select • q/esc: close •")
	dialogLines := lipgloss.JoinVertical(lipgloss.Left, dialog, help)

	dialogH := lipgloss.Height(dialogLines)
	x := (width - overlayW - 2) / 2
	y := (height - dialogH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	return []*lipgloss.Layer{lipgloss.NewLayer(dialogLines).X(x).Y(y).Z(1)}
}

func (h *HelpLayer) Status() (string, lipgloss.Style) { return "help", lipgloss.Style{} }
