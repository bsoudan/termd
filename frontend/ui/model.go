package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	te "github.com/rcarmo/go-te/pkg/te"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// LogEntryMsg is sent by the log handler to trigger a re-render when new
// log entries arrive (throttled to 100ms).
type LogEntryMsg struct{}

// modeAltScreenLegacy is the original xterm alternate screen mode (DEC private 47).
// Not defined in charmbracelet/x/ansi which only has 1047 and 1049.
const modeAltScreenLegacy = 47

// privateModeKey converts a DEC private mode number to the key used
// by go-te's Screen.Mode map, which shifts private modes left by 5 bits.
func privateModeKey(mode int) int {
	return mode << 5
}

type showHintMsg struct{}
type hideHintMsg struct{}
type reconnectTickMsg struct{}

type Model struct {
	server      *Server
	cmd         string
	cmdArgs     []string
	Endpoint    string
	Version     string
	Changelog   string
	RegionReady    chan string
	FocusCh        chan chan struct{} // raw loop reads this to enter focus mode
	ChildWantsMouse *atomic.Bool      // raw loop checks this to route mouse events
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
	// Scrollback navigation
	scrollbackMode   bool
	scrollbackOffset int                    // lines scrolled back from bottom (0 = live)
	scrollbackCells  [][]protocol.ScreenCell // server-side scrollback buffer
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

func NewModel(s *Server, cmd string, args []string, ring *termlog.LogRingBuffer, endpoint, version, changelog string) Model {
	hostname, _ := os.Hostname()
	return Model{
		server:          s,
		cmd:             cmd,
		cmdArgs:         args,
		Endpoint:        endpoint,
		Version:         version,
		Changelog:       changelog,
		localHostname:   hostname,
		RegionReady:     make(chan string, 1),
		FocusCh:         make(chan chan struct{}, 1),
		ChildWantsMouse: &atomic.Bool{},
		LogRing:         ring,
		connStatus:      "connected",
		status:          "connecting...",
	}
}

func (m Model) Init() tea.Cmd {
	m.server.Send(protocol.ListRegionsRequest{
		Type: "list_regions_request",
	})
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ServerIdentifyMsg:
		if msg.Hostname != m.localHostname {
			m.Endpoint = m.localHostname + " -> " + m.Endpoint
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		if m.regionID != "" {
			m.server.Send(protocol.ResizeRequest{
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
			m.server.Send(protocol.SubscribeRequest{
				Type:     "subscribe_request",
				RegionID: m.regionID,
			})
			return m, nil
		}
		m.status = "spawning..."
		m.server.Send(protocol.SpawnRequest{
			Type: "spawn_request",
			Cmd:  m.cmd,
			Args: m.cmdArgs,
		})
		return m, nil

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
		m.server.Send(protocol.SubscribeRequest{
			Type:     "subscribe_request",
			RegionID: m.regionID,
		})
		return m, nil

	case SubscribeResponseMsg:
		if msg.Error {
			m.err = "subscribe failed: " + msg.Message
			return m, tea.Quit
		}
		m.status = ""
		if m.termWidth > 0 && m.termHeight > 2 {
			m.server.Send(protocol.ResizeRequest{
				Type:     "resize_request",
				RegionID: m.regionID,
				Width:    uint16(m.termWidth),
				Height:   uint16(m.contentHeight()),
			})
		}
		return m, nil

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
			initScreenFromCells(m.localScreen, msg.Cells)
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
			return m, func() tea.Msg { return tea.ClearScreen() }
		}
		return m, nil

	case TerminalEventsMsg:
		if m.localScreen != nil {
			needsClear := ReplayEvents(m.localScreen, msg.Events)
			m.cursorRow = m.localScreen.Cursor.Row
			m.cursorCol = m.localScreen.Cursor.Col
			if needsClear {
				return m, func() tea.Msg { return tea.ClearScreen() }
			}
		}
		if m.overlayMode == "log" {
			m.refreshOverlay()
		}
		return m, nil

	case RegionCreatedMsg:
		if m.regionName == "" {
			m.regionName = msg.Name
		}
		return m, nil

	case ResizeResponseMsg:
		return m, nil

	case RegionDestroyedMsg:
		m.err = "region destroyed"
		return m, tea.Quit

	case DisconnectedMsg:
		m.connStatus = "reconnecting"
		m.retryAt = msg.RetryAt
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} })

	case ReconnectedMsg:
		m.connStatus = "connected"
		m.retryAt = time.Time{}
		if m.regionID != "" {
			m.server.Send(protocol.SubscribeRequest{
				Type:     "subscribe_request",
				RegionID: m.regionID,
			})
		}
		return m, nil

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

	case ScrollbackResponseMsg:
		m.scrollbackCells = msg.Lines
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

	case tea.MouseMsg:
		return m.handleMouse(msg)

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
		if m.scrollbackMode {
			return m.updateScrollbackViewer(msg)
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
			m.server.Send(protocol.InputMsg{
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
			m.server.Send(protocol.StatusRequest{Type: "status_request"})
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
	case "[":
		// Enter scrollback mode — request scrollback from server
		if m.regionID != "" {
			m.scrollbackMode = true
			m.scrollbackOffset = 0
			done := make(chan struct{})
			m.focusDone = done
			select {
			case m.FocusCh <- done:
			default:
			}
			return m, func() tea.Msg {
				m.server.Send(protocol.GetScrollbackRequest{
					Type:     "get_scrollback_request",
					RegionID: m.regionID,
				})
				return nil
			}
		}
		return m, nil
	case "r":
		// Request a full screen refresh from the server.
		// The ClearScreen happens when the response arrives (in ScreenUpdateMsg).
		if m.regionID != "" {
			m.pendingClear = true
			m.server.Send(protocol.GetScreenRequest{
				Type:     "get_screen_request",
				RegionID: m.regionID,
			})
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
		m.server.Send(protocol.StatusRequest{Type: "status_request"})
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
			m.server.Send(protocol.GetScreenRequest{
				Type:     "get_screen_request",
				RegionID: m.regionID,
			})
			return m, nil
		}
		return m, nil
	}},
	{"[", "scrollback", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			m.scrollbackMode = true
			m.scrollbackOffset = 0
			done := make(chan struct{})
			m.focusDone = done
			select {
			case m.FocusCh <- done:
			default:
			}
			m.server.Send(protocol.GetScrollbackRequest{
				Type:     "get_scrollback_request",
				RegionID: m.regionID,
			})
		}
		return m, nil
	}},
	{"b", "send literal ctrl+b", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			data := base64.StdEncoding.EncodeToString([]byte{0x02})
			m.server.Send(protocol.InputMsg{
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



func (m Model) exitScrollback() Model {
	m.scrollbackMode = false
	m.scrollbackOffset = 0
	m.scrollbackCells = nil
	if m.focusDone != nil {
		close(m.focusDone)
		m.focusDone = nil
	}
	return m
}

func (m Model) updateScrollbackViewer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	maxOffset := len(m.scrollbackCells)
	halfPage := m.contentHeight() / 2
	if halfPage < 1 {
		halfPage = 1
	}

	switch msg.String() {
	case "q", "esc":
		m = m.exitScrollback()
		return m, nil
	case "up", "k":
		if m.scrollbackOffset < maxOffset {
			m.scrollbackOffset++
		}
		return m, nil
	case "down", "j":
		if m.scrollbackOffset > 0 {
			m.scrollbackOffset--
		}
		return m, nil
	case "pgup", "ctrl+u":
		m.scrollbackOffset += halfPage
		if m.scrollbackOffset > maxOffset {
			m.scrollbackOffset = maxOffset
		}
		return m, nil
	case "pgdown", "ctrl+d":
		m.scrollbackOffset -= halfPage
		if m.scrollbackOffset < 0 {
			m.scrollbackOffset = 0
		}
		return m, nil
	case "home", "g":
		m.scrollbackOffset = maxOffset
		return m, nil
	case "end", "G":
		m.scrollbackOffset = 0
		return m, nil
	default:
		// Any other key exits scrollback and snaps to live
		m = m.exitScrollback()
		return m, nil
	}
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

// handleMouse processes mouse events. If an overlay is active, route to it.
// If the child app has mouse mode enabled, forward to the server.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Overlays get mouse events (scroll wheel) while they're visible
	if m.overlayMode != "" || m.showStatus || m.showHelp {
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			if m.overlayMode != "" {
				var cmd tea.Cmd
				m.overlayVP, cmd = m.overlayVP.Update(wheel)
				return m, cmd
			}
		}
		return m, nil
	}

	mouse := msg.Mouse()

	// Check if the child app wants mouse events
	childWantsMouse := false
	if m.localScreen != nil {
		_, m1000 := m.localScreen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]
		_, m1002 := m.localScreen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]
		_, m1003 := m.localScreen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
		childWantsMouse = m1000 || m1002 || m1003
	}

	if childWantsMouse && m.regionID != "" {
		// Forward to the server as SGR mouse escape sequence.
		// Adjust Y coordinate: subtract 1 for the tab bar.
		seq := encodeSGRMouse(msg, mouse.X, mouse.Y-1)
		if seq != "" {
			data := base64.StdEncoding.EncodeToString([]byte(seq))
			m.server.Send(protocol.InputMsg{
				Type: "input", RegionID: m.regionID, Data: data,
			})
		}
		return m, nil
	}

	// Child doesn't want mouse — scroll wheel enters/navigates scrollback
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		if m.scrollbackMode {
			switch wheel.Button {
			case tea.MouseWheelUp:
				m.scrollbackOffset += 3
			case tea.MouseWheelDown:
				m.scrollbackOffset -= 3
				if m.scrollbackOffset <= 0 {
					// Scrolled back to live — exit scrollback
					m = m.exitScrollback()
					return m, nil
				}
			}
			return m, nil
		}
		// Scroll up activates scrollback mode (no focus mode —
		// scroll wheel events always arrive via program.Send)
		if wheel.Button == tea.MouseWheelUp && m.regionID != "" {
			m.scrollbackMode = true
			m.scrollbackOffset = 3
			return m, func() tea.Msg {
				m.server.Send(protocol.GetScrollbackRequest{
					Type:     "get_scrollback_request",
					RegionID: m.regionID,
				})
				return nil
			}
		}
	}
	return m, nil
}

// encodeSGRMouse encodes a mouse event as an SGR mouse escape sequence.
// Format: ESC [ < button ; col ; row M (press) or m (release)
func encodeSGRMouse(msg tea.MouseMsg, col, row int) string {
	if row < 0 {
		row = 0
	}
	// SGR uses 1-based coordinates
	col++
	row++

	var button int
	var suffix byte

	switch e := msg.(type) {
	case tea.MouseClickMsg:
		suffix = 'M'
		button = mouseButtonSGR(e.Button)
	case tea.MouseReleaseMsg:
		suffix = 'm'
		button = mouseButtonSGR(e.Button)
	case tea.MouseWheelMsg:
		suffix = 'M'
		switch e.Button {
		case tea.MouseWheelUp:
			button = 64
		case tea.MouseWheelDown:
			button = 65
		default:
			return ""
		}
	case tea.MouseMotionMsg:
		suffix = 'M'
		button = mouseButtonSGR(e.Button) + 32 // motion adds 32
	default:
		return ""
	}

	return fmt.Sprintf("%c[<%d;%d;%d%c", ansi.ESC, button, col, row, suffix)
}

func mouseButtonSGR(b tea.MouseButton) int {
	switch b {
	case tea.MouseLeft:
		return 0
	case tea.MouseMiddle:
		return 1
	case tea.MouseRight:
		return 2
	default:
		return 0
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

// ReplayEvents applies terminal events to the screen. Returns true if the
// screen needs a full repaint (e.g., alt screen transition).
func ReplayEvents(screen *te.Screen, events []protocol.TerminalEvent) bool {
	needsClear := false
	for _, ev := range events {
		switch ev.Op {
		case "draw":
			screen.Draw(ev.Data)
		case "cup":
			screen.CursorPosition(ev.Params...)
		case "cuu":
			screen.CursorUp(ev.Params...)
		case "cud":
			screen.CursorDown(ev.Params...)
		case "cuf":
			screen.CursorForward(ev.Params...)
		case "cub":
			screen.CursorBack(ev.Params...)
		case "su":
			screen.ScrollUp(ev.Params...)
		case "sd":
			screen.ScrollDown(ev.Params...)
		case "ed":
			screen.EraseInDisplay(ev.How, ev.Private)
		case "el":
			screen.EraseInLine(ev.How, ev.Private)
		case "il":
			screen.InsertLines(ev.Params...)
		case "dl":
			screen.DeleteLines(ev.Params...)
		case "ich":
			screen.InsertCharacters(ev.Params...)
		case "dch":
			screen.DeleteCharacters(ev.Params...)
		case "ech":
			screen.EraseCharacters(ev.Params...)
		case "sgr":
			screen.SelectGraphicRendition(ev.Attrs, ev.Private)
		case "lf":
			screen.LineFeed()
		case "cr":
			screen.CarriageReturn()
		case "tab":
			screen.Tab()
		case "bs":
			screen.Backspace()
		case "ind":
			screen.Index()
		case "ri":
			screen.ReverseIndex()
		case "decstbm":
			screen.SetMargins(ev.Params...)
		case "sc":
			screen.SaveCursor()
		case "rc":
			screen.RestoreCursor()
		case "decsc":
			screen.SaveCursorDEC()
		case "decrc":
			screen.RestoreCursorDEC()
		case "cud1":
			screen.CursorDown1(ev.Params...)
		case "cuu1":
			screen.CursorUp1(ev.Params...)
		case "cha":
			screen.CursorToColumn(ev.Params...)
		case "hpa":
			screen.CursorToColumnAbsolute(ev.Params...)
		case "cbt":
			screen.CursorBackTab(ev.Params...)
		case "cht":
			screen.CursorForwardTab(ev.Params...)
		case "vpa":
			screen.CursorToLine(ev.Params...)
		case "nel":
			screen.NextLine()
		case "so":
			screen.ShiftOut()
		case "si":
			screen.ShiftIn()
		case "hts":
			screen.SetTabStop()
		case "tbc":
			screen.ClearTabStop(ev.Params...)
		case "decaln":
			screen.AlignmentDisplay()
		case "fi":
			screen.ForwardIndex()
		case "bi":
			screen.BackIndex()
		case "decstr":
			screen.SoftReset()
		case "spa":
			screen.StartProtectedArea()
		case "epa":
			screen.EndProtectedArea()
		case "rep":
			screen.RepeatLast(ev.Params...)
		case "decsca":
			if len(ev.Params) > 0 {
				screen.SetCharacterProtection(ev.Params[0])
			}
		case "declrmm":
			if len(ev.Params) >= 2 {
				screen.SetLeftRightMargins(ev.Params[0], ev.Params[1])
			}
		case "decic":
			if len(ev.Params) > 0 {
				screen.InsertColumns(ev.Params[0])
			}
		case "decdc":
			if len(ev.Params) > 0 {
				screen.DeleteColumns(ev.Params[0])
			}
		case "decer":
			if len(ev.Params) >= 4 {
				screen.EraseRectangle(ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3])
			}
		case "decser":
			if len(ev.Params) >= 4 {
				screen.SelectiveEraseRectangle(ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3])
			}
		case "decfra":
			if len(ev.Params) >= 4 && len(ev.Data) > 0 {
				ch := []rune(ev.Data)[0]
				screen.FillRectangle(ch, ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3])
			}
		case "deccra":
			if len(ev.Params) >= 6 {
				screen.CopyRectangle(ev.Params[0], ev.Params[1], ev.Params[2], ev.Params[3], ev.Params[4], ev.Params[5])
			}
		case "savem":
			screen.SaveModes(ev.Params)
		case "restm":
			screen.RestoreModes(ev.Params)
		case "icon":
			screen.SetIconName(ev.Data)
		case "charset":
			// Data format: "code:mode"
			if parts := splitCharset(ev.Data); len(parts) == 2 {
				screen.DefineCharset(parts[0], parts[1])
			}
		case "winop":
			screen.WindowOp(ev.Params)
		case "decscl":
			if len(ev.Params) >= 2 {
				screen.SetConformance(ev.Params[0], ev.Params[1])
			}
		case "titlemode":
			screen.SetTitleMode(ev.Params, false)
		case "decscusr":
			if len(ev.Params) > 0 {
				screen.SetCursorStyle(ev.Params[0])
			}
		case "decsasd":
			if len(ev.Params) > 0 {
				screen.SetActiveStatusDisplay(ev.Params[0])
			}
		case "reset":
			screen.Reset()
		case "title":
			screen.SetTitle(ev.Data)
		case "bell":
			screen.Bell()
		case "sm":
			screen.SetMode(ev.Params, ev.Private)
			if ev.Private {
				for _, m := range ev.Params {
					if m == ansi.ModeAltScreenSaveCursor.Mode() || m == ansi.ModeAltScreen.Mode() || m == modeAltScreenLegacy {
						needsClear = true
					}
				}
			}
		case "rm":
			screen.ResetMode(ev.Params, ev.Private)
			if ev.Private {
				for _, m := range ev.Params {
					if m == ansi.ModeAltScreenSaveCursor.Mode() || m == ansi.ModeAltScreen.Mode() || m == modeAltScreenLegacy {
						needsClear = true
					}
				}
			}
		}
	}
	return needsClear
}

// initScreenFromCells writes cell data (including colors and attributes)
// directly into the screen buffer.
func initScreenFromCells(screen *te.Screen, cells [][]protocol.ScreenCell) {
	for row := range screen.Buffer {
		if row >= len(cells) {
			break
		}
		for col := range screen.Buffer[row] {
			if col >= len(cells[row]) {
				break
			}
			pc := cells[row][col]
			ch := pc.Char
			if ch == "" || ch == "\x00" {
				ch = " "
			}
			screen.Buffer[row][col] = te.Cell{
				Data: ch,
				Attr: te.Attr{
					Fg:            specToColor(pc.Fg),
					Bg:            specToColor(pc.Bg),
					Bold:          pc.A&1 != 0,
					Italics:       pc.A&2 != 0,
					Underline:     pc.A&4 != 0,
					Strikethrough: pc.A&8 != 0,
					Reverse:       pc.A&16 != 0,
					Blink:         pc.A&32 != 0,
					Conceal:       pc.A&64 != 0,
				},
			}
		}
	}
}

// specToColor converts a color spec string back to a go-te Color.
func specToColor(spec string) te.Color {
	if spec == "" {
		return te.Color{Mode: te.ColorDefault, Name: "default"}
	}
	// ANSI256: "5;N"
	if len(spec) > 2 && spec[0] == '5' && spec[1] == ';' {
		var idx uint8
		fmt.Sscanf(spec[2:], "%d", &idx)
		return te.Color{Mode: te.ColorANSI256, Index: idx}
	}
	// TrueColor: "2;rrggbb"
	if len(spec) > 2 && spec[0] == '2' && spec[1] == ';' {
		return te.Color{Mode: te.ColorTrueColor, Name: spec[2:]}
	}
	// ANSI16: color name like "red", "brightgreen"
	idx, _ := protocol.ANSI16NameToIndex[spec]
	return te.Color{Mode: te.ColorANSI16, Name: spec, Index: idx}
}

func splitCharset(s string) []string {
	i := strings.Index(s, ":")
	if i < 0 {
		return nil
	}
	return []string{s[:i], s[i+1:]}
}

func (m Model) View() tea.View {
	v := tea.NewView(renderView(m))
	v.AltScreen = true

	// Enable mouse on the real terminal when the child app requests it,
	// or for scroll wheel support in termd-tui's own UI.
	if m.localScreen != nil {
		_, m1000 := m.localScreen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]
		_, m1002 := m.localScreen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]
		_, m1003 := m.localScreen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]
		childMouse := m1000 || m1002 || m1003

		// Update the atomic flag so the raw input loop knows whether to
		// forward mouse events to the server or route them to bubbletea.
		m.ChildWantsMouse.Store(childMouse)

		if m1003 {
			v.MouseMode = tea.MouseModeAllMotion
		} else if m1002 || m1000 {
			v.MouseMode = tea.MouseModeCellMotion
		} else {
			// Child doesn't want mouse — enable for scroll wheel / selection
			v.MouseMode = tea.MouseModeCellMotion
		}
	}

	return v
}
