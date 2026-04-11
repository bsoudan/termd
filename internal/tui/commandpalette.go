package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// CommandPaletteLayer provides a VS Code-style command palette.
// A text input at the top fuzzy-matches against all registered commands,
// showing a suggestion panel below.
type CommandPaletteLayer struct {
	input    []rune
	cursor   int
	selected int
	matches  []paletteMatch
	all      []PaletteEntry
	registry *Registry
}

type paletteMatch struct {
	entry PaletteEntry
	score int
}

func NewCommandPaletteLayer(registry *Registry) *CommandPaletteLayer {
	all := registry.PaletteEntries()
	// Initial matches: all entries, sorted by original order.
	matches := make([]paletteMatch, len(all))
	for i, e := range all {
		matches[i] = paletteMatch{entry: e, score: 0}
	}
	return &CommandPaletteLayer{
		all:      all,
		matches:  matches,
		registry: registry,
	}
}

func (p *CommandPaletteLayer) Activate() tea.Cmd { return nil }
func (p *CommandPaletteLayer) Deactivate()       {}

func (p *CommandPaletteLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return p.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true
	}
	return nil, nil, false
}

func (p *CommandPaletteLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+g":
		return QuitLayerMsg{}, nil, true
	case "enter":
		if p.selected >= 0 && p.selected < len(p.matches) {
			m := p.matches[p.selected]
			cmd := cmdForBinding(m.entry.Command, m.entry.Args)
			return QuitLayerMsg{}, cmd, true
		}
		return QuitLayerMsg{}, nil, true
	case "up":
		if p.selected > 0 {
			p.selected--
		}
		return nil, nil, true
	case "down":
		if p.selected < len(p.matches)-1 {
			p.selected++
		}
		return nil, nil, true
	case "backspace":
		if p.cursor > 0 {
			p.input = append(p.input[:p.cursor-1], p.input[p.cursor:]...)
			p.cursor--
			p.updateMatches()
		}
		return nil, nil, true
	case "left":
		if p.cursor > 0 {
			p.cursor--
		}
		return nil, nil, true
	case "right":
		if p.cursor < len(p.input) {
			p.cursor++
		}
		return nil, nil, true
	default:
		for _, r := range msg.Text {
			if unicode.IsPrint(r) {
				p.input = append(p.input, 0)
				copy(p.input[p.cursor+1:], p.input[p.cursor:])
				p.input[p.cursor] = r
				p.cursor++
			}
		}
		p.updateMatches()
		return nil, nil, true
	}
}

func (p *CommandPaletteLayer) updateMatches() {
	query := strings.ToLower(string(p.input))
	if query == "" {
		p.matches = make([]paletteMatch, len(p.all))
		for i, e := range p.all {
			p.matches[i] = paletteMatch{entry: e}
		}
		p.selected = 0
		return
	}

	p.matches = nil
	for _, e := range p.all {
		name := commandInvocation(e.Command.Name, e.Args)
		candidate := strings.ToLower(name + " " + e.Command.Description)
		if score, ok := fuzzyScore(query, candidate); ok {
			p.matches = append(p.matches, paletteMatch{entry: e, score: score})
		}
	}
	// Sort by score descending (higher = better match).
	for i := 1; i < len(p.matches); i++ {
		for j := i; j > 0 && p.matches[j].score > p.matches[j-1].score; j-- {
			p.matches[j], p.matches[j-1] = p.matches[j-1], p.matches[j]
		}
	}
	if p.selected >= len(p.matches) {
		p.selected = max(len(p.matches)-1, 0)
	}
}

// fuzzyScore checks if all characters of query appear in order in candidate.
// Returns (score, matched). Higher score = better match. Bonus for matches
// at word boundaries.
func fuzzyScore(query, candidate string) (int, bool) {
	qi := 0
	score := 0
	for i := 0; i < len(candidate) && qi < len(query); i++ {
		if candidate[i] == query[qi] {
			qi++
			score++
			if i == 0 || candidate[i-1] == '-' || candidate[i-1] == ' ' {
				score += 10 // word boundary bonus
			}
		}
	}
	if qi < len(query) {
		return 0, false
	}
	return score, true
}

var paletteStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("14")).
	Padding(0, 1)

// paletteFaint matches the border color for internal separators and placeholder text.
var paletteFaint = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

func (p *CommandPaletteLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	overlayW := width * 2 / 3
	if overlayW < 40 {
		overlayW = min(width, 40)
	}
	// paletteStyle.Width sets the total rendered width.
	// Content area = overlayW - horizontal frame (border + padding).
	contentW := overlayW - paletteStyle.GetHorizontalFrameSize()
	if contentW < 20 {
		contentW = 20
	}

	content := p.buildContent(contentW, height)
	dialog := paletteStyle.Width(overlayW).Render(content)

	x := (width - overlayW) / 2
	if x < 0 {
		x = 0
	}

	return overlayLayers(dialog, x, 0, 2)
}

// buildContent builds the palette content string (prompt + separator +
// suggestions). Every line must be exactly overlayW display characters
// so the border renders cleanly.
func (p *CommandPaletteLayer) buildContent(overlayW, height int) string {
	inputStr := string(p.input)

	// Input line: "> query", truncated to overlayW.
	promptText := "> " + inputStr
	if len(promptText) > overlayW {
		promptText = promptText[:overlayW]
	}
	prompt := statusBold.Render(promptText)
	if len(p.input) == 0 {
		prompt = statusBold.Render("> ") + paletteFaint.Render("type a command...")
	}

	// Separator line matching the border's horizontal line character.
	separator := paletteFaint.Render(strings.Repeat("─", overlayW))

	// Suggestion panel.
	maxVisible := min(len(p.matches), height-4)
	if maxVisible < 0 {
		maxVisible = 0
	}

	// Measure column widths from visible matches.
	maxName, maxKey := 0, 0
	for i := range maxVisible {
		m := p.matches[i]
		name := commandInvocation(m.entry.Command.Name, m.entry.Args)
		if len(name) > maxName {
			maxName = len(name)
		}
		if len(m.entry.KeyDisplay) > maxKey {
			maxKey = len(m.entry.KeyDisplay)
		}
	}

	// Layout: name " · " desc [" · " key]
	// All lines must be exactly overlayW display cells.
	const sep = " • "
	sepW := utf8.RuneCountInString(sep)
	fixedW := maxName + sepW
	if maxKey > 0 {
		fixedW += sepW + maxKey
	}
	descW := overlayW - fixedW
	if descW < 4 {
		maxName -= (4 - descW)
		if maxName < 4 {
			maxName = 4
		}
		fixedW = maxName + sepW
		if maxKey > 0 {
			fixedW += sepW + maxKey
		}
		descW = overlayW - fixedW
		if descW < 1 {
			descW = 1
		}
	}

	faintSep := " " + paletteFaint.Render("•") + " "

	var suggestions []string
	for i := range maxVisible {
		m := p.matches[i]
		name := commandInvocation(m.entry.Command.Name, m.entry.Args)
		if len(name) > maxName {
			name = name[:maxName]
		}
		desc := m.entry.Command.Description
		if m.entry.Args != "" {
			desc += " " + m.entry.Args
		}
		if len(desc) > descW {
			desc = desc[:descW-1] + "…"
		}

		// Build with plain sep for width calculation.
		var line string
		if maxKey > 0 {
			line = fmt.Sprintf("%-*s%s%-*s%s%*s",
				maxName, name,
				sep, descW, desc,
				sep, maxKey, m.entry.KeyDisplay)
		} else {
			line = fmt.Sprintf("%-*s%s%-*s",
				maxName, name,
				sep, descW, desc)
		}

		// Ensure exactly overlayW display width.
		lineRunes := []rune(line)
		if len(lineRunes) < overlayW {
			line += strings.Repeat(" ", overlayW-len(lineRunes))
		} else if len(lineRunes) > overlayW {
			line = string(lineRunes[:overlayW])
		}

		if i == p.selected {
			// Selected: reverse the whole plain line (no embedded ANSI
			// that would break the reverse bar).
			line = helpSelected.Render(line)
		} else {
			// Non-selected: replace plain dots with faint-styled dots.
			line = strings.ReplaceAll(line, sep, faintSep)
		}
		suggestions = append(suggestions, line)
	}

	content := prompt + "\n" + separator + "\n"
	if len(suggestions) > 0 {
		content += strings.Join(suggestions, "\n")
	} else {
		content += paletteFaint.Render(" no matches")
	}
	return content
}

func (p *CommandPaletteLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }

func (p *CommandPaletteLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	return "run command", lipgloss.Style{}
}
