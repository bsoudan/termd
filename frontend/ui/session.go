package ui

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// tab represents a single terminal tab backed by a server region.
type tab struct {
	regionID   string
	regionName string
	term       *TerminalChild // nil until subscribe succeeds
}

// SessionLayer is the root layer — it owns server communication, region
// lifecycle, terminal children, and connection state.
type SessionLayer struct {
	server    *Server
	pipeW     io.Writer
	requestFn RequestFunc

	cmd     string
	cmdArgs []string

	tabs      []tab
	activeTab int

	connStatus string
	retryAt    time.Time
	status     string
	err        string

	logRing       *termlog.LogRingBuffer
	localHostname string
	endpoint      string
	version       string
	changelog     string
	sessionName   string

	// Pre-terminal dimensions (stored until terminal is created).
	termWidth  int
	termHeight int
}

// activeTerm returns the active tab's TerminalChild, or nil.
func (s *SessionLayer) activeTerm() *TerminalChild {
	if len(s.tabs) == 0 {
		return nil
	}
	return s.tabs[s.activeTab].term
}

// ActiveTerm returns the active tab's TerminalChild (exported for model).
func (s *SessionLayer) ActiveTerm() *TerminalChild {
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

	// Subscribe to the active region.
	if id := s.activeRegionID(); id != "" {
		s.status = "subscribing..."
		s.server.Send(protocol.SubscribeRequest{RegionID: id})
	}
}

// NewSessionLayer creates a session layer with the given dependencies.
func NewSessionLayer(
	server *Server, pipeW io.Writer, requestFn RequestFunc,
	cmd string, args []string,
	logRing *termlog.LogRingBuffer,
	endpoint, version, changelog, hostname, sessionName string,
) *SessionLayer {
	return &SessionLayer{
		server:        server,
		pipeW:         pipeW,
		requestFn:     requestFn,
		cmd:           cmd,
		cmdArgs:       args,
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

// Init sends the initial SessionConnectRequest and returns a cmd to show the hint.
func (s *SessionLayer) Init() tea.Cmd {
	s.server.Send(protocol.SessionConnectRequest{Session: s.sessionName})
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} })
}

func (s *SessionLayer) contentHeight() int {
	h := s.termHeight - 1 // tab bar only
	if h < 1 {
		h = 1
	}
	return h
}

func (s *SessionLayer) quit() (tea.Msg, tea.Cmd) {
	id := s.activeRegionID()
	if id != "" {
		s.server.Send(protocol.UnsubscribeRequest{RegionID: id})
	}
	s.server.Send(protocol.Disconnect{})
	return nil, tea.Quit
}

func (s *SessionLayer) detach() (tea.Msg, tea.Cmd) {
	id := s.activeRegionID()
	if id != "" {
		s.server.Send(protocol.UnsubscribeRequest{RegionID: id})
	}
	s.server.Send(protocol.Disconnect{})
	return DetachMsg{}, tea.Quit
}

// ensureTerminal creates the terminal for the active tab if it doesn't exist yet.
func (s *SessionLayer) ensureTerminal() {
	if len(s.tabs) == 0 {
		return
	}
	t := &s.tabs[s.activeTab]
	if t.term == nil {
		t.term = NewTerminalChild(s.server, t.regionID, t.regionName, s.termWidth, s.termHeight)
	}
}

// ensureTerminalForTab creates the terminal for a specific tab if it doesn't exist yet.
func (s *SessionLayer) ensureTerminalForTab(idx int) {
	if idx < 0 || idx >= len(s.tabs) {
		return
	}
	t := &s.tabs[idx]
	if t.term == nil {
		t.term = NewTerminalChild(s.server, t.regionID, t.regionName, s.termWidth, s.termHeight)
	}
}

// switchToTab switches from the current active tab to the given index.
// It unsubscribes from the old region and subscribes to the new one.
func (s *SessionLayer) switchToTab(idx int) {
	if idx < 0 || idx >= len(s.tabs) || idx == s.activeTab {
		return
	}
	oldID := s.activeRegionID()
	if oldID != "" {
		s.server.Send(protocol.UnsubscribeRequest{RegionID: oldID})
	}
	s.activeTab = idx
	newID := s.activeRegionID()
	if newID != "" {
		s.server.Send(protocol.SubscribeRequest{RegionID: newID})
		if s.termWidth > 0 && s.termHeight > 2 {
			s.server.Send(protocol.ResizeRequest{
				RegionID: newID,
				Width:    uint16(s.termWidth),
				Height:   uint16(s.contentHeight()),
			})
		}
	}
}

// Update implements the Layer interface.
func (s *SessionLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RawInputMsg:
		resp, cmd := s.handleRawInput([]byte(msg))
		return resp, cmd, true

	case DetachRequestMsg:
		resp, cmd := s.detach()
		return resp, cmd, true

	case SendLiteralPrefixMsg:
		s.sendRawToServer([]byte{prefixKey})
		return nil, nil, true

	case OpenOverlayMsg:
		cmd := s.openOverlay(msg.Name)
		return nil, cmd, true

	case EnterScrollbackMsg:
		if t := s.activeTerm(); t != nil {
			t.EnterScrollback(0)
		}
		return nil, nil, true

	case RefreshScreenMsg:
		if t := s.activeTerm(); t != nil {
			t.SetPendingClear()
			s.server.Send(protocol.GetScreenRequest{
				RegionID: t.RegionID(),
			})
		}
		return nil, nil, true

	case SpawnRegionMsg:
		s.status = "spawning..."
		s.server.Send(protocol.SpawnRequest{
			Session: s.sessionName,
			Cmd:     s.cmd,
			Args:    s.cmdArgs,
		})
		return nil, nil, true

	case SwitchTabMsg:
		s.switchToTab(msg.Index)
		return nil, nil, true

	case CloseTabMsg:
		id := s.activeRegionID()
		if id != "" {
			s.server.Send(protocol.KillRegionRequest{RegionID: id})
		}
		return nil, nil, true

	case protocol.Identify:
		if msg.Hostname != s.localHostname {
			s.endpoint = s.localHostname + " -> " + s.endpoint
		}
		return nil, nil, true

	case tea.WindowSizeMsg:
		s.termWidth = msg.Width
		s.termHeight = msg.Height
		if t := s.activeTerm(); t != nil {
			cmd := t.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true

	case protocol.SessionConnectResponse:
		if msg.Error {
			s.err = "session connect failed: " + msg.Message
			resp, cmd := s.quit()
			return resp, cmd, true
		}
		s.sessionName = msg.Session
		s.syncTabs(msg.Regions)
		return nil, nil, true

	case protocol.ListRegionsResponse:
		if msg.Error {
			s.err = "list regions failed: " + msg.Message
			resp, cmd := s.quit()
			return resp, cmd, true
		}
		s.syncTabs(msg.Regions)
		return nil, nil, true

	case protocol.SpawnResponse:
		if msg.Error {
			if len(s.tabs) == 0 {
				s.err = "spawn failed: " + msg.Message
				resp, cmd := s.quit()
				return resp, cmd, true
			}
			// Non-fatal: we have other tabs
			s.status = ""
			return nil, nil, true
		}
		// Unsubscribe from old active region before switching
		oldID := s.activeRegionID()
		if oldID != "" {
			s.server.Send(protocol.UnsubscribeRequest{RegionID: oldID})
		}
		s.tabs = append(s.tabs, tab{regionID: msg.RegionID, regionName: msg.Name})
		s.activeTab = len(s.tabs) - 1
		s.status = "subscribing..."
		s.server.Send(protocol.SubscribeRequest{RegionID: msg.RegionID})
		return nil, nil, true

	case protocol.SubscribeResponse:
		if msg.Error {
			if len(s.tabs) == 0 {
				s.err = "subscribe failed: " + msg.Message
				resp, cmd := s.quit()
				return resp, cmd, true
			}
			s.status = ""
			return nil, nil, true
		}
		s.status = ""
		s.ensureTerminal()
		if s.termWidth > 0 && s.termHeight > 2 {
			s.server.Send(protocol.ResizeRequest{
				RegionID: s.activeRegionID(),
				Width:    uint16(s.termWidth),
				Height:   uint16(s.contentHeight()),
			})
		}
		return nil, nil, true

	// Terminal messages — route to the correct tab by RegionID
	case protocol.ScreenUpdate:
		idx := s.findTabIndex(msg.RegionID)
		if idx < 0 {
			return nil, nil, true
		}
		s.ensureTerminalForTab(idx)
		cmd := s.tabs[idx].term.Update(msg)
		return nil, cmd, true
	case protocol.GetScreenResponse:
		idx := s.findTabIndex(msg.RegionID)
		if idx >= 0 && s.tabs[idx].term != nil {
			cmd := s.tabs[idx].term.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true
	case protocol.TerminalEvents:
		idx := s.findTabIndex(msg.RegionID)
		if idx < 0 {
			return nil, nil, true
		}
		s.ensureTerminalForTab(idx)
		cmd := s.tabs[idx].term.Update(msg)
		return nil, cmd, true
	case protocol.GetScrollbackResponse:
		idx := s.findTabIndex(msg.RegionID)
		if idx >= 0 && s.tabs[idx].term != nil {
			s.tabs[idx].term.Update(msg)
		}
		return nil, nil, true
	case protocol.ResizeResponse:
		return nil, nil, true

	// Capability messages — delegate to active terminal if it exists
	case tea.KeyboardEnhancementsMsg:
		if t := s.activeTerm(); t != nil {
			t.Update(msg)
		}
		return nil, nil, true
	case tea.BackgroundColorMsg:
		if t := s.activeTerm(); t != nil {
			t.Update(msg)
		}
		return nil, nil, true
	case tea.EnvMsg:
		if t := s.activeTerm(); t != nil {
			t.Update(msg)
		}
		return nil, nil, true

	case protocol.RegionCreated:
		// Update name for the matching tab if it exists
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
			s.err = "region destroyed"
			resp, cmd := s.quit()
			return resp, cmd, true
		}
		if s.activeTab >= len(s.tabs) {
			s.activeTab = len(s.tabs) - 1
		}
		// Subscribe to the now-active tab
		newID := s.activeRegionID()
		if newID != "" {
			s.server.Send(protocol.SubscribeRequest{RegionID: newID})
		}
		return nil, nil, true

	case DisconnectedMsg:
		s.connStatus = "reconnecting"
		s.retryAt = msg.RetryAt
		return nil, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} }), true

	case ReconnectedMsg:
		s.connStatus = "connected"
		s.retryAt = time.Time{}
		// Re-connect to session to restore all tabs.
		s.server.Send(protocol.SessionConnectRequest{Session: s.sessionName})
		return nil, nil, true

	case ServerErrorMsg:
		s.err = msg.Context + ": " + msg.Message
		resp, cmd := s.quit()
		return resp, cmd, true

	case LogEntryMsg:
		return nil, nil, true

	case showHintMsg:
		pushCmd := func() tea.Msg { return PushLayerMsg{Layer: &HintLayer{}} }
		hideCmd := tea.Tick(3*time.Second, func(time.Time) tea.Msg { return hideHintMsg{} })
		return nil, tea.Batch(pushCmd, hideCmd), true

	case reconnectTickMsg:
		if s.connStatus == "reconnecting" {
			return nil, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} }), true
		}
		return nil, nil, true

	case tea.MouseMsg:
		cmd := s.handleMouse(msg)
		return nil, cmd, true

	case tea.KeyPressMsg:
		if t := s.activeTerm(); t != nil && t.ScrollbackActive() {
			t.HandleScrollbackKey(msg)
			return nil, nil, true
		}
		return nil, nil, true

	default:
		return nil, nil, true
	}
}

func (s *SessionLayer) openOverlay(name string) tea.Cmd {
	var layer Layer
	switch name {
	case "logviewer":
		layer = NewScrollableLayer("logviewer", s.logRing.String(), true, s.logRing, s.termWidth, s.termHeight)
	case "help":
		layer = NewHelpLayer(helpItems)
	case "status":
		sl := NewStatusLayer(s.buildStatusCaps())
		s.requestFn(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(protocol.StatusResponse); ok {
				sl.SetStatus(&resp)
			}
		})
		layer = sl
	case "release notes":
		layer = NewScrollableLayer("release notes", strings.TrimRight(s.changelog, "\n"), false, nil, s.termWidth, s.termHeight)
	}
	if layer == nil {
		return nil
	}
	return func() tea.Msg { return PushLayerMsg{Layer: layer} }
}

func (s *SessionLayer) buildStatusCaps() StatusCaps {
	caps := StatusCaps{
		Hostname:    s.localHostname,
		Endpoint:    s.endpoint,
		SessionName: s.sessionName,
		Version:     s.version,
		ConnStatus:  s.connStatus,
	}
	if t := s.activeTerm(); t != nil {
		caps.KeyboardFlags = t.KeyboardFlags()
		caps.BgDark = t.BgDark()
		caps.TermEnv = t.TermEnv()
		caps.MouseModes = t.MouseModes()
	}
	return caps
}

// View implements the Layer interface. Renders the tab bar (left side
// only — terminal tabs) plus terminal content. Model composites the
// right side of the tab bar (status + branding) as a separate layer.
func (s *SessionLayer) View(width, height int, active bool) *lipgloss.Layer {
	if s.err != "" {
		return lipgloss.NewLayer("error: " + s.err + "\n")
	}

	width = max(width, 80)
	height = max(height, 24)

	var sb strings.Builder
	sb.WriteString(s.renderTabBar(width))
	sb.WriteByte('\n')

	contentHeight := max(height-1, 1)
	term := s.activeTerm()
	scrollbackActive := term != nil && term.ScrollbackActive()
	showCursor := active && !scrollbackActive
	disconnected := s.connStatus == "reconnecting"

	if term != nil {
		term.View(&sb, width, contentHeight, showCursor, disconnected)
	} else {
		for i := range contentHeight {
			for range width {
				sb.WriteByte(' ')
			}
			if i < contentHeight-1 {
				sb.WriteByte('\n')
			}
		}
	}

	return lipgloss.NewLayer(sb.String())
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

// Status implements the Layer interface. Returns the session's own
// status — scrollback mode, reconnecting, or endpoint. Layers above
// session override this via the layer stack Status traversal.
var (
	statusFaint   = lipgloss.NewStyle().Faint(true)
	statusBoldRed = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	statusBold    = lipgloss.NewStyle().Bold(true)
)

func (s *SessionLayer) Status() (string, lipgloss.Style) {
	if t := s.activeTerm(); t != nil && t.ScrollbackActive() {
		text, _, _ := t.Status()
		return text, statusBold
	}
	if s.connStatus == "reconnecting" {
		secs := int(time.Until(s.retryAt).Seconds()) + 1
		return fmt.Sprintf("reconnecting to %s in %ds...", s.endpoint, secs), statusBoldRed
	}
	name := s.endpoint
	if s.sessionName != "" {
		name = s.sessionName + "@" + s.endpoint
	}
	if len(name) > 30 {
		name = name[len(name)-30:]
	}
	return name, statusFaint
}
