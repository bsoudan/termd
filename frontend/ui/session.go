package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	termlog "nxtermd/frontend/log"
	"nxtermd/frontend/protocol"
)

// tab represents a single terminal tab backed by a server region.
type tab struct {
	regionID   string
	regionName string
	term       *TerminalLayer // nil until subscribe succeeds
}

// SessionLayer manages one named session's regions and terminals.
// MainLayer owns the session list and forwards messages here.
type SessionLayer struct {
	server    *Server
	requestFn RequestFunc
	registry  *Registry

	programs []protocol.ProgramInfo

	tabs      []tab
	activeTab int

	connStatus string // set by MainLayer on disconnect/reconnect
	status     string
	err        string

	logRing       *termlog.LogRingBuffer
	localHostname string
	endpoint      string
	version       string
	changelog     string
	sessionName   string

	termWidth  int
	termHeight int
}

// activeTerm returns the active tab's TerminalLayer, or nil.
func (s *SessionLayer) activeTerm() *TerminalLayer {
	if len(s.tabs) == 0 {
		return nil
	}
	return s.tabs[s.activeTab].term
}

// ActiveTerm returns the active tab's TerminalLayer (exported for model).
func (s *SessionLayer) ActiveTerm() *TerminalLayer {
	return s.activeTerm()
}

// activeRegionID returns the active tab's region ID, or "".
func (s *SessionLayer) activeRegionID() string {
	if len(s.tabs) == 0 {
		return ""
	}
	return s.tabs[s.activeTab].regionID
}

// findTabIndex returns the index of the tab with the given region ID, or -1.
func (s *SessionLayer) findTabIndex(regionID string) int {
	for i, t := range s.tabs {
		if t.regionID == regionID {
			return i
		}
	}
	return -1
}

// syncTabs reconciles the local tab list with the server's region list.
// New regions get tabs appended; tabs whose regions no longer exist are
// removed. The active tab is preserved if its region still exists,
// otherwise it falls back to the first tab. After syncing, the active
// tab's region is subscribed.
func (s *SessionLayer) syncTabs(regions []protocol.RegionInfo) {
	// Build a set of server region IDs for quick lookup.
	serverIDs := make(map[string]bool, len(regions))
	for _, r := range regions {
		serverIDs[r.RegionID] = true
	}

	// Remove tabs whose regions no longer exist on the server.
	prevActiveID := s.activeRegionID()
	n := 0
	for _, t := range s.tabs {
		if serverIDs[t.regionID] {
			s.tabs[n] = t
			n++
		}
	}
	s.tabs = s.tabs[:n]

	// Add tabs for regions not already tracked.
	for _, r := range regions {
		if s.findTabIndex(r.RegionID) < 0 {
			s.tabs = append(s.tabs, tab{regionID: r.RegionID, regionName: r.Name})
		}
	}

	// Restore active tab to the previously active region if still present.
	if prevActiveID != "" {
		if idx := s.findTabIndex(prevActiveID); idx >= 0 {
			s.activeTab = idx
		}
	}
	if s.activeTab >= len(s.tabs) {
		s.activeTab = max(len(s.tabs)-1, 0)
	}

}

// NewSessionLayer creates a session layer with the given dependencies.
func NewSessionLayer(
	server *Server, requestFn RequestFunc, registry *Registry,
	logRing *termlog.LogRingBuffer,
	endpoint, version, changelog, hostname, sessionName string,
) *SessionLayer {
	return &SessionLayer{
		server:        server,
		requestFn:     requestFn,
		registry:      registry,
		endpoint:      endpoint,
		version:       version,
		changelog:     changelog,
		localHostname: hostname,
		logRing:       logRing,
		sessionName:   sessionName,
		connStatus:    "connected",
		status:        "connecting...",
	}
}

// Reconnect re-sends the SessionConnectRequest to refresh the region list.
// Called by MainLayer after a connection is restored.
func (s *SessionLayer) Reconnect() {
	s.server.Send(protocol.SessionConnectRequest{Session: s.sessionName})
}

// KillAllRegions sends KillRegionRequest for every region in this session.
func (s *SessionLayer) KillAllRegions() {
	for _, t := range s.tabs {
		s.server.Send(protocol.KillRegionRequest{RegionID: t.regionID})
	}
}

// Activate subscribes to the active region. Called when this session
// becomes the active session (e.g., MainLayer switches to it).
func (s *SessionLayer) Activate() tea.Cmd {
	s.ensureTerminal()
	if t := s.activeTerm(); t != nil {
		s.status = "subscribing..."
		return t.Activate()
	}
	return nil
}

// Deactivate unsubscribes from the active region. Called when this
// session is no longer the active session.
func (s *SessionLayer) Deactivate() {
	if t := s.activeTerm(); t != nil {
		t.Deactivate()
	}
}

// ensureTerminal creates the terminal for the active tab if it doesn't exist yet.
func (s *SessionLayer) ensureTerminal() {
	if len(s.tabs) == 0 {
		return
	}
	t := &s.tabs[s.activeTab]
	if t.term == nil {
		t.term = NewTerminalLayer(s.server, t.regionID, t.regionName, s.termWidth, s.termHeight)
	}
}

// ensureTerminalForTab creates the terminal for a specific tab if it doesn't exist yet.
func (s *SessionLayer) ensureTerminalForTab(idx int) {
	if idx < 0 || idx >= len(s.tabs) {
		return
	}
	t := &s.tabs[idx]
	if t.term == nil {
		t.term = NewTerminalLayer(s.server, t.regionID, t.regionName, s.termWidth, s.termHeight)
	}
}

// switchToTab switches from the current active tab to the given index.
func (s *SessionLayer) switchToTab(idx int) {
	if idx < 0 || idx >= len(s.tabs) || idx == s.activeTab {
		return
	}
	s.Deactivate()
	s.activeTab = idx
	s.Activate()
}

// nextTab switches to the next tab, wrapping around.
func (s *SessionLayer) nextTab() {
	if len(s.tabs) <= 1 {
		return
	}
	s.switchToTab((s.activeTab + 1) % len(s.tabs))
}

// prevTab switches to the previous tab, wrapping around.
func (s *SessionLayer) prevTab() {
	if len(s.tabs) <= 1 {
		return
	}
	s.switchToTab((s.activeTab - 1 + len(s.tabs)) % len(s.tabs))
}

// Update implements the Layer interface.
// Update implements the Layer interface. Handles session-specific messages.
// Global messages (disconnect, reconnect, detach, etc.) are handled by MainLayer.
func (s *SessionLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RawInputMsg:
		resp, cmd := s.handleRawInput([]byte(msg))
		return resp, cmd, true

	case SessionCmd:
		return s.handleCmd(msg)

	case tea.WindowSizeMsg:
		s.termWidth = msg.Width
		s.termHeight = msg.Height
		if t := s.activeTerm(); t != nil {
			_, cmd, _ := t.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true

	case protocol.SessionConnectResponse:
		if msg.Error {
			s.err = "session connect failed: " + msg.Message
			return nil, nil, true
		}
		s.sessionName = msg.Session
		s.programs = msg.Programs
		s.syncTabs(msg.Regions)
		s.Activate()
		return nil, nil, true

	case protocol.ListRegionsResponse:
		if msg.Error {
			s.err = "list regions failed: " + msg.Message
			return nil, nil, true
		}
		s.syncTabs(msg.Regions)
		s.Activate()
		return nil, nil, true

	case protocol.SpawnResponse:
		if msg.Error {
			if len(s.tabs) == 0 {
				s.err = "spawn failed: " + msg.Message
			}
			s.status = ""
			return nil, nil, true
		}
		s.Deactivate()
		s.tabs = append(s.tabs, tab{regionID: msg.RegionID, regionName: msg.Name})
		s.activeTab = len(s.tabs) - 1
		s.Activate()
		return nil, nil, true

	case protocol.SubscribeResponse:
		if msg.Error {
			if len(s.tabs) == 0 {
				s.err = "subscribe failed: " + msg.Message
			}
			s.status = ""
			return nil, nil, true
		}
		s.status = ""
		s.ensureTerminal()
		return nil, nil, true

	// Terminal messages — route to the correct tab by RegionID
	case protocol.ScreenUpdate:
		idx := s.findTabIndex(msg.RegionID)
		if idx < 0 {
			return nil, nil, true
		}
		s.ensureTerminalForTab(idx)
		_, cmd, _ := s.tabs[idx].term.Update(msg)
		return nil, cmd, true
	case protocol.GetScreenResponse:
		idx := s.findTabIndex(msg.RegionID)
		if idx >= 0 && s.tabs[idx].term != nil {
			_, cmd, _ := s.tabs[idx].term.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true
	case protocol.TerminalEvents:
		idx := s.findTabIndex(msg.RegionID)
		if idx < 0 {
			return nil, nil, true
		}
		s.ensureTerminalForTab(idx)
		_, cmd, _ := s.tabs[idx].term.Update(msg)
		return nil, cmd, true
	case protocol.GetScrollbackResponse:
		idx := s.findTabIndex(msg.RegionID)
		if idx >= 0 && s.tabs[idx].term != nil {
			_, _, _ = s.tabs[idx].term.Update(msg)
		}
		return nil, nil, true
	case protocol.ResizeResponse:
		return nil, nil, true

	// Capability messages — delegate to active terminal if it exists
	case tea.KeyboardEnhancementsMsg:
		if t := s.activeTerm(); t != nil {
			_, _, _ = t.Update(msg)
		}
		return nil, nil, true
	case tea.BackgroundColorMsg:
		if t := s.activeTerm(); t != nil {
			_, _, _ = t.Update(msg)
		}
		return nil, nil, true
	case tea.EnvMsg:
		if t := s.activeTerm(); t != nil {
			_, _, _ = t.Update(msg)
		}
		return nil, nil, true

	case protocol.RegionCreated:
		idx := s.findTabIndex(msg.RegionID)
		if idx >= 0 {
			s.tabs[idx].regionName = msg.Name
			if s.tabs[idx].term != nil {
				s.tabs[idx].term.regionName = msg.Name
			}
		}
		return nil, nil, true

	case protocol.RegionDestroyed:
		idx := s.findTabIndex(msg.RegionID)
		if idx < 0 {
			return nil, nil, true
		}
		s.tabs = slices.Delete(s.tabs, idx, idx+1)
		if len(s.tabs) == 0 {
			// No regions left — MainLayer will handle this via View showing error
			s.status = "no regions"
			return nil, nil, true
		}
		if s.activeTab >= len(s.tabs) {
			s.activeTab = len(s.tabs) - 1
		}
		s.Activate()
		return nil, nil, true

	case tea.MouseMsg:
		cmd := s.handleMouse(msg)
		return nil, cmd, true

	case tea.KeyPressMsg:
		// When scrollback is active, keys are handled by ScrollbackLayer
		// via TerminalLayer.Update. Forward to the active terminal.
		if t := s.activeTerm(); t != nil && t.ScrollbackActive() {
			_, cmd, _ := t.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true

	default:
		return nil, nil, true
	}
}

func (s *SessionLayer) handleCmd(msg SessionCmd) (tea.Msg, tea.Cmd, bool) {
	switch msg.Name {
	case "open-tab":
		if msg.Args != "" {
			s.status = "spawning..."
			s.server.Send(protocol.SpawnRequest{
				Session: s.sessionName,
				Program: msg.Args,
			})
		} else if len(s.programs) == 1 {
			s.status = "spawning..."
			s.server.Send(protocol.SpawnRequest{
				Session: s.sessionName,
				Program: s.programs[0].Name,
			})
		} else if len(s.programs) > 1 {
			picker := NewProgramPickerLayer(s.programs)
			return nil, func() tea.Msg { return PushLayerMsg{Layer: picker} }, true
		}
		return nil, nil, true
	case "close-tab":
		id := s.activeRegionID()
		if id != "" {
			s.server.Send(protocol.KillRegionRequest{RegionID: id})
		}
		return nil, nil, true
	case "next-tab":
		s.nextTab()
		return nil, nil, true
	case "prev-tab":
		s.prevTab()
		return nil, nil, true
	case "switch-tab":
		if msg.Args != "" {
			idx, err := strconv.Atoi(msg.Args)
			if err == nil && idx >= 0 {
				s.switchToTab(idx - 1)
			}
		}
		return nil, nil, true
	default:
		return nil, nil, true
	}
}

// View implements the Layer interface. Returns the tab bar and terminal
// content as separate layers for compositing. Model composites the
// right side of the tab bar (status + branding) as an additional layer.
func (s *SessionLayer) View(width, height int, active bool) []*lipgloss.Layer {
	if s.err != "" {
		return []*lipgloss.Layer{lipgloss.NewLayer("error: " + s.err + "\n")}
	}

	width = max(width, 80)
	height = max(height, 24)

	layers := []*lipgloss.Layer{lipgloss.NewLayer(s.renderTabBar(width))}

	contentHeight := max(height-1, 1)
	if term := s.activeTerm(); term != nil {
		term.disconnected = (s.connStatus == "reconnecting")
		termLayers := term.View(width, contentHeight, active)
		for i := range termLayers {
			termLayers[i] = termLayers[i].Y(1)
		}
		layers = append(layers, termLayers...)
	} else {
		var sb strings.Builder
		for i := range contentHeight {
			for range width {
				sb.WriteByte(' ')
			}
			if i < contentHeight-1 {
				sb.WriteByte('\n')
			}
		}
		layers = append(layers, lipgloss.NewLayer(sb.String()).Y(1))
	}

	return layers
}

// renderTabBar renders the left side of the tab bar with all tabs.
// Each segment is styled individually so the active tab can be bold
// while the rest remains faint.
func (s *SessionLayer) renderTabBar(width int) string {
	var sb strings.Builder

	sb.WriteString(statusFaint.Render("•"))
	used := 1

	for i, t := range s.tabs {
		label := fmt.Sprintf(" %d:%s ", i+1, t.regionName)
		if i == s.activeTab {
			sb.WriteString(statusBold.Render(label))
		} else {
			sb.WriteString(statusFaint.Render(label))
		}
		sb.WriteString(statusFaint.Render("•"))
		used += len([]rune(label)) + 1
	}

	if len(s.tabs) == 0 && s.status != "" {
		sb.WriteString(statusFaint.Render(" " + s.status + " •"))
		used += len([]rune(s.status)) + 3
	}

	fillCount := max(width-used-1, 1)
	var fill strings.Builder
	for range fillCount {
		fill.WriteString("·")
	}
	fill.WriteString("•")
	sb.WriteString(statusFaint.Render(fill.String()))

	return sb.String()
}

// Status implements the Layer interface. Returns scrollback mode or session name.
// Reconnecting status is handled by MainLayer.
var (
	statusFaint   = lipgloss.NewStyle().Faint(true)
	statusBoldRed = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	statusBold    = lipgloss.NewStyle().Bold(true)
)

func (s *SessionLayer) WantsKeyboardInput() *KeyboardFilter { return nil }

func (s *SessionLayer) Status() (string, lipgloss.Style) {
	if t := s.activeTerm(); t != nil && t.ScrollbackActive() {
		return t.Status()
	}
	name := s.endpoint
	if s.sessionName != "" {
		name = s.sessionName + "@" + s.endpoint
	}
	if len(name) > 30 {
		if s.sessionName != "" {
			// Trim only the endpoint, keeping "session@...path"
			prefix := s.sessionName + "@..."
			remain := 30 - len(prefix)
			if remain > 0 {
				name = prefix + s.endpoint[len(s.endpoint)-remain:]
			} else {
				name = name[len(name)-30:]
			}
		} else {
			name = "..." + name[len(name)-27:]
		}
	}
	return name, statusFaint
}
