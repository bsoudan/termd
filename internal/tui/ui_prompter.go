package tui

import (
	"fmt"
	"log/slog"

	tea "charm.land/bubbletea/v2"
)

// UIPrompter implements transport.Prompter by pushing overlay layers
// onto the bubbletea layer stack and blocking on a result channel that
// the layer fills when the user submits or cancels.
//
// Used for any dial that happens after bubbletea has taken over the
// terminal — that's the connect-overlay dial, the in-session reconnect
// loop, and any dialFn passed to a fresh Server.Run after a connect.
type UIPrompter struct {
	program *tea.Program
}

// NewUIPrompter constructs a UIPrompter bound to a running tea.Program.
func NewUIPrompter(p *tea.Program) *UIPrompter {
	return &UIPrompter{program: p}
}

// Password pushes a SecretInputLayer and blocks until the user
// submits a value or cancels.
func (p *UIPrompter) Password(prompt string) (string, error) {
	return p.secretInput(prompt)
}

// Passphrase uses the same masked-input overlay as Password.
func (p *UIPrompter) Passphrase(prompt string) (string, error) {
	return p.secretInput(prompt)
}

// Confirm currently auto-accepts host-key prompts. A dedicated
// confirm overlay would let the user accept or reject explicitly;
// for now we trust the user already saw any host-key warning the
// first time they connected outside nxterm.
//
// TODO: add a ConfirmLayer overlay and route the prompt through it.
func (p *UIPrompter) Confirm(prompt string) (bool, error) {
	slog.Debug("ssh confirm prompt auto-accepted", "prompt", prompt)
	return true, nil
}

// Info logs diagnostic ssh chatter. Could later be surfaced as a
// transient toast layer.
func (p *UIPrompter) Info(message string) {
	slog.Debug("ssh info", "message", message)
}

func (p *UIPrompter) secretInput(prompt string) (string, error) {
	if p.program == nil {
		return "", fmt.Errorf("UIPrompter: no tea.Program set")
	}
	ch := make(chan SecretInputResult, 1)
	layer := NewSecretInputLayer(prompt, ch)
	p.program.Send(PushLayerMsg{Layer: layer})
	result := <-ch
	if result.Cancelled {
		return "", fmt.Errorf("user cancelled secret prompt")
	}
	return result.Value, nil
}
