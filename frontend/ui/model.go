// Package ui contains the bubbletea model, messages, and renderer.
package ui

import (
	"encoding/base64"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

// Model is the bubbletea model for the termd frontend.
type Model struct {
	client     *client.Client
	cmd        string
	cmdArgs    []string
	regionID   string
	regionName string
	lines      []string
	cursorRow  int
	cursorCol  int
	termWidth  int
	termHeight int
	status     string
	err        string
}

func NewModel(c *client.Client, cmd string, args []string) Model {
	return Model{
		client:  c,
		cmd:     cmd,
		cmdArgs: args,
		status:  "spawning...",
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
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
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		if m.regionID != "" {
			contentHeight := msg.Height - 1 // subtract tab bar
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

	case SpawnResponseMsg:
		if msg.Error {
			m.err = "spawn failed: " + msg.Message
			return m, tea.Quit
		}
		m.regionID = msg.RegionID
		m.regionName = msg.Name
		m.status = "subscribing..."
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
		// Send initial resize now that we're subscribed
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
		return m, waitForUpdate(m.client)

	case RegionCreatedMsg:
		if m.regionName == "" {
			m.regionName = msg.Name
		}
		return m, waitForUpdate(m.client)

	case ResizeResponseMsg:
		// No action needed; resize is fire-and-forget from the UI perspective.
		return m, waitForUpdate(m.client)

	case RegionDestroyedMsg:
		m.err = "region destroyed"
		return m, tea.Quit

	case ServerErrorMsg:
		m.err = msg.Context + ": " + msg.Message
		return m, tea.Quit

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.regionID != "" {
			raw := keyToBytes(msg)
			if len(raw) > 0 {
				data := base64.StdEncoding.EncodeToString(raw)
				err := m.client.Send(protocol.InputMsg{
					Type:     "input",
					RegionID: m.regionID,
					Data:     data,
				})
				if err != nil {
					slog.Debug("input send error", "error", err)
				}
			}
		}
		return m, nil

	default:
		return m, nil
	}
}

func (m Model) View() string {
	return renderView(m)
}

// keyToBytes converts a bubbletea KeyMsg to the raw bytes to send to the PTY.
func keyToBytes(msg tea.KeyMsg) []byte {
	switch msg.Type {
	case tea.KeyRunes:
		return []byte(string(msg.Runes))
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyBackspace:
		return []byte{'\x7f'}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyEscape:
		return []byte{'\x1b'}
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	case tea.KeyF1:
		return []byte("\x1bOP")
	case tea.KeyF2:
		return []byte("\x1bOQ")
	case tea.KeyF3:
		return []byte("\x1bOR")
	case tea.KeyF4:
		return []byte("\x1bOS")
	case tea.KeyF5:
		return []byte("\x1b[15~")
	case tea.KeyF6:
		return []byte("\x1b[17~")
	case tea.KeyF7:
		return []byte("\x1b[18~")
	case tea.KeyF8:
		return []byte("\x1b[19~")
	case tea.KeyF9:
		return []byte("\x1b[20~")
	case tea.KeyF10:
		return []byte("\x1b[21~")
	case tea.KeyF11:
		return []byte("\x1b[23~")
	case tea.KeyF12:
		return []byte("\x1b[24~")
	case tea.KeyCtrlA:
		return []byte{'\x01'}
	case tea.KeyCtrlB:
		return []byte{'\x02'}
	case tea.KeyCtrlC:
		return []byte{'\x03'}
	case tea.KeyCtrlD:
		return []byte{'\x04'}
	case tea.KeyCtrlE:
		return []byte{'\x05'}
	case tea.KeyCtrlF:
		return []byte{'\x06'}
	case tea.KeyCtrlK:
		return []byte{'\x0b'}
	case tea.KeyCtrlL:
		return []byte{'\x0c'}
	case tea.KeyCtrlN:
		return []byte{'\x0e'}
	case tea.KeyCtrlP:
		return []byte{'\x10'}
	case tea.KeyCtrlR:
		return []byte{'\x12'}
	case tea.KeyCtrlU:
		return []byte{'\x15'}
	case tea.KeyCtrlW:
		return []byte{'\x17'}
	case tea.KeyCtrlZ:
		return []byte{'\x1a'}
	default:
		return nil
	}
}
