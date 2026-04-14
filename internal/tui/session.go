package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nxtermd/internal/protocol"
)

// regionLayer pairs a region ID with its TerminalLayer for rendering.
type regionLayer struct {
	regionID   string
	regionName string
	term       *TerminalLayer
}

// tab represents a single tab backed by a stack of one or more regions.
// The bottom region (index 0) is the primary terminal; upper regions
// are composited on top via lipgloss layers.
type tab struct {
	stackID  string
	regions  []regionLayer
}

// SessionLayer manages one named session's regions and terminals.
// SessionManagerLayer owns the session list and forwards messages here.
type SessionLayer struct {
	server    *Server
	registry  *Registry
	treeStore *TreeStore

	programs []protocol.ProgramInfo

	tabs      []tab
	activeTab int

	connStatus string // set by SessionManagerLayer on disconnect/reconnect
	status     string
	err        string

	endpoint    string
	sessionName string

	termWidth       int
	termHeight      int
	statusBarMargin int
}

// activeTerm returns the active tab's primary (bottom) TerminalLayer, or nil.
func (s *SessionLayer) activeTerm() *TerminalLayer {
	if len(s.tabs) == 0 || len(s.tabs[s.activeTab].regions) == 0 {
		return nil
	}
	return s.tabs[s.activeTab].regions[0].term
}

// ActiveTerm returns the active tab's primary TerminalLayer (exported for model).
func (s *SessionLayer) ActiveTerm() *TerminalLayer {
	return s.activeTerm()
}

// activeRegionID returns the active tab's primary region ID, or "".
func (s *SessionLayer) activeRegionID() string {
	if len(s.tabs) == 0 || len(s.tabs[s.activeTab].regions) == 0 {
		return ""
	}
	return s.tabs[s.activeTab].regions[0].regionID
}

// activeStackID returns the active tab's stack ID, or "".
func (s *SessionLayer) activeStackID() string {
	if len(s.tabs) == 0 {
		return ""
	}
	return s.tabs[s.activeTab].stackID
}

// findTabByStack returns the index of the tab for a stack, or -1.
func (s *SessionLayer) findTabByStack(stackID string) int {
	for i, t := range s.tabs {
		if t.stackID == stackID {
			return i
		}
	}
	return -1
}

// findRegionLayer returns the regionLayer for a given region ID, or nil.
func (s *SessionLayer) findRegionLayer(regionID string) *regionLayer {
	for i := range s.tabs {
		for j := range s.tabs[i].regions {
			if s.tabs[i].regions[j].regionID == regionID {
				return &s.tabs[i].regions[j]
			}
		}
	}
	return nil
}

// findTabIndex returns the index of the tab containing a region, or -1.
func (s *SessionLayer) findTabIndex(regionID string) int {
	for i, t := range s.tabs {
		for _, rl := range t.regions {
			if rl.regionID == regionID {
				return i
			}
		}
	}
	return -1
}

// syncFromTree reconciles the local tab list and programs with the
// server's object tree. Called on every TreeChangedMsg.
func (s *SessionLayer) syncFromTree(tree protocol.Tree) {
	sess, ok := tree.Sessions[s.sessionName]
	if !ok {
		if s.sessionName == "" {
			return // session name not yet known
		}
		// Session was removed from the tree (all regions destroyed).
		if len(s.tabs) > 0 {
			s.tabs = s.tabs[:0]
			s.status = "no regions"
		}
		return
	}

	// Derive programs from tree.
	s.programs = s.programs[:0]
	for _, p := range tree.Programs {
		s.programs = append(s.programs, protocol.ProgramInfo{Name: p.Name, Cmd: p.Cmd})
	}

	// Build set of server stack IDs.
	serverStackIDs := make(map[string]bool, len(sess.StackIDs))
	for _, sid := range sess.StackIDs {
		serverStackIDs[sid] = true
	}

	prevActiveStackID := s.activeStackID()
	hadTabs := len(s.tabs) > 0

	// Remove tabs whose stacks no longer exist.
	n := 0
	for _, t := range s.tabs {
		if serverStackIDs[t.stackID] {
			s.tabs[n] = t
			n++
		}
	}
	s.tabs = s.tabs[:n]

	// Add or update tabs for each stack in session order.
	for _, stackID := range sess.StackIDs {
		stack, ok := tree.Stacks[stackID]
		if !ok || len(stack.RegionIDs) == 0 {
			continue
		}

		tabIdx := s.findTabByStack(stackID)
		if tabIdx < 0 {
			// New stack — create tab with all its regions.
			t := tab{stackID: stackID}
			for _, rid := range stack.RegionIDs {
				name := ""
				if r, ok := tree.Regions[rid]; ok {
					name = r.Name
				}
				term := NewTerminalLayer(s.server, rid, name, s.termWidth, s.termHeight, s.statusBarMargin)
				t.regions = append(t.regions, regionLayer{regionID: rid, regionName: name, term: term})
			}
			s.tabs = append(s.tabs, t)
		} else {
			// Existing stack — sync regions within it.
			t := &s.tabs[tabIdx]
			regionSet := make(map[string]bool, len(stack.RegionIDs))
			for _, rid := range stack.RegionIDs {
				regionSet[rid] = true
			}
			// Remove regions no longer in the stack.
			rn := 0
			for _, rl := range t.regions {
				if regionSet[rl.regionID] {
					if r, ok := tree.Regions[rl.regionID]; ok {
						rl.regionName = r.Name
						rl.term.regionName = r.Name
					}
					t.regions[rn] = rl
					rn++
				}
			}
			t.regions = t.regions[:rn]
			// Add new regions.
			for _, rid := range stack.RegionIDs {
				found := false
				for _, rl := range t.regions {
					if rl.regionID == rid {
						found = true
						break
					}
				}
				if !found {
					name := ""
					if r, ok := tree.Regions[rid]; ok {
						name = r.Name
					}
					term := NewTerminalLayer(s.server, rid, name, s.termWidth, s.termHeight, s.statusBarMargin)
					t.regions = append(t.regions, regionLayer{regionID: rid, regionName: name, term: term})
				}
			}
		}
	}

	// Restore active tab.
	if prevActiveStackID != "" {
		if idx := s.findTabByStack(prevActiveStackID); idx >= 0 {
			s.activeTab = idx
		}
	}
	if s.activeTab >= len(s.tabs) {
		s.activeTab = max(len(s.tabs)-1, 0)
	}

	// Handle state transitions.
	if len(s.tabs) == 0 && hadTabs {
		s.status = "no regions"
	} else if len(s.tabs) > 0 {
		s.status = ""
		newActiveStackID := s.activeStackID()
		if newActiveStackID != prevActiveStackID || !hadTabs {
			s.Activate()
		}
	}
}

// NewSessionLayer creates a session layer with the given dependencies.
func NewSessionLayer(
	server *Server, registry *Registry,
	treeStore *TreeStore,
	endpoint, sessionName string,
	statusBarMargin int,
) *SessionLayer {
	return &SessionLayer{
		server:          server,
		registry:        registry,
		treeStore:       treeStore,
		endpoint:        endpoint,
		sessionName:     sessionName,
		statusBarMargin: statusBarMargin,
		connStatus:      "connected",
		status:          "connecting...",
	}
}

// Reconnect re-sends the SessionConnectRequest to refresh the region list.
// Called by SessionManagerLayer after a connection is restored.
func (s *SessionLayer) Reconnect() {
	height := s.termHeight - 1 - s.statusBarMargin
	if height < 1 {
		height = 1
	}
	s.server.Send(protocol.SessionConnectRequest{
		Session: s.sessionName,
		Width:   uint16(s.termWidth),
		Height:  uint16(height),
	})
}

// KillAllRegions sends KillRegionRequest for every region in this session.
func (s *SessionLayer) KillAllRegions() {
	for _, t := range s.tabs {
		for _, rl := range t.regions {
			s.server.Send(protocol.KillRegionRequest{RegionID: rl.regionID})
		}
	}
}

// Activate subscribes to all regions in the active stack. Called when
// this session becomes the active session.
func (s *SessionLayer) Activate() tea.Cmd {
	if len(s.tabs) == 0 {
		return nil
	}
	t := s.tabs[s.activeTab]
	if len(t.regions) == 0 {
		return nil
	}
	s.status = "subscribing..."
	// Activate the primary (bottom) region first; upper regions are
	// activated after to establish subscriptions for all layers.
	var cmds []tea.Cmd
	for _, rl := range t.regions {
		if cmd := rl.term.Activate(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// Deactivate unsubscribes from all regions in the active stack.
func (s *SessionLayer) Deactivate() {
	if len(s.tabs) == 0 {
		return
	}
	for _, rl := range s.tabs[s.activeTab].regions {
		rl.term.Deactivate()
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
// Global messages (disconnect, reconnect, detach, etc.) are handled by SessionManagerLayer.
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

	case TreeChangedMsg:
		s.syncFromTree(msg.Tree)
		return nil, nil, true

	case protocol.SessionConnectResponse:
		if msg.Error {
			s.err = "session connect failed: " + msg.Message
			return nil, nil, true
		}
		s.sessionName = msg.Session
		// The session name is now known; re-derive tabs from the tree.
		// Tree data may have arrived before the response (via tree_events)
		// but syncFromTree couldn't match the session with an empty name.
		if s.treeStore != nil && s.treeStore.Valid() {
			s.syncFromTree(s.treeStore.Tree())
		}
		return nil, nil, true

	case protocol.SpawnResponse:
		if msg.Error {
			if len(s.tabs) == 0 {
				s.err = "spawn failed: " + msg.Message
			}
			s.status = ""
			return nil, nil, true
		}
		// The tab is created by syncFromTree when the tree event arrives
		// (which the server broadcasts before sending this response).
		// Just switch to it if it already exists.
		if idx := s.findTabIndex(msg.RegionID); idx >= 0 {
			s.switchToTab(idx)
		}
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
		return nil, nil, true

	// Terminal messages — route to the correct region layer by RegionID
	case protocol.ScreenUpdate:
		if rl := s.findRegionLayer(msg.RegionID); rl != nil {
			_, cmd, _ := rl.term.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true
	case protocol.GetScreenResponse:
		if rl := s.findRegionLayer(msg.RegionID); rl != nil {
			_, cmd, _ := rl.term.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true
	case protocol.TerminalEvents:
		if rl := s.findRegionLayer(msg.RegionID); rl != nil {
			_, cmd, _ := rl.term.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true
	case protocol.GetScrollbackResponse:
		// Handled by ScrollbackLayer in the layer stack when active.
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

	case tea.MouseMsg:
		cmd := s.handleMouse(msg)
		return nil, cmd, true

	case tea.KeyPressMsg:
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
	case "scroll-up", "scroll-down":
		if t := s.activeTerm(); t != nil {
			return t.Update(msg)
		}
		return nil, nil, true
	case "send-prefix":
		s.sendRawToServer([]byte{s.registry.PrefixKey})
		return nil, nil, true
	case "enter-scrollback":
		if t := s.activeTerm(); t != nil && !t.ScrollbackActive() {
			sl := t.NewScrollbackLayer(0)
			return nil, func() tea.Msg { return PushLayerMsg{Layer: sl} }, true
		}
		return nil, nil, true
	case "refresh-screen":
		if t := s.activeTerm(); t != nil {
			t.SetPendingClear()
			s.server.Send(protocol.GetScreenRequest{RegionID: t.RegionID()})
		}
		return nil, nil, true
	default:
		return nil, nil, true
	}
}

// checkBindingCondition evaluates a binding condition against current state.
func (s *SessionLayer) checkBindingCondition(when string) bool {
	switch when {
	case "normal-screen":
		t := s.activeTerm()
		return t != nil && !t.IsAltScreen() && !t.ScrollbackActive()
	default:
		return true
	}
}

// View implements the Layer interface. Returns the tab bar and terminal
// content as separate layers for compositing. Model composites the
// right side of the tab bar (status + branding) as an additional layer.
func (s *SessionLayer) View(width, height int, rs *RenderState) []*lipgloss.Layer {
	if s.err != "" {
		return []*lipgloss.Layer{lipgloss.NewLayer("error: " + s.err + "\n")}
	}

	width = max(width, 80)
	height = max(height, 24)

	layers := []*lipgloss.Layer{lipgloss.NewLayer(s.renderTabBar(width))}

	termY := 1 + s.statusBarMargin
	contentHeight := max(height-termY, 1)
	activeTab := s.activeTab
	if activeTab < len(s.tabs) && len(s.tabs[activeTab].regions) > 0 {
		// Composite all region layers in the stack (bottom-to-top).
		for z, rl := range s.tabs[activeTab].regions {
			rl.term.disconnected = (s.connStatus == "reconnecting")
			termLayers := rl.term.View(width, contentHeight, rs)
			for i := range termLayers {
				termLayers[i] = termLayers[i].Y(termY).Z(z)
			}
			layers = append(layers, termLayers...)
		}
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
		layers = append(layers, lipgloss.NewLayer(sb.String()).Y(termY))
	}

	return layers
}

// renderTabBar renders the left side of the tab bar with all tabs.
// Each segment is styled individually so the active tab can be bold
// while the rest remains faint.
func (s *SessionLayer) renderTabBar(width int) string {
	var sb strings.Builder

	dot := func(bold bool) {
		if bold {
			sb.WriteString(statusActiveTab.Render("•"))
		} else {
			sb.WriteString(statusFaint.Render("•"))
		}
	}

	dot(s.activeTab == 0)
	used := 1

	for i, t := range s.tabs {
		var label string
		if i == s.activeTab {
			label = fmt.Sprintf(" <%d> ", i+1)
			sb.WriteString(statusActiveTab.Render(label))
		} else {
			name := ""
			if len(t.regions) > 0 {
				name = t.regions[0].term.Title()
			}
			label = fmt.Sprintf(" %d:%s ", i+1, truncateTitle(stripEmoji(name), 30))
			sb.WriteString(statusFaint.Render(label))
		}
		dot(i == s.activeTab || i+1 == s.activeTab)
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
// Reconnecting status is handled by SessionManagerLayer.
var (
	statusFaint      = lipgloss.NewStyle().Faint(true)
	statusBoldRed    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	statusBold       = lipgloss.NewStyle().Bold(true)
	statusActiveTab  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightCyan)
	commandModeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightCyan)
)

// stripEmoji removes runs of emoji codepoints (and any intervening
// ZWJ or variation selectors). Terminals render emoji from a color
// font and ignore SGR faint, so they stand out in dim tab labels.
func stripEmoji(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	inRun := false
	for _, r := range s {
		if isEmojiBase(r) {
			inRun = true
			continue
		}
		if inRun && (r == 0x200D || r == 0xFE0F || r == 0xFE0E) {
			continue
		}
		inRun = false
		sb.WriteRune(r)
	}
	return sb.String()
}

func isEmojiBase(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	}
	return false
}

// truncateTitle caps s at max runes, replacing the tail with an
// ellipsis (…) when it would otherwise be longer. The returned string
// is at most max runes wide.
func truncateTitle(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

func (s *SessionLayer) WantsKeyboardInput() bool { return false }

func (s *SessionLayer) Status(rs *RenderState) (string, lipgloss.Style) {
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
