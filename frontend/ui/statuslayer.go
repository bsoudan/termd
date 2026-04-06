package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nxtermd/frontend/protocol"
)

// StatusLayer displays server and terminal status in a centered dialog.
type StatusLayer struct {
	status *protocol.StatusResponse
	caps   StatusCaps
}

// StatusCaps captures terminal capability data at open time.
type StatusCaps struct {
	Hostname      string
	Endpoint      string
	SessionName   string
	Version       string
	ConnStatus    string
	KeyboardFlags int
	BgDark        *bool
	TermEnv       map[string]string
	MouseModes    string
	Modes         string

	ClientUpgradeAvail bool
	ClientUpgradeVer   string
	ServerUpgradeAvail bool
	ServerUpgradeVer   string
}

func NewStatusLayer(caps StatusCaps) *StatusLayer {
	return &StatusLayer{caps: caps}
}

func (s *StatusLayer) SetStatus(resp *protocol.StatusResponse) {
	s.status = resp
}

func (s *StatusLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "s":
			return QuitLayerMsg{}, nil, true
		}
		return nil, nil, true
	case tea.MouseMsg:
		return nil, nil, true // absorb mouse events
	}
	return nil, nil, false
}

func (s *StatusLayer) Activate() tea.Cmd { return nil }
func (s *StatusLayer) Deactivate()       {}

// View returns a positioned dialog layer for compositing.
func (s *StatusLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	var lines []string

	header := "nxterm:"
	if s.caps.ClientUpgradeAvail {
		header += " (upgrade available: " + s.caps.ClientUpgradeVer + ")"
	}
	lines = append(lines, header)
	lines = append(lines, fmt.Sprintf("  Hostname:  %s", s.caps.Hostname))
	lines = append(lines, fmt.Sprintf("  Version:   %s", s.caps.Version))
	endpointStr := s.caps.Endpoint
	if s.caps.SessionName != "" {
		endpointStr = s.caps.SessionName + "@" + endpointStr
	}
	if s.caps.ConnStatus == "reconnecting" {
		endpointStr += " (disconnected)"
	}
	lines = append(lines, fmt.Sprintf("  Endpoint:  %s", endpointStr))
	lines = append(lines, "")

	lines = append(lines, "terminal:")
	if term, ok := s.caps.TermEnv["TERM"]; ok {
		lines = append(lines, fmt.Sprintf("  TERM:      %s", term))
	}
	if ct, ok := s.caps.TermEnv["COLORTERM"]; ok {
		lines = append(lines, fmt.Sprintf("  COLORTERM: %s", ct))
	}
	if tp, ok := s.caps.TermEnv["TERM_PROGRAM"]; ok {
		lines = append(lines, fmt.Sprintf("  Program:   %s", tp))
	}
	if s.caps.KeyboardFlags > 0 {
		var kbCaps []string
		if s.caps.KeyboardFlags&1 != 0 {
			kbCaps = append(kbCaps, "disambiguate")
		}
		if s.caps.KeyboardFlags&2 != 0 {
			kbCaps = append(kbCaps, "event-types")
		}
		if s.caps.KeyboardFlags&4 != 0 {
			kbCaps = append(kbCaps, "alt-keys")
		}
		if s.caps.KeyboardFlags&8 != 0 {
			kbCaps = append(kbCaps, "all-as-escapes")
		}
		lines = append(lines, fmt.Sprintf("  Keyboard:  kitty (%s)", strings.Join(kbCaps, ", ")))
	} else {
		lines = append(lines, "  Keyboard:  legacy")
	}
	if s.caps.BgDark != nil {
		if *s.caps.BgDark {
			lines = append(lines, "  Background: dark")
		} else {
			lines = append(lines, "  Background: light")
		}
	}
	if s.caps.MouseModes != "" {
		lines = append(lines, fmt.Sprintf("  Mouse:     %s", s.caps.MouseModes))
	}
	if s.caps.Modes != "" {
		lines = append(lines, "  Modes:")
		// Soft-wrap the comma-separated mode list to fit the dialog
		// width. Indent continuation lines so they read as a list.
		const wrapAt = 44
		const indent = "    "
		var line strings.Builder
		first := true
		for _, mode := range strings.Split(s.caps.Modes, ", ") {
			if first {
				line.WriteString(indent)
				line.WriteString(mode)
				first = false
				continue
			}
			if line.Len()+2+len(mode) > wrapAt {
				lines = append(lines, line.String()+",")
				line.Reset()
				line.WriteString(indent)
				line.WriteString(mode)
			} else {
				line.WriteString(", ")
				line.WriteString(mode)
			}
		}
		if line.Len() > 0 {
			lines = append(lines, line.String())
		}
	}
	lines = append(lines, "")

	srvHeader := "nxtermd:"
	if s.caps.ServerUpgradeAvail {
		srvHeader += " (upgrade available: " + s.caps.ServerUpgradeVer + ")"
	}
	lines = append(lines, srvHeader)
	if s.status != nil {
		d := time.Duration(s.status.UptimeSeconds) * time.Second
		lines = append(lines, fmt.Sprintf("  Hostname:  %s", s.status.Hostname))
		lines = append(lines, fmt.Sprintf("  Version:   %s", s.status.Version))
		lines = append(lines, fmt.Sprintf("  PID:       %d", s.status.Pid))
		lines = append(lines, fmt.Sprintf("  Uptime:    %s", d.String()))
		lines = append(lines, fmt.Sprintf("  Listeners: %s", s.status.SocketPath))
		lines = append(lines, fmt.Sprintf("  Clients:   %d", s.status.NumClients))
		lines = append(lines, fmt.Sprintf("  Regions:   %d", s.status.NumRegions))

		for _, r := range s.status.Regions {
			lines = append(lines, "")
			kind := "pty"
			if r.Native {
				kind = "native"
			}
			lines = append(lines, fmt.Sprintf("region %s:", r.Name))
			lines = append(lines, fmt.Sprintf("  Cmd:        %s", r.Cmd))
			lines = append(lines, fmt.Sprintf("  PID:        %d", r.Pid))
			lines = append(lines, fmt.Sprintf("  Size:       %dx%d", r.Width, r.Height))
			lines = append(lines, fmt.Sprintf("  Scrollback: %d lines", r.ScrollbackLen))
			lines = append(lines, fmt.Sprintf("  Type:       %s", kind))
		}
	} else {
		lines = append(lines, "  loading...")
	}

	content := strings.Join(lines, "\n")

	overlayW := 50
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := overlayHint.Render("• q/esc: close •")
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

func (s *StatusLayer) WantsKeyboardInput() *KeyboardFilter { return allKeysFilter }
func (s *StatusLayer) Status(rs *RenderState) (string, lipgloss.Style) { return "status", lipgloss.Style{} }
