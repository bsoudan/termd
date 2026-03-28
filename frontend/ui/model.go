package ui

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	Detached   bool
	prefixMode bool
	nextReqID  uint64
	pending    map[uint64]ReplyFunc
	showHint   bool
	overlay    Overlay
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
// ReplyFunc is called when a server response matches a pending request.
type ReplyFunc func(payload any)

// request sends a message to the server with a req_id and registers a reply
// handler. When the response arrives, the handler is called from Update().
func (m *Model) request(msg any, reply ReplyFunc) {
	m.nextReqID++
	m.pending[m.nextReqID] = reply
	m.server.Send(protocol.TaggedWithReqID(msg, m.nextReqID))
}

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
		pending:       make(map[uint64]ReplyFunc),
		connStatus:    "connected",
		status:        "connecting...",
	}
}

func (m Model) Init() tea.Cmd {
	m.server.Send(protocol.ListRegionsRequest{})
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return showHintMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Unwrap protocol.Message: check for reply handler, then dispatch on payload.
	if pmsg, ok := msg.(protocol.Message); ok {
		if pmsg.ReqID > 0 {
			if reply, ok := m.pending[pmsg.ReqID]; ok {
				delete(m.pending, pmsg.ReqID)
				reply(pmsg.Payload)
				return m, nil
			}
		}
		msg = pmsg.Payload
	}

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
		if m.overlay != nil {
			m.refreshLogOverlay()
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
		if so, ok := m.overlay.(*StatusOverlay); ok { so.SetStatus(&msg) }
		return m, nil

	case protocol.GetScrollbackResponse:
		m.scrollback = m.scrollback.SetData(msg.Lines)
		return m, nil

	case ServerErrorMsg:
		m.err = msg.Context + ": " + msg.Message
		return m.quit()

	case LogEntryMsg:
		if m.overlay != nil {
			m.refreshLogOverlay()
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
		if m.overlay != nil {
			return m.updateOverlay(msg)
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
	if m.overlay != nil {
		m.refreshLogOverlay()
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
	if m.overlay != nil || m.scrollback.Active() {
		if bytes.Contains(chunk, sgrMousePrefix) {
			mice, rest := extractSGRMouseSequences(chunk)
			var cmds []tea.Cmd
			if len(rest) > 0 {
				pipeW := m.pipeW
				cmds = append(cmds, func() tea.Msg {
					pipeW.Write(rest)
					return nil
				})
			}
			for _, mouse := range mice {
				saved := mouse
				cmds = append(cmds, func() tea.Msg { return saved })
			}
			return m, tea.Batch(cmds...)
		}
		pipeW := m.pipeW
		data := make([]byte, len(chunk))
		copy(data, chunk)
		return m, func() tea.Msg {
			pipeW.Write(data)
			return nil
		}
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
		m.overlay = NewScrollableOverlay("logviewer", m.LogRing.String(), true, m.termWidth, m.termHeight)
		return m, nil
	case '?':
		m.overlay = NewHelpOverlay(helpItems)
		return m, nil
	case 's':
		so := NewStatusOverlay(m.buildStatusCaps())
		m.overlay = so
		m.request(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				so.SetStatus(resp)
			}
		})
		return m, nil
	case 'n':
		m.overlay = NewScrollableOverlay("release notes", strings.TrimRight(m.Changelog, "\n"), false, m.termWidth, m.termHeight)
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
		m.overlay = NewScrollableOverlay("logviewer", m.LogRing.String(), true, m.termWidth, m.termHeight)
		return m, nil
	}},
	{"s", "status", func(m Model) (Model, tea.Cmd) {
		so := NewStatusOverlay(m.buildStatusCaps())
		m.overlay = so
		m.request(protocol.StatusRequest{}, func(payload any) {
			if resp, ok := payload.(*protocol.StatusResponse); ok {
				so.SetStatus(resp)
			}
		})
		return m, nil
	}},
	{"n", "release notes", func(m Model) (Model, tea.Cmd) {
		m.overlay = NewScrollableOverlay("release notes", strings.TrimRight(m.Changelog, "\n"), false, m.termWidth, m.termHeight)
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

func (m Model) updateOverlay(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	updated, action, cmd := m.overlay.Update(msg)
	m.overlay = updated
	if action != nil {
		m, cmd2 := action(m)
		return m, tea.Batch(cmd, cmd2)
	}
	return m, cmd
}

func (m *Model) refreshLogOverlay() {
	if so, ok := m.overlay.(*ScrollableOverlay); ok && so.Label() == "logviewer" {
		so.RefreshContent(m.LogRing.String())
	}
}

func (m Model) buildStatusCaps() StatusCaps {
	caps := StatusCaps{
		Hostname:      m.localHostname,
		Endpoint:      m.Endpoint,
		Version:       m.Version,
		ConnStatus:    m.connStatus,
		KeyboardFlags: m.keyboardFlags,
		BgDark:        m.bgDark,
		TermEnv:       m.termEnv,
	}
	if m.terminal.Screen != nil {
		var mouseModes []string
		if _, ok := m.terminal.Screen.Mode[privateModeKey(ansi.ModeMouseNormal.Mode())]; ok {
			mouseModes = append(mouseModes, "normal(1000)")
		}
		if _, ok := m.terminal.Screen.Mode[privateModeKey(ansi.ModeMouseButtonEvent.Mode())]; ok {
			mouseModes = append(mouseModes, "button(1002)")
		}
		if _, ok := m.terminal.Screen.Mode[privateModeKey(ansi.ModeMouseAnyEvent.Mode())]; ok {
			mouseModes = append(mouseModes, "any(1003)")
		}
		if _, ok := m.terminal.Screen.Mode[privateModeKey(ansi.ModeMouseExtSgr.Mode())]; ok {
			mouseModes = append(mouseModes, "sgr(1006)")
		}
		if len(mouseModes) > 0 {
			caps.MouseModes = strings.Join(mouseModes, ", ")
		} else {
			caps.MouseModes = "off"
		}
	}
	return caps
}

// handleMouse processes mouse events. If an overlay is active, route to it.
// If the child app has mouse mode enabled, forward to the server.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Overlays get mouse events (scroll wheel) while they're visible
	if m.overlay != nil {
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			var cmd tea.Cmd
			m.overlay, cmd = m.overlay.HandleWheel(wheel)
			return m, cmd
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
