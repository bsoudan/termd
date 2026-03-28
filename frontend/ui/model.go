package ui

import (
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

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
