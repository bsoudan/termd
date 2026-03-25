// Package ui contains the bubbletea model, messages, and renderer.
package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

// Model is the bubbletea model for the termd frontend.
// Input is handled by a separate raw stdin reader (see rawio.go),
// not by bubbletea's key parsing.
type Model struct {
	client      *client.Client
	cmd         string
	cmdArgs     []string
	RegionReady chan string // signals raw input goroutine with regionID
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

func NewModel(c *client.Client, cmd string, args []string) Model {
	return Model{
		client:      c,
		cmd:         cmd,
		cmdArgs:     args,
		RegionReady: make(chan string, 1),
		status:      "spawning...",
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

	case SpawnResponseMsg:
		if msg.Error {
			m.err = "spawn failed: " + msg.Message
			return m, tea.Quit
		}
		m.regionID = msg.RegionID
		m.regionName = msg.Name
		m.status = "subscribing..."
		// Signal the raw input goroutine that regionID is available
		select {
		case m.RegionReady <- msg.RegionID:
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

	default:
		return m, nil
	}
}

func (m Model) View() string {
	return renderView(m)
}
