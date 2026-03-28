package ui

import (
	"encoding/base64"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	te "github.com/rcarmo/go-te/pkg/te"
	termlog "termd/frontend/log"
	"termd/frontend/client"
	"termd/frontend/protocol"
	"termd/frontend/terminal"
)

// LogEntryMsg is sent by the log handler to trigger a re-render when new
// log entries arrive (throttled to 100ms).
type LogEntryMsg struct{}

type showHintMsg struct{}
type hideHintMsg struct{}
type reconnectTickMsg struct{}

type Model struct {
	client      *client.Client
	cmd         string
	cmdArgs     []string
	Endpoint    string
	Version     string
	Changelog   string
	RegionReady chan string
	FocusCh     chan chan struct{} // raw loop reads this to enter focus mode
	Detached    bool
	prefixMode  bool
	focusDone   chan struct{}
	showHelp    bool
	helpCursor  int
	showHint    bool
	showStatus    bool
	serverStatus  *protocol.StatusResponse
	// Scrollable overlay viewer — used for log viewer and changelog
	overlayMode    string // "" = hidden, "log", "changelog"
	overlayVP      viewport.Model
	overlayHScroll int
	LogRing     *termlog.LogRingBuffer
	regionID    string
	regionName  string
	connStatus  string
	retryAt     time.Time
	localHostname  string
	termEnv        map[string]string
	keyboardFlags  int  // kitty keyboard protocol flags (0 = not supported)
	bgDark         *bool // nil = unknown, true = dark, false = light
	localScreen    *te.Screen
	lines       []string
	cursorRow   int
	cursorCol   int
	termWidth    int
	termHeight   int
	pendingClear bool
	status       string
	err          string
}

// contentHeight returns the number of rows available for terminal content
// (total height minus tab bar and status bar).
func (m Model) contentHeight() int {
	h := m.termHeight - 1 // tab bar only
	if h < 1 {
		h = 1
	}
	return h
}

func NewModel(c *client.Client, cmd string, args []string, ring *termlog.LogRingBuffer, endpoint, version, changelog string) Model {
	hostname, _ := os.Hostname()
	return Model{
		client:        c,
		cmd:           cmd,
		cmdArgs:       args,
		Endpoint:      endpoint,
		Version:       version,
		Changelog:     changelog,
		localHostname: hostname,
		RegionReady:   make(chan string, 1),
		FocusCh:       make(chan chan struct{}, 1),
		LogRing:       ring,
		connStatus:    "connected",
		status:        "connecting...",
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
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} }),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ServerIdentifyMsg:
		if msg.Hostname != m.localHostname {
			m.Endpoint = m.localHostname + " -> " + m.Endpoint
		}
		return m, waitForUpdate(m.client)

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		if m.regionID != "" {
			_ = m.client.Send(protocol.ResizeRequest{
				Type:     "resize_request",
				RegionID: m.regionID,
				Width:    uint16(msg.Width),
				Height:   uint16(m.contentHeight()),
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
		if m.termWidth > 0 && m.termHeight > 2 {
			_ = m.client.Send(protocol.ResizeRequest{
				Type:     "resize_request",
				RegionID: m.regionID,
				Width:    uint16(m.termWidth),
				Height:   uint16(m.contentHeight()),
			})
		}
		return m, waitForUpdate(m.client)

	case ScreenUpdateMsg:
		m.lines = msg.Lines
		m.cursorRow = int(msg.CursorRow)
		m.cursorCol = int(msg.CursorCol)
		// Initialize local screen from the full snapshot for future event replay
		width := m.termWidth
		if width <= 0 {
			width = 80
		}
		height := m.contentHeight()
		if m.termHeight <= 0 {
			height = 23 // default 24 - 1 for tab bar
		}
		m.localScreen = te.NewScreen(width, height)
		if len(msg.Cells) > 0 {
			// Cell data available: restore with full color/attribute info
			terminal.InitScreenFromCells(m.localScreen, msg.Cells)
		} else {
			// Plain text fallback
			for i, line := range msg.Lines {
				if i > 0 {
					m.localScreen.LineFeed()
					m.localScreen.CarriageReturn()
				}
				m.localScreen.Draw(line)
			}
		}
		// Position cursor to match server
		m.localScreen.CursorPosition(int(msg.CursorRow)+1, int(msg.CursorCol)+1)
		if m.overlayMode == "log" {
			m.refreshOverlay()
		}
		if m.pendingClear {
			m.pendingClear = false
			return m, tea.Batch(
				func() tea.Msg { return tea.ClearScreen() },
				waitForUpdate(m.client),
			)
		}
		return m, waitForUpdate(m.client)

	case TerminalEventsMsg:
		if m.localScreen != nil {
			needsClear := terminal.ReplayEvents(m.localScreen, msg.Events)

			// Drain any additional pending terminal events to keep up
			// with fast-updating programs like top.
			var pendingMsg tea.Msg
		drain:
			for {
				select {
				case pending, ok := <-m.client.Updates():
					if !ok {
						break drain
					}
					if te, ok := pending.(protocol.TerminalEvents); ok {
						if terminal.ReplayEvents(m.localScreen, te.Events) {
							needsClear = true
						}
					} else {
						// Non-events message: convert and save for processing.
						pendingMsg = convertProtocolMsg(pending)
						break drain
					}
				default:
					break drain
				}
			}

			m.cursorRow = m.localScreen.Cursor.Row
			m.cursorCol = m.localScreen.Cursor.Col
			if needsClear {
				cmds := []tea.Cmd{
					func() tea.Msg { return tea.ClearScreen() },
					waitForUpdate(m.client),
				}
				if pendingMsg != nil {
					saved := pendingMsg
					cmds = append(cmds, func() tea.Msg { return saved })
				}
				return m, tea.Batch(cmds...)
			}
			if pendingMsg != nil {
				saved := pendingMsg
				return m, tea.Batch(
					waitForUpdate(m.client),
					func() tea.Msg { return saved },
				)
			}
		}
		if m.overlayMode == "log" {
			m.refreshOverlay()
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

	case DisconnectedMsg:
		m.connStatus = "reconnecting"
		m.retryAt = msg.RetryAt
		return m, tea.Batch(
			waitForUpdate(m.client),
			tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} }),
		)

	case ReconnectedMsg:
		m.connStatus = "connected"
		m.retryAt = time.Time{}
		// Re-subscribe to the previous region
		if m.regionID != "" {
			return m, tea.Batch(
				func() tea.Msg {
					err := m.client.Send(protocol.SubscribeRequest{
						Type:     "subscribe_request",
						RegionID: m.regionID,
					})
					if err != nil {
						return ServerErrorMsg{Context: "resubscribe", Message: err.Error()}
					}
					return nil
				},
				waitForUpdate(m.client),
			)
		}
		return m, waitForUpdate(m.client)

	case ServerErrorMsg:
		m.err = msg.Context + ": " + msg.Message
		return m, tea.Quit

	case LogEntryMsg:
		if m.overlayMode == "log" {
			m.refreshOverlay()
		}
		return m, nil

	case showHintMsg:
		m.showHint = true
		return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return hideHintMsg{} })

	case hideHintMsg:
		m.showHint = false
		return m, nil

	case reconnectTickMsg:
		if m.connStatus == "reconnecting" {
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} })
		}
		return m, nil

	case protocol.StatusResponse:
		m.serverStatus = &msg
		return m, nil

	case tea.KeyboardEnhancementsMsg:
		m.keyboardFlags = msg.Flags
		return m, nil

	case tea.BackgroundColorMsg:
		dark := msg.IsDark()
		m.bgDark = &dark
		return m, nil

	case tea.EnvMsg:
		m.termEnv = make(map[string]string)
		for _, key := range []string{"TERM", "COLORTERM", "TERM_PROGRAM"} {
			if v := msg.Getenv(key); v != "" {
				m.termEnv[key] = v
			}
		}
		return m, nil

	case prefixStartedMsg:
		m.prefixMode = true
		return m, nil

	case tea.KeyPressMsg:
		if m.overlayMode != "" {
			return m.updateOverlayViewer(msg)
		}
		if m.showStatus {
			return m.updateStatusViewer(msg)
		}
		if m.showHelp {
			return m.updateHelpViewer(msg)
		}
		return m.updatePrefixCommand(msg)

	default:
		return m, nil
	}
}

func (m Model) updatePrefixCommand(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		m.overlayMode = "log"
		m.initOverlay(m.LogRing.String(), true)
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		return m, nil
	case "?":
		m.showHelp = true
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		return m, nil
	case "s":
		m.showStatus = true
		m.serverStatus = nil
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		// Request server status
		return m, func() tea.Msg {
			_ = m.client.Send(protocol.StatusRequest{Type: "status_request"})
			return nil
		}
	case "n":
		m.overlayMode = "changelog"
		m.initOverlay(strings.TrimRight(m.Changelog, "\n"), false)
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		return m, nil
	case "r":
		// Request a full screen refresh from the server.
		// The ClearScreen happens when the response arrives (in ScreenUpdateMsg).
		if m.regionID != "" {
			m.pendingClear = true
			return m, tea.Batch(
				func() tea.Msg {
					_ = m.client.Send(protocol.GetScreenRequest{
						Type:     "get_screen_request",
						RegionID: m.regionID,
					})
					return nil
				},
				waitForUpdate(m.client),
			)
		}
		return m, nil
	default:
		return m, nil
	}
}

type helpItem struct {
	key    string
	label  string
	action func(m Model) (Model, tea.Cmd)
}

var helpItems = []helpItem{
	{"d", "detach", func(m Model) (Model, tea.Cmd) {
		m.Detached = true
		return m, tea.Quit
	}},
	{"l", "log viewer", func(m Model) (Model, tea.Cmd) {
		m.overlayMode = "log"
		m.initOverlay(m.LogRing.String(), true)
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		return m, nil
	}},
	{"s", "status", func(m Model) (Model, tea.Cmd) {
		m.showStatus = true
		m.serverStatus = nil
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		_ = m.client.Send(protocol.StatusRequest{Type: "status_request"})
		return m, nil
	}},
	{"n", "release notes", func(m Model) (Model, tea.Cmd) {
		m.overlayMode = "changelog"
		m.initOverlay(strings.TrimRight(m.Changelog, "\n"), false)
		done := make(chan struct{})
		m.focusDone = done
		select {
		case m.FocusCh <- done:
		default:
		}
		return m, nil
	}},
	{"r", "refresh screen", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			m.pendingClear = true
			_ = m.client.Send(protocol.GetScreenRequest{
				Type:     "get_screen_request",
				RegionID: m.regionID,
			})
			return m, waitForUpdate(m.client)
		}
		return m, nil
	}},
	{"b", "send literal ctrl+b", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			data := base64.StdEncoding.EncodeToString([]byte{0x02})
			_ = m.client.Send(protocol.InputMsg{
				Type: "input", RegionID: m.regionID, Data: data,
			})
		}
		return m, nil
	}},
}

func (m Model) closeHelp() Model {
	m.showHelp = false
	m.helpCursor = 0
	if m.focusDone != nil {
		close(m.focusDone)
		m.focusDone = nil
	}
	return m
}



func (m Model) updateStatusViewer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "s":
		m.showStatus = false
		m.serverStatus = nil
		if m.focusDone != nil {
			close(m.focusDone)
			m.focusDone = nil
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) updateHelpViewer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "?":
		m = m.closeHelp()
		return m, nil
	case "up", "k":
		if m.helpCursor > 0 {
			m.helpCursor--
		}
		return m, nil
	case "down", "j":
		if m.helpCursor < len(helpItems)-1 {
			m.helpCursor++
		}
		return m, nil
	case "enter":
		item := helpItems[m.helpCursor]
		m = m.closeHelp()
		return item.action(m)
	default:
		// Direct key shortcut while help is open
		for _, item := range helpItems {
			if msg.String() == item.key {
				m = m.closeHelp()
				return item.action(m)
			}
		}
		return m, nil
	}
}

func (m Model) updateOverlayViewer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.overlayMode = ""
		m.overlayHScroll = 0
		if m.focusDone != nil {
			close(m.focusDone)
			m.focusDone = nil
		}
		return m, nil
	case "left":
		if m.overlayHScroll > 0 {
			m.overlayHScroll--
		}
		return m, nil
	case "right":
		m.overlayHScroll++
		return m, nil
	case "home":
		m.overlayHScroll = 0
		m.overlayVP.GotoTop()
		return m, nil
	default:
		var cmd tea.Cmd
		m.overlayVP, cmd = m.overlayVP.Update(msg)
		return m, cmd
	}
}

func (m *Model) initOverlay(content string, gotoBottom bool) {
	w := m.termWidth * 80 / 100
	h := m.termHeight * 80 / 100
	if w < 20 {
		w = 20
	}
	if h < 5 {
		h = 5
	}
	m.overlayHScroll = 0
	m.overlayVP = viewport.New(viewport.WithWidth(10000), viewport.WithHeight(h-3))
	m.overlayVP.SetContent(content)
	if gotoBottom {
		m.overlayVP.GotoBottom()
	}
}

func (m *Model) refreshOverlay() {
	if m.overlayMode != "log" || m.LogRing == nil {
		return
	}
	atBottom := m.overlayVP.AtBottom()
	m.overlayVP.SetContent(m.LogRing.String())
	if atBottom {
		m.overlayVP.GotoBottom()
	}
}

func (m Model) View() tea.View {
	v := tea.NewView(renderView(m))
	v.AltScreen = true
	return v
}
