package ui

import (
	"encoding/base64"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	te "github.com/rcarmo/go-te/pkg/te"
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
	localScreen *te.Screen
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
		// Initialize local screen from the full snapshot for future event replay
		width := m.termWidth
		if width <= 0 {
			width = 80
		}
		height := m.termHeight - 1
		if height < 1 {
			height = 24
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
		if m.showLogView {
			m.refreshLogViewport()
		}
		return m, waitForUpdate(m.client)

	case TerminalEventsMsg:
		if m.localScreen != nil {
			needsClear := replayEvents(m.localScreen, msg.Events)

			// Drain any additional pending terminal events to keep up
			// with fast-updating programs like top.
		drain:
			for {
				select {
				case pending, ok := <-m.client.Updates():
					if !ok {
						break drain
					}
					if te, ok := pending.(protocol.TerminalEvents); ok {
						if replayEvents(m.localScreen, te.Events) {
							needsClear = true
						}
					}
				default:
					break drain
				}
			}

			m.cursorRow = m.localScreen.Cursor.Row
			m.cursorCol = m.localScreen.Cursor.Col
			if needsClear {
				return m, tea.Batch(
					func() tea.Msg { return tea.ClearScreen() },
					waitForUpdate(m.client),
				)
			}
		}
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

	case tea.KeyPressMsg:
		if m.showLogView {
			return m.updateLogViewer(msg)
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

func (m Model) updateLogViewer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
	m.logViewport = viewport.New(viewport.WithWidth(10000), viewport.WithHeight(h-3))
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

// replayEvents applies terminal events to the screen. Returns true if the
// screen needs a full repaint (e.g., alt screen transition).
func replayEvents(screen *te.Screen, events []protocol.TerminalEvent) bool {
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
					if m == 1049 || m == 1047 || m == 47 {
						needsClear = true
					}
				}
			}
		case "rm":
			screen.ResetMode(ev.Params, ev.Private)
			if ev.Private {
				for _, m := range ev.Params {
					if m == 1049 || m == 1047 || m == 47 {
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
	return v
}
