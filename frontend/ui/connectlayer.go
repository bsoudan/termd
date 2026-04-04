package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ConnectLayer is an overlay that lets the user type a server address
// or select from recent/discovered servers. It handles RawInputMsg
// directly (not via focus mode) to avoid interleaving issues with long
// typed strings.
type ConnectLayer struct {
	input    []rune
	cursor   int
	selected int // -1 = text input, 0..N = suggestion index

	recents    []RecentServer
	discovered []DiscoveredServerMsg

	err string
}

func NewConnectLayer(recents []RecentServer) *ConnectLayer {
	return &ConnectLayer{
		selected: -1,
		recents:  recents,
	}
}

func (c *ConnectLayer) Activate() tea.Cmd { return nil }
func (c *ConnectLayer) Deactivate()       {}

func (c *ConnectLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RawInputMsg:
		return c.handleRaw([]byte(msg))
	case tea.KeyPressMsg:
		return c.handleKey(msg)
	case DiscoveredServerMsg:
		for _, d := range c.discovered {
			if d.Endpoint == msg.Endpoint {
				return nil, nil, true
			}
		}
		c.discovered = append(c.discovered, msg)
		return nil, nil, true
	case ConnectErrorMsg:
		c.err = msg.Error
		return nil, nil, true
	case tea.MouseMsg:
		return nil, nil, true
	}
	return nil, nil, false
}

// handleRaw processes raw input bytes directly, extracting printable
// characters and control sequences without going through pipeW/bubbletea
// key parsing. This avoids the interleaving race in focus-mode resend.
func (c *ConnectLayer) handleRaw(data []byte) (tea.Msg, tea.Cmd, bool) {
	pos := 0
	for pos < len(data) {
		_, _, n, _ := ansi.DecodeSequence(data[pos:], ansi.NormalState, nil)
		if n == 0 {
			break
		}
		seq := data[pos : pos+n]

		// Single-byte control characters.
		if n == 1 {
			switch seq[0] {
			case 0x1b: // ESC
				return QuitLayerMsg{}, nil, true
			case 0x0d, 0x0a: // Enter
				addr := strings.TrimSpace(string(c.input))
				if c.selected >= 0 {
					items := c.suggestions()
					if c.selected < len(items) {
						addr = items[c.selected].endpoint
					}
				}
				if addr == "" {
					return nil, nil, true
				}
				c.err = ""
				return QuitLayerMsg{}, cmdMsg(ConnectToServerMsg{Endpoint: addr}), true
			case 0x7f, 0x08: // Backspace / DEL
				if c.cursor > 0 {
					c.input = append(c.input[:c.cursor-1], c.input[c.cursor:]...)
					c.cursor--
					c.selected = -1
				}
				pos += n
				continue
			case 0x03: // Ctrl+C
				return QuitLayerMsg{}, nil, true
			}
		}

		// Arrow keys (CSI A/B).
		if n >= 3 && seq[0] == 0x1b && seq[1] == '[' {
			switch seq[n-1] {
			case 'A': // Up
				c.selected--
				if c.selected < -1 {
					c.selected = -1
				}
				pos += n
				continue
			case 'B': // Down
				items := c.suggestions()
				if c.selected < len(items)-1 {
					c.selected++
				}
				pos += n
				continue
			case 'C': // Right
				if c.cursor < len(c.input) {
					c.cursor++
				}
				pos += n
				continue
			case 'D': // Left
				if c.cursor > 0 {
					c.cursor--
				}
				pos += n
				continue
			}
		}

		// Printable characters.
		r, sz := utf8.DecodeRune(seq)
		if sz > 0 && unicode.IsPrint(r) {
			c.input = append(c.input, 0)
			copy(c.input[c.cursor+1:], c.input[c.cursor:])
			c.input[c.cursor] = r
			c.cursor++
			c.selected = -1
		}

		pos += n
	}
	return nil, nil, true
}

func (c *ConnectLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return QuitLayerMsg{}, nil, true
	case "enter":
		addr := strings.TrimSpace(string(c.input))
		if c.selected >= 0 {
			items := c.suggestions()
			if c.selected < len(items) {
				addr = items[c.selected].endpoint
			}
		}
		if addr == "" {
			return nil, nil, true
		}
		c.err = ""
		return QuitLayerMsg{}, cmdMsg(ConnectToServerMsg{Endpoint: addr}), true
	case "up":
		c.selected--
		if c.selected < -1 {
			c.selected = -1
		}
		return nil, nil, true
	case "down":
		items := c.suggestions()
		if c.selected < len(items)-1 {
			c.selected++
		}
		return nil, nil, true
	case "backspace":
		if c.cursor > 0 {
			c.input = append(c.input[:c.cursor-1], c.input[c.cursor:]...)
			c.cursor--
			c.selected = -1
		}
		return nil, nil, true
	case "left":
		if c.cursor > 0 {
			c.cursor--
		}
		return nil, nil, true
	case "right":
		if c.cursor < len(c.input) {
			c.cursor++
		}
		return nil, nil, true
	default:
		for _, r := range msg.Text {
			if unicode.IsPrint(r) {
				c.input = append(c.input, 0)
				copy(c.input[c.cursor+1:], c.input[c.cursor:])
				c.input[c.cursor] = r
				c.cursor++
				c.selected = -1
			}
		}
		return nil, nil, true
	}
}

type suggestion struct {
	label    string
	endpoint string
}

func (c *ConnectLayer) suggestions() []suggestion {
	var items []suggestion
	for _, r := range c.recents {
		items = append(items, suggestion{label: r.Label, endpoint: r.Address})
	}
	for _, d := range c.discovered {
		items = append(items, suggestion{label: d.Name, endpoint: d.Endpoint})
	}
	return items
}

func (c *ConnectLayer) View(width, height int, active bool) []*lipgloss.Layer {
	overlayW := width * 2 / 3
	if overlayW < 40 {
		overlayW = min(width, 40)
	}
	contentW := max(overlayW-paletteStyle.GetHorizontalFrameSize(), 20)

	content := c.buildContent(contentW, height)
	dialog := paletteStyle.Width(overlayW).Render(content)

	x := max((width-overlayW)/2, 0)

	return []*lipgloss.Layer{lipgloss.NewLayer(dialog).X(x).Y(0).Z(2)}
}

func (c *ConnectLayer) buildContent(w, height int) string {
	var prompt string
	if len(c.input) == 0 {
		prompt = statusBold.Render("> ") + paletteFaint.Render("type a server address...")
	} else {
		text := "> " + string(c.input)
		if len(text) > w {
			text = text[:w]
		}
		prompt = statusBold.Render(text)
	}

	separator := paletteFaint.Render(strings.Repeat("·", w))

	var lines []string
	items := c.suggestions()
	suggIdx := 0

	if len(c.recents) > 0 {
		lines = append(lines, paletteFaint.Render(" recent"))
		for _, r := range c.recents {
			line := c.renderSuggestion(r.Label, r.Address, relativeTime(r.Timestamp), w, suggIdx)
			lines = append(lines, line)
			suggIdx++
		}
	}

	if len(c.discovered) > 0 {
		if len(lines) > 0 {
			lines = append(lines, separator)
		}
		lines = append(lines, paletteFaint.Render(" discovered"))
		for _, d := range c.discovered {
			line := c.renderSuggestion(d.Name, d.Endpoint, "", w, suggIdx)
			lines = append(lines, line)
			suggIdx++
		}
	}

	maxVisible := max(height-4, 0)
	if len(lines) > maxVisible {
		lines = lines[:maxVisible]
	}

	var sb strings.Builder
	sb.WriteString(prompt)
	if c.err != "" {
		sb.WriteString("\n")
		errLine := " error: " + c.err
		if len(errLine) > w {
			errLine = errLine[:w]
		}
		sb.WriteString(statusBoldRed.Render(errLine))
	}
	sb.WriteString("\n")
	sb.WriteString(separator)
	if len(lines) > 0 || len(items) == 0 {
		sb.WriteString("\n")
		if len(lines) > 0 {
			sb.WriteString(strings.Join(lines, "\n"))
		}
	}

	return sb.String()
}

func (c *ConnectLayer) renderSuggestion(label, endpoint, age string, w, idx int) string {
	const pad = "  "
	const sep = " · "

	var line string
	if age != "" {
		line = fmt.Sprintf("%s%s%s%s%s%s",
			pad, label, sep, endpoint, sep, age)
	} else {
		line = fmt.Sprintf("%s%s%s%s",
			pad, label, sep, endpoint)
	}

	lineRunes := []rune(line)
	if len(lineRunes) < w {
		line += strings.Repeat(" ", w-len(lineRunes))
	} else if len(lineRunes) > w {
		line = string(lineRunes[:w])
	}

	if idx == c.selected {
		return helpSelected.Render(line)
	}
	faintSep := " " + paletteFaint.Render("·") + " "
	return strings.ReplaceAll(line, sep, faintSep)
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (c *ConnectLayer) WantsKeyboardInput() *KeyboardFilter { return nil }

func (c *ConnectLayer) Status() (string, lipgloss.Style) {
	return "connect to server", lipgloss.Style{}
}
