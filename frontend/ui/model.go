package ui

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// modeAltScreenLegacy is the original xterm alternate screen mode (DEC private 47).
// Not defined in charmbracelet/x/ansi which only has 1047 and 1049.
const modeAltScreenLegacy = 47

// privateModeKey converts a DEC private mode number to the key used
// by go-te's Screen.Mode map, which shifts private modes left by 5 bits.
func privateModeKey(mode int) int {
	return mode << 5
}


type Model struct {
	server  *Server
	pipeW   io.Writer // bubbletea's input pipe (for focus mode key events)
	cmd         string
	cmdArgs     []string
	Endpoint    string
	Version     string
	Changelog   string
	Detached    bool
	prefixMode  bool
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
	terminal   Terminal
	termWidth  int
	termHeight int
	status       string
	err          string
	scrollback Scrollback
}

// contentHeight returns the number of rows available for terminal content
// (total height minus tab bar and status bar).
// quit sends unsubscribe and disconnect to the server, then returns tea.Quit.
func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.regionID != "" {
		m.server.Send(protocol.UnsubscribeRequest{RegionID: m.regionID})
	}
	m.server.Send(protocol.Disconnect{})
	return m, tea.Quit
}

func (m Model) contentHeight() int {
	h := m.termHeight - 1 // tab bar only
	if h < 1 {
		h = 1
	}
	return h
}

func NewModel(s *Server, pipeW io.Writer, cmd string, args []string, ring *termlog.LogRingBuffer, endpoint, version, changelog string) Model {
	hostname, _ := os.Hostname()
	return Model{
		server:        s,
		pipeW:         pipeW,
		cmd:           cmd,
		cmdArgs:       args,
		Endpoint:      endpoint,
		Version:       version,
		Changelog:     changelog,
		localHostname: hostname,
		LogRing:       ring,
		connStatus:    "connected",
		status:        "connecting...",
	}
}

func (m Model) Init() tea.Cmd {
	m.server.Send(protocol.ListRegionsRequest{})
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case RawInputMsg:
		return m.handleRawInput([]byte(msg))

	case protocol.Identify:
		if msg.Hostname != m.localHostname {
			m.Endpoint = m.localHostname + " -> " + m.Endpoint
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		if m.regionID != "" {
			m.server.Send(protocol.ResizeRequest{
				RegionID: m.regionID,
				Width:    uint16(msg.Width),
				Height:   uint16(m.contentHeight()),
			})
		}
		return m, nil

	case protocol.ListRegionsResponse:
		if msg.Error {
			m.err = "list regions failed: " + msg.Message
			return m.quit()
		}
		if len(msg.Regions) > 0 {
			m.regionID = msg.Regions[0].RegionID
			m.regionName = msg.Regions[0].Name
			m.status = "subscribing..."
			m.server.Send(protocol.SubscribeRequest{
				RegionID: m.regionID,
			})
			return m, nil
		}
		m.status = "spawning..."
		m.server.Send(protocol.SpawnRequest{
			Cmd:  m.cmd,
			Args: m.cmdArgs,
		})
		return m, nil

	case protocol.SpawnResponse:
		if msg.Error {
			m.err = "spawn failed: " + msg.Message
			return m.quit()
		}
		m.regionID = msg.RegionID
		m.regionName = msg.Name
		m.status = "subscribing..."
		m.server.Send(protocol.SubscribeRequest{
			RegionID: m.regionID,
		})
		return m, nil

	case protocol.SubscribeResponse:
		if msg.Error {
			m.err = "subscribe failed: " + msg.Message
			return m.quit()
		}
		m.status = ""
		if m.termWidth > 0 && m.termHeight > 2 {
			m.server.Send(protocol.ResizeRequest{
				RegionID: m.regionID,
				Width:    uint16(m.termWidth),
				Height:   uint16(m.contentHeight()),
			})
		}
		return m, nil

	// ScreenUpdate and GetScreenResponse have the same fields — handle both
	case protocol.ScreenUpdate:
		return m.handleScreenUpdate(msg.Lines, msg.Cells, msg.CursorRow, msg.CursorCol)
	case protocol.GetScreenResponse:
		return m.handleScreenUpdate(msg.Lines, msg.Cells, msg.CursorRow, msg.CursorCol)

	case protocol.TerminalEvents:
		var needsClear bool
		m.terminal, needsClear = m.terminal.HandleTerminalEvents(msg.Events)
		if needsClear {
			return m, func() tea.Msg { return tea.ClearScreen() }
		}
		if m.overlayMode == "log" {
			m.refreshOverlay()
		}
		return m, nil

	case protocol.RegionCreated:
		if m.regionName == "" {
			m.regionName = msg.Name
		}
		return m, nil

	case protocol.ResizeResponse:
		return m, nil

	case protocol.RegionDestroyed:
		m.err = "region destroyed"
		return m.quit()

	case DisconnectedMsg:
		m.connStatus = "reconnecting"
		m.retryAt = msg.RetryAt
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return reconnectTickMsg{} })

	case ReconnectedMsg:
		m.connStatus = "connected"
		m.retryAt = time.Time{}
		if m.regionID != "" {
			m.server.Send(protocol.SubscribeRequest{
				RegionID: m.regionID,
			})
		}
		return m, nil

	case protocol.StatusResponse:
		m.serverStatus = &msg
		return m, nil

	case protocol.GetScrollbackResponse:
		m.scrollback = m.scrollback.SetData(msg.Lines)
		return m, nil

	case ServerErrorMsg:
		m.err = msg.Context + ": " + msg.Message
		return m.quit()

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
		if m.scrollback.Active() {
			var exited bool
			m.scrollback, exited = m.scrollback.Update(msg, m.contentHeight())
			if exited {
				m.scrollback = m.scrollback.Exit()
			}
			return m, nil
		}
		// KeyPressMsg only arrives from bubbletea's pipe during focus mode.
		// If no viewer is active, ignore it.
		return m, nil

	default:
		return m, nil
	}
}

func (m Model) handleScreenUpdate(lines []string, cells [][]protocol.ScreenCell, cursorRow, cursorCol uint16) (tea.Model, tea.Cmd) {
	height := m.contentHeight()
	if m.termHeight <= 0 {
		height = 23
	}
	m.terminal = m.terminal.HandleScreenUpdate(lines, cells, cursorRow, cursorCol, m.termWidth, height)
	if m.overlayMode == "log" {
		m.refreshOverlay()
	}
	var clear bool
	m.terminal, clear = m.terminal.ConsumePendingClear()
	if clear {
		return m, func() tea.Msg { return tea.ClearScreen() }
	}
	return m, nil
}

const prefixKey = 0x02 // ctrl+b

// handleRawInput processes raw bytes from the terminal input goroutine.
// It handles prefix key detection, SGR mouse parsing, and input routing.
func (m Model) handleRawInput(chunk []byte) (tea.Model, tea.Cmd) {
	// Focus mode (overlay/help/status with keyboard nav): write to bubbletea's
	// input pipe so it parses as key events. Mouse still parsed here.
	if m.overlayMode != "" || m.showStatus || m.showHelp || m.scrollback.Active() {
		if bytes.Contains(chunk, sgrMousePrefix) {
			mice, rest := extractSGRMouseSequences(chunk)
			if len(rest) > 0 {
				m.pipeW.Write(rest)
			}
			var cmds []tea.Cmd
			for _, mouse := range mice {
				saved := mouse
				cmds = append(cmds, func() tea.Msg { return saved })
			}
			return m, tea.Batch(cmds...)
		}
		m.pipeW.Write(chunk)
		return m, nil
	}

	// Prefix active: next byte is the command
	if m.prefixMode {
		m.prefixMode = false
		key := chunk[0]
		chunk = chunk[1:]
		// Handle the prefix command
		model, cmd := m.handlePrefixKey(key)
		// Forward any remaining bytes
		if len(chunk) > 0 {
			m2 := model.(Model)
			m2.sendRawToServer(chunk)
			return m2, cmd
		}
		return model, cmd
	}

	// Scan for prefix key (ctrl+b)
	if idx := bytes.IndexByte(chunk, prefixKey); idx >= 0 {
		// Forward bytes before the prefix
		if idx > 0 {
			m.sendRawToServer(chunk[:idx])
		}
		m.prefixMode = true
		rest := chunk[idx+1:]
		if len(rest) > 0 {
			// Next byte is the command
			m.prefixMode = false
			key := rest[0]
			model, cmd := m.handlePrefixKey(key)
			if len(rest) > 1 {
				m2 := model.(Model)
				m2.sendRawToServer(rest[1:])
				return m2, cmd
			}
			return model, cmd
		}
		return m, nil
	}

	// Parse and route mouse sequences
	if bytes.Contains(chunk, sgrMousePrefix) {
		mice, rest := extractSGRMouseSequences(chunk)
		// Non-mouse bytes go to server
		if len(rest) > 0 {
			m.sendRawToServer(rest)
		}
		// Route mouse: child wants mouse → encode and forward to server,
		// otherwise → handle locally (scrollback, etc.)
		var cmds []tea.Cmd
		for _, mouse := range mice {
			if m.terminal.ChildWantsMouse() {
				seq := encodeSGRMouse(mouse, mouse.Mouse().X, mouse.Mouse().Y-chromeRows)
				if seq != "" {
					m.server.Send(InputMsg{
						RegionID: m.regionID,
						Data:     []byte(seq),
					})
				}
			} else {
				m2, cmd := m.handleMouse(mouse)
				m = m2.(Model)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)
	}

	// Regular input — forward to server
	m.sendRawToServer(chunk)
	return m, nil
}

// handlePrefixKey handles a single key byte after ctrl+b.
func (m Model) handlePrefixKey(key byte) (tea.Model, tea.Cmd) {
	return m.handlePrefixCommand(key)
}

// sendRawToServer forwards raw bytes as input to the active region.
func (m Model) sendRawToServer(raw []byte) {
	if m.regionID == "" || len(raw) == 0 {
		return
	}
	m.server.Send(InputMsg{
		RegionID: m.regionID,
		Data:     raw,
	})
}


func (m Model) handlePrefixCommand(key byte) (tea.Model, tea.Cmd) {
	m.prefixMode = false
	switch key {
	case 'd':
		m.Detached = true
		return m.quit()
	case prefixKey: // ctrl+b ctrl+b → send literal ctrl+b
		m.sendRawToServer([]byte{prefixKey})
		return m, nil
	case 'l':
		m.overlayMode = "log"
		m.initOverlay(m.LogRing.String(), true)
		return m, nil
	case '?':
		m.showHelp = true
		return m, nil
	case 's':
		m.showStatus = true
		m.serverStatus = nil
		m.server.Send(protocol.StatusRequest{})
		return m, nil
	case 'n':
		m.overlayMode = "changelog"
		m.initOverlay(strings.TrimRight(m.Changelog, "\n"), false)
		return m, nil
	case '[':
		if m.regionID != "" {
			m.scrollback = m.scrollback.Enter(0)
			m.server.Send(protocol.GetScrollbackRequest{RegionID: m.regionID})
		}
		return m, nil
	case 'r':
		if m.regionID != "" {
			m.terminal = m.terminal.SetPendingClear()
			m.server.Send(protocol.GetScreenRequest{
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
		model, cmd := m.quit()
		return model.(Model), cmd
	}},
	{"l", "log viewer", func(m Model) (Model, tea.Cmd) {
		m.overlayMode = "log"
		m.initOverlay(m.LogRing.String(), true)
		return m, nil
	}},
	{"s", "status", func(m Model) (Model, tea.Cmd) {
		m.showStatus = true
		m.serverStatus = nil
		m.server.Send(protocol.StatusRequest{})
		return m, nil
	}},
	{"n", "release notes", func(m Model) (Model, tea.Cmd) {
		m.overlayMode = "changelog"
		m.initOverlay(strings.TrimRight(m.Changelog, "\n"), false)
		return m, nil
	}},
	{"r", "refresh screen", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			m.terminal = m.terminal.SetPendingClear()
			m.server.Send(protocol.GetScreenRequest{
				RegionID: m.regionID,
			})
			return m, nil
		}
		return m, nil
	}},
	{"[", "scrollback", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			m.scrollback = m.scrollback.Enter(0)
			m.server.Send(protocol.GetScrollbackRequest{RegionID: m.regionID})
		}
		return m, nil
	}},
	{"b", "send literal ctrl+b", func(m Model) (Model, tea.Cmd) {
		if m.regionID != "" {
			data := base64.StdEncoding.EncodeToString([]byte{0x02})
			m.server.Send(protocol.InputMsg{
				RegionID: m.regionID, Data: data,
			})
		}
		return m, nil
	}},
}

func (m Model) closeHelp() Model {
	m.showHelp = false
	m.helpCursor = 0
	return m
}



func (m Model) updateStatusViewer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "s":
		m.showStatus = false
		m.serverStatus = nil
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
	childWantsMouse = m.terminal.ChildWantsMouse()

	if childWantsMouse && m.regionID != "" {
		// Forward to the server as SGR mouse escape sequence.
		// Adjust Y coordinate: subtract 1 for the tab bar.
		seq := encodeSGRMouse(msg, mouse.X, mouse.Y-1)
		if seq != "" {
			data := base64.StdEncoding.EncodeToString([]byte(seq))
			m.server.Send(protocol.InputMsg{
				RegionID: m.regionID, Data: data,
			})
		}
		return m, nil
	}

	// Child doesn't want mouse — scroll wheel enters/navigates scrollback
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		if m.scrollback.Active() {
			var exited bool
			m.scrollback, exited = m.scrollback.HandleWheel(wheel.Button)
			if exited {
				m.scrollback = m.scrollback.Exit()
			}
			return m, nil
		}
		if wheel.Button == tea.MouseWheelUp && m.regionID != "" {
			m.scrollback = m.scrollback.Enter(3)
			return m, func() tea.Msg {
				m.server.Send(protocol.GetScrollbackRequest{RegionID: m.regionID})
				return nil
			}
		}
	}
	return m, nil
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

	// Enable mouse on the real terminal
	switch m.terminal.MouseMode() {
	case 2:
		v.MouseMode = tea.MouseModeAllMotion
	default:
		v.MouseMode = tea.MouseModeCellMotion
	}

	return v
}
