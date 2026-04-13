package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	case tea.KeyPressMsg:
		return c.handleKey(msg)
	case DiscoveredServerMsg:
		// Replace in place if we already have this endpoint, so a
		// refreshed announcement with an updated session list takes
		// effect immediately.
		for i, d := range c.discovered {
			if d.Endpoint == msg.Endpoint {
				c.discovered[i] = msg
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


func (c *ConnectLayer) handleKey(msg tea.KeyPressMsg) (tea.Msg, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return QuitLayerMsg{}, nil, true
	case "enter":
		endpoint, session := c.selectedConnect()
		if endpoint == "" {
			return nil, nil, true
		}
		c.err = ""
		return QuitLayerMsg{}, cmdMsg(ConnectToServerMsg{Endpoint: endpoint, Session: session}), true
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
		if c.removeSelectedRecent() {
			return nil, nil, true
		}
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
	label    string // friendly name shown in the picker
	display  string // address column shown next to the label (may be "session@endpoint")
	endpoint string // server endpoint to dial (no session prefix)
	session  string // session name to request, or "" for the server default
}

func (c *ConnectLayer) suggestions() []suggestion {
	var items []suggestion
	for _, r := range c.recents {
		// Recents are stored as the literal address the user picked,
		// which may already be in "session@endpoint" form. Split it
		// here so the dial path always sees a bare endpoint, while the
		// display column reproduces the original combined form.
		session, endpoint := splitSessionEndpoint(r.Address)
		items = append(items, suggestion{
			label:    r.Label,
			display:  r.Address,
			endpoint: endpoint,
			session:  session,
		})
	}
	for _, d := range c.discovered {
		if len(d.Sessions) == 0 {
			items = append(items, suggestion{
				label:    d.Name,
				display:  d.Endpoint,
				endpoint: d.Endpoint,
			})
			continue
		}
		for _, sess := range d.Sessions {
			items = append(items, suggestion{
				label:    d.Name,
				display:  sess + "@" + d.Endpoint,
				endpoint: d.Endpoint,
				session:  sess,
			})
		}
	}
	return items
}

// removeSelectedRecent removes the currently-highlighted recent entry
// from both the in-memory list and the on-disk recents file. Returns
// true when the current selection points at a recent (and the action
// applied), false otherwise — which lets the caller fall through to
// the default backspace behaviour of editing the input field.
func (c *ConnectLayer) removeSelectedRecent() bool {
	if c.selected < 0 || c.selected >= len(c.recents) {
		return false
	}
	addr := c.recents[c.selected].Address
	if err := RemoveRecent(addr); err != nil {
		c.err = "remove recent: " + err.Error()
		return true
	}
	c.recents = append(c.recents[:c.selected], c.recents[c.selected+1:]...)
	total := len(c.recents) + len(c.discovered)
	if c.selected >= total {
		if total == 0 {
			c.selected = -1
		} else {
			c.selected = total - 1
		}
	}
	return true
}

// selectedConnect returns the (endpoint, session) pair the user is
// about to connect to: either the highlighted suggestion or, if none
// is selected, the typed input parsed for a "session@" prefix. Returns
// ("", "") when there is nothing to connect to.
func (c *ConnectLayer) selectedConnect() (endpoint, session string) {
	if c.selected >= 0 {
		items := c.suggestions()
		if c.selected < len(items) {
			s := items[c.selected]
			return s.endpoint, s.session
		}
	}
	addr := strings.TrimSpace(string(c.input))
	if addr == "" {
		return "", ""
	}
	session, endpoint = splitSessionEndpoint(addr)
	return endpoint, session
}

// splitSessionEndpoint parses an address that may begin with a
// "session@" prefix. A leading token followed by "@" is treated as a
// session name only when it contains no ":" or "/" (which would make
// it part of a URL or filesystem path). This lets ssh:user@host and
// unix paths flow through unchanged.
func splitSessionEndpoint(s string) (session, endpoint string) {
	at := strings.Index(s, "@")
	if at <= 0 {
		return "", s
	}
	prefix := s[:at]
	if strings.ContainsAny(prefix, ":/") {
		return "", s
	}
	return prefix, s[at+1:]
}

func (c *ConnectLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	overlayW := width * 2 / 3
	if overlayW < 40 {
		overlayW = min(width, 40)
	}
	contentW := max(overlayW-paletteStyle.GetHorizontalFrameSize(), 20)

	content := c.buildContent(contentW, height)
	dialog := paletteStyle.Width(overlayW).Render(content)

	hint := overlayHint.Render("• " + c.hintText() + " •")
	helpPad := max((lipgloss.Width(dialog)-lipgloss.Width(hint))/2, 0)
	dialog = dialog + "\n" + strings.Repeat(" ", helpPad) + hint

	x := max((width-overlayW)/2, 0)

	return overlayLayers(dialog, x, 0, 2)
}

// hintText returns the keybinding hint shown beneath the dialog.
// When a recent entry is highlighted the hint advertises that bksp
// removes it; otherwise bksp simply edits the input field.
func (c *ConnectLayer) hintText() string {
	if c.selected >= 0 && c.selected < len(c.recents) {
		return "↑↓ select · enter connect · bksp remove recent · esc cancel"
	}
	return "↑↓ select · enter connect · esc cancel"
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

	separator := paletteFaint.Render(strings.Repeat("─", w))

	var lines []string
	items := c.suggestions()
	suggIdx := 0

	if len(c.recents) > 0 {
		lines = append(lines, paletteFaint.Render(" recent"))
		for i := range c.recents {
			s := items[suggIdx]
			line := c.renderSuggestion(s.label, s.display, relativeTime(c.recents[i].Timestamp), w, suggIdx)
			lines = append(lines, line)
			suggIdx++
		}
	}

	if len(c.discovered) > 0 {
		if len(lines) > 0 {
			lines = append(lines, separator)
		}
		lines = append(lines, paletteFaint.Render(" discovered"))
		for suggIdx < len(items) {
			s := items[suggIdx]
			line := c.renderSuggestion(s.label, s.display, "", w, suggIdx)
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

func (c *ConnectLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }

func (c *ConnectLayer) Status(rs *RenderState) (string, lipgloss.Style) {
	return "connect to server", lipgloss.Style{}
}
