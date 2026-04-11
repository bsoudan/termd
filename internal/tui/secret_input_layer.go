package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// SecretInputResult is sent on a SecretInputLayer's result channel
// when the user either submits a value (Enter) or cancels (Esc).
// Cancelled is true when the user pressed Esc; in that case Value is
// empty.
type SecretInputResult struct {
	Value     string
	Cancelled bool
}

// SecretInputLayer is a centered modal that asks the user for a
// password or passphrase. The typed value is displayed as bullets so
// it does not appear in shoulder-surfing distance, and is never logged.
//
// On Enter or Esc the layer sends a SecretInputResult to its result
// channel and emits QuitLayerMsg. The result channel is owned by the
// caller (typically a Prompter wrapping the dial path) and is the
// cross-goroutine handoff between bubbletea's Update and the dial
// goroutine that's blocked waiting for credentials.
type SecretInputLayer struct {
	prompt string
	input  []rune
	result chan<- SecretInputResult
	sent   bool
}

// NewSecretInputLayer constructs a masked-input overlay. prompt is the
// literal label shown to the user (typically the line ssh wrote to
// its tty, e.g. "user@host's password: "). result receives exactly one
// SecretInputResult — caller should size the channel to 1 to avoid
// blocking the bubbletea goroutine.
func NewSecretInputLayer(prompt string, result chan<- SecretInputResult) *SecretInputLayer {
	return &SecretInputLayer{
		prompt: strings.TrimSpace(prompt),
		result: result,
	}
}

func (l *SecretInputLayer) Activate() tea.Cmd { return nil }
func (l *SecretInputLayer) Deactivate()       {}

func (l *SecretInputLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }

func (l *SecretInputLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	return "secret input", lipgloss.Style{}
}

func (l *SecretInputLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return l.handleKey(msg)
	case tea.MouseMsg:
		return nil, nil, true
	}
	return nil, nil, false
}

func (l *SecretInputLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		l.send(SecretInputResult{Cancelled: true})
		return QuitLayerMsg{}, nil, true
	case "enter":
		l.send(SecretInputResult{Value: string(l.input)})
		return QuitLayerMsg{}, nil, true
	case "backspace":
		if len(l.input) > 0 {
			l.input = l.input[:len(l.input)-1]
		}
		return nil, nil, true
	case "ctrl+u":
		l.input = l.input[:0]
		return nil, nil, true
	default:
		for _, r := range msg.Text {
			if unicode.IsPrint(r) {
				l.input = append(l.input, r)
			}
		}
		return nil, nil, true
	}
}

// send delivers result on the result channel exactly once. Subsequent
// calls (e.g. if Deactivate fires for some reason) are no-ops. The
// send is non-blocking; the caller is expected to size the channel
// at 1 so it always succeeds.
func (l *SecretInputLayer) send(r SecretInputResult) {
	if l.sent {
		return
	}
	l.sent = true
	select {
	case l.result <- r:
	default:
	}
}

func (l *SecretInputLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	// Mask each typed rune as a bullet. The bullet count gives the
	// user enough feedback to know their keystrokes are landing
	// without revealing the length to onlookers more than necessary.
	masked := strings.Repeat("•", len(l.input))

	const minInputWidth = 24
	pad := minInputWidth - len([]rune(masked))
	if pad < 1 {
		pad = 1
	}
	display := l.prompt + " " + masked + helpSelected.Render(" ") + strings.Repeat(" ", pad)

	overlayW := max(lipgloss.Width(l.prompt)+minInputWidth+2, 40)
	dialog := overlayBorder.Width(overlayW).Render(display)

	help := overlayHint.Render("• enter: submit • esc: cancel •")
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
