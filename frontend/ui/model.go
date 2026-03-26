package ui

import (
	"encoding/base64"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	termlog "termd/frontend/log"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

// LogEntryMsg is sent by the log handler to trigger a re-render when new
// log entries arrive (throttled to 100ms).
type LogEntryMsg struct{}

type Model struct {
	client      *client.Client
	cmd         string
	cmdArgs     []string
	RegionReady chan string
	FocusCh     chan chan struct{} // raw loop reads this to enter focus mode
	Detached    bool
	prefixMode  bool
	focusDone   chan struct{}
	showLogView bool
	logViewport viewport.Model
	logHScroll  int
	LogRing     *termlog.LogRingBuffer
	regionID    string
	regionName  string
	lines       []string
	cursorRow   int
	cursorCol   int
	termWidth   int
	termHeight  int
	status      string
	err         string
}

func NewModel(c *client.Client, cmd string, args []string, ring *termlog.LogRingBuffer) Model {
	return Model{
		client:      c,
		cmd:         cmd,
		cmdArgs:     args,
		RegionReady: make(chan string, 1),
		FocusCh:     make(chan chan struct{}, 1),
		LogRing:     ring,
		status:      "connecting...",
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			err := m.client.Send(protocol.ListRegionsRequest{
				Type: "list_regions_request",
			})
			if err != nil {
				return ServerErrorMsg{Context: "list_regions", Message: err.Error()}
			}
			return nil
		},
		waitForUpdate(m.client),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		if m.regionID != "" {
			contentHeight := msg.Height - 1
			if contentHeight < 1 {
				contentHeight = 1
			}
			_ = m.client.Send(protocol.ResizeRequest{
				Type:     "resize_request",
				RegionID: m.regionID,
				Width:    uint16(msg.Width),
				Height:   uint16(contentHeight),
			})
		}
		return m, nil

	case ListRegionsResponseMsg:
		if msg.Error {
			m.err = "list regions failed: " + msg.Message
			return m, tea.Quit
		}
		if len(msg.Regions) > 0 {
			m.regionID = msg.Regions[0].RegionID
			m.regionName = msg.Regions[0].Name
			m.status = "subscribing..."
			select {
			case m.RegionReady <- m.regionID:
			default:
			}
			return m, tea.Batch(
				func() tea.Msg {
					err := m.client.Send(protocol.SubscribeRequest{
						Type:     "subscribe_request",
						RegionID: m.regionID,
					})
					if err != nil {
						return ServerErrorMsg{Context: "subscribe", Message: err.Error()}
					}
					return nil
				},
				waitForUpdate(m.client),
			)
		}
		m.status = "spawning..."
		return m, tea.Batch(
			func() tea.Msg {
				err := m.client.Send(protocol.SpawnRequest{
					Type: "spawn_request",
					Cmd:  m.cmd,
					Args: m.cmdArgs,
				})
				if err != nil {
					return ServerErrorMsg{Context: "spawn", Message: err.Error()}
				}
				return nil
			},
			waitForUpdate(m.client),
		)

	case SpawnResponseMsg:
		if msg.Error {
			m.err = "spawn failed: " + msg.Message
			return m, tea.Quit
		}
		m.regionID = msg.RegionID
		m.regionName = msg.Name
		m.status = "subscribing..."
		select {
		case m.RegionReady <- m.regionID:
		default:
		}
		return m, tea.Batch(
			func() tea.Msg {
				err := m.client.Send(protocol.SubscribeRequest{
					Type:     "subscribe_request",
					RegionID: m.regionID,
				})
				if err != nil {
					return ServerErrorMsg{Context: "subscribe", Message: err.Error()}
				}
				return nil
			},
			waitForUpdate(m.client),
		)

	case SubscribeResponseMsg:
		if msg.Error {
			m.err = "subscribe failed: " + msg.Message
			return m, tea.Quit
		}
		m.status = ""
		if m.termWidth > 0 && m.termHeight > 1 {
			_ = m.client.Send(protocol.ResizeRequest{
				Type:     "resize_request",
				RegionID: m.regionID,
				Width:    uint16(m.termWidth),
				Height:   uint16(m.termHeight - 1),
			})
		}
		return m, waitForUpdate(m.client)

	case ScreenUpdateMsg:
		m.lines = msg.Lines
		m.cursorRow = int(msg.CursorRow)
		m.cursorCol = int(msg.CursorCol)
		if m.showLogView {
			m.refreshLogViewport()
		}
		return m, waitForUpdate(m.client)

	case RegionCreatedMsg:
		if m.regionName == "" {
			m.regionName = msg.Name
		}
		return m, waitForUpdate(m.client)

	case ResizeResponseMsg:
		return m, waitForUpdate(m.client)

	case RegionDestroyedMsg:
		m.err = "region destroyed"
		return m, tea.Quit

	case ServerErrorMsg:
		m.err = msg.Context + ": " + msg.Message
		return m, tea.Quit

	case LogEntryMsg:
		if m.showLogView {
			m.refreshLogViewport()
		}
		return m, nil

	case prefixStartedMsg:
		m.prefixMode = true
		return m, nil

	case tea.KeyMsg:
		if m.showLogView {
			return m.updateLogViewer(msg)
		}
		return m.updatePrefixCommand(msg)

	default:
		return m, nil
	}
}

func (m Model) updatePrefixCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.prefixMode = false
	switch msg.String() {
	case "d":
		m.Detached = true
		return m, tea.Quit
	case "ctrl+b":
		if m.regionID != "" {
			data := base64.StdEncoding.EncodeToString([]byte{0x02})
			_ = m.client.Send(protocol.InputMsg{
				Type: "input", RegionID: m.regionID, Data: data,
			})
		}
		return m, nil
	case "l":
		m.showLogView = true
		m.initLogViewport()
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) updateLogViewer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.showLogView = false
		m.logHScroll = 0
		if m.focusDone != nil {
			close(m.focusDone)
			m.focusDone = nil
		}
		return m, nil
	case "left":
		if m.logHScroll > 0 {
			m.logHScroll--
		}
		return m, nil
	case "right":
		m.logHScroll++
		return m, nil
	case "home":
		m.logHScroll = 0
		m.logViewport.GotoTop()
		return m, nil
	default:
		var cmd tea.Cmd
		m.logViewport, cmd = m.logViewport.Update(msg)
		return m, cmd
	}
}

func (m *Model) initLogViewport() {
	w := m.termWidth * 80 / 100
	h := m.termHeight * 80 / 100
	if w < 20 {
		w = 20
	}
	if h < 5 {
		h = 5
	}
	// Wide viewport — horizontal truncation is handled in the render step
	// so horizontal scrolling can access the full line content.
	m.logViewport = viewport.New(10000, h-4)
	m.refreshLogViewport()
	m.logViewport.GotoBottom()
}

func (m *Model) refreshLogViewport() {
	if m.LogRing == nil {
		return
	}
	atBottom := m.logViewport.AtBottom()
	m.logViewport.SetContent(m.LogRing.String())
	if atBottom {
		m.logViewport.GotoBottom()
	}
}

func (m Model) View() string {
	return renderView(m)
}
