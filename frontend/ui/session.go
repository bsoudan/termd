package ui

import (
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// SessionLayer is the root layer — it owns server communication, region
// lifecycle, terminal children, and connection state.
type SessionLayer struct {
	server    *Server
	pipeW     io.Writer
	requestFn RequestFunc

	cmd     string
	cmdArgs []string

	term      *TerminalChild // nil until subscribe succeeds
	focusMode bool          // set by Model when overlay/command layer is active

	regionID   string
	regionName string
	connStatus string
	retryAt    time.Time
	status     string
	err        string

	logRing       *termlog.LogRingBuffer
	localHostname string
	endpoint      string
	version       string
	changelog     string

	// Pre-terminal dimensions (stored until terminal is created).
	termWidth  int
	termHeight int
}

// NewSessionLayer creates a session layer with the given dependencies.
func NewSessionLayer(
	server *Server, pipeW io.Writer, requestFn RequestFunc,
	cmd string, args []string,
	logRing *termlog.LogRingBuffer,
	endpoint, version, changelog, hostname string,
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
		connStatus:    "connected",
		status:        "connecting...",
	}
}

// Init sends the initial ListRegionsRequest and returns a cmd to show the hint.
func (s *SessionLayer) Init() tea.Cmd {
	s.server.Send(protocol.ListRegionsRequest{})
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
	if s.regionID != "" {
		s.server.Send(protocol.UnsubscribeRequest{RegionID: s.regionID})
	}
	s.server.Send(protocol.Disconnect{})
	return nil, tea.Quit
}

func (s *SessionLayer) detach() (tea.Msg, tea.Cmd) {
	if s.regionID != "" {
		s.server.Send(protocol.UnsubscribeRequest{RegionID: s.regionID})
	}
	s.server.Send(protocol.Disconnect{})
	return DetachMsg{}, tea.Quit
}

// ensureTerminal creates the terminal if it doesn't exist yet.
func (s *SessionLayer) ensureTerminal() {
	if s.term == nil {
		s.term = NewTerminalChild(s.server, s.regionID, s.regionName, s.termWidth, s.termHeight)
	}
}

// Update implements the Layer interface.
func (s *SessionLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RawInputMsg:
		resp, cmd := s.handleRawInput([]byte(msg))
		return resp, cmd, true

	case protocol.Identify:
		if msg.Hostname != s.localHostname {
			s.endpoint = s.localHostname + " -> " + s.endpoint
		}
		return nil, nil, true

	case tea.WindowSizeMsg:
		s.termWidth = msg.Width
		s.termHeight = msg.Height
		if s.term != nil {
			cmd := s.term.Update(msg)
			return nil, cmd, true
		}
		return nil, nil, true

	case protocol.ListRegionsResponse:
		if msg.Error {
			s.err = "list regions failed: " + msg.Message
			resp, cmd := s.quit()
			return resp, cmd, true
		}
		if len(msg.Regions) > 0 {
			s.regionID = msg.Regions[0].RegionID
			s.regionName = msg.Regions[0].Name
			s.status = "subscribing..."
			s.server.Send(protocol.SubscribeRequest{
				RegionID: s.regionID,
			})
			return nil, nil, true
		}
		s.status = "spawning..."
		s.server.Send(protocol.SpawnRequest{
			Cmd:  s.cmd,
			Args: s.cmdArgs,
		})
		return nil, nil, true

	case protocol.SpawnResponse:
		if msg.Error {
			s.err = "spawn failed: " + msg.Message
			resp, cmd := s.quit()
			return resp, cmd, true
		}
		s.regionID = msg.RegionID
		s.regionName = msg.Name
		s.status = "subscribing..."
		s.server.Send(protocol.SubscribeRequest{
			RegionID: s.regionID,
		})
		return nil, nil, true

	case protocol.SubscribeResponse:
		if msg.Error {
			s.err = "subscribe failed: " + msg.Message
			resp, cmd := s.quit()
			return resp, cmd, true
		}
		s.status = ""
		s.ensureTerminal()
		if s.termWidth > 0 && s.termHeight > 2 {
			s.server.Send(protocol.ResizeRequest{
				RegionID: s.regionID,
				Width:    uint16(s.termWidth),
				Height:   uint16(s.contentHeight()),
			})
		}
		return nil, nil, true

	// Terminal messages — delegate to TerminalChild
	case protocol.ScreenUpdate:
		s.ensureTerminal()
		cmd := s.term.Update(msg)
		return nil, cmd, true
	case protocol.GetScreenResponse:
		s.ensureTerminal()
		cmd := s.term.Update(msg)
		return nil, cmd, true
	case protocol.TerminalEvents:
		s.ensureTerminal()
		cmd := s.term.Update(msg)
		return nil, cmd, true
	case protocol.GetScrollbackResponse:
		if s.term != nil {
			s.term.Update(msg)
		}
		return nil, nil, true
	case protocol.ResizeResponse:
		return nil, nil, true

	// Capability messages — delegate to terminal if it exists
	case tea.KeyboardEnhancementsMsg:
		if s.term != nil {
			s.term.Update(msg)
		}
		return nil, nil, true
	case tea.BackgroundColorMsg:
		if s.term != nil {
			s.term.Update(msg)
		}
		return nil, nil, true
	case tea.EnvMsg:
		if s.term != nil {
			s.term.Update(msg)
		}
		return nil, nil, true

	case protocol.RegionCreated:
		if s.regionName == "" {
			s.regionName = msg.Name
		}
		if s.term != nil {
			s.term.regionName = msg.Name
		}
		return nil, nil, true

	case protocol.RegionDestroyed:
		s.err = "region destroyed"
		resp, cmd := s.quit()
		return resp, cmd, true

	case DisconnectedMsg:
		s.connStatus = "reconnecting"
		s.retryAt = msg.RetryAt
		return nil, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} }), true

	case ReconnectedMsg:
		s.connStatus = "connected"
		s.retryAt = time.Time{}
		if s.regionID != "" {
			s.server.Send(protocol.SubscribeRequest{
				RegionID: s.regionID,
			})
		}
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
		if s.term != nil && s.term.ScrollbackActive() {
			s.term.HandleScrollbackKey(msg)
			return nil, nil, true
		}
		return nil, nil, true

	default:
		return nil, nil, true
	}
}

func (s *SessionLayer) buildStatusCaps() StatusCaps {
	caps := StatusCaps{
		Hostname:   s.localHostname,
		Endpoint:   s.endpoint,
		Version:    s.version,
		ConnStatus: s.connStatus,
	}
	if s.term != nil {
		caps.KeyboardFlags = s.term.KeyboardFlags()
		caps.BgDark = s.term.BgDark()
		caps.TermEnv = s.term.TermEnv()
		caps.MouseModes = s.term.MouseModes()
	}
	return caps
}

// View implements the Layer interface.
func (s *SessionLayer) View(width, height int) string {
	return renderView(s, "", false, false, false)
}

// ViewWithStatus renders the session with a status override from the layer stack.
func (s *SessionLayer) ViewWithStatus(layerStatus string, layerStatusBold, layerStatusRed, hideCursor bool) string {
	return renderView(s, layerStatus, layerStatusBold, layerStatusRed, hideCursor)
}

// Status implements the Layer interface.
func (s *SessionLayer) Status() (string, bool, bool) {
	if s.connStatus == "reconnecting" {
		return s.endpoint, true, true
	}
	return s.endpoint, false, false
}
