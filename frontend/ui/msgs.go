package ui

import (
	"fmt"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

// ── Tea messages (bridge between client channel and bubbletea) ──────────────

type ScreenUpdateMsg struct {
	RegionID  string
	CursorRow uint16
	CursorCol uint16
	Lines     []string
}

type TerminalEventsMsg struct {
	RegionID string
	Events   []protocol.TerminalEvent
}

type RegionCreatedMsg struct {
	RegionID string
	Name     string
}

type RegionDestroyedMsg struct {
	RegionID string
}

type SpawnResponseMsg struct {
	RegionID string
	Name     string
	Error    bool
	Message  string
}

type SubscribeResponseMsg struct {
	RegionID string
	Error    bool
	Message  string
}

type ResizeResponseMsg struct {
	RegionID string
	Error    bool
	Message  string
}

type ListRegionsResponseMsg struct {
	Regions []protocol.RegionInfo
	Error   bool
	Message string
}

type ServerErrorMsg struct {
	Context string
	Message string
}

// waitForUpdate returns a tea.Cmd that blocks on c.Updates() until a
// recognized message arrives, then returns the appropriate tea.Msg.
func waitForUpdate(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		for {
			msg, ok := <-c.Updates()
			if !ok {
				return RegionDestroyedMsg{}
			}
			switch m := msg.(type) {
			case protocol.ScreenUpdate:
				return ScreenUpdateMsg{RegionID: m.RegionID, CursorRow: m.CursorRow, CursorCol: m.CursorCol, Lines: m.Lines}
			case protocol.TerminalEvents:
				return TerminalEventsMsg{RegionID: m.RegionID, Events: m.Events}
			case protocol.RegionCreated:
				return RegionCreatedMsg{RegionID: m.RegionID, Name: m.Name}
			case protocol.RegionDestroyed:
				return RegionDestroyedMsg{RegionID: m.RegionID}
			case protocol.SpawnResponse:
				return SpawnResponseMsg{
					RegionID: m.RegionID, Name: m.Name,
					Error: m.Error, Message: m.Message,
				}
			case protocol.SubscribeResponse:
				return SubscribeResponseMsg{
					RegionID: m.RegionID,
					Error: m.Error, Message: m.Message,
				}
			case protocol.ResizeResponse:
				return ResizeResponseMsg{
					RegionID: m.RegionID,
					Error: m.Error, Message: m.Message,
				}
			case protocol.ListRegionsResponse:
				return ListRegionsResponseMsg{
					Regions: m.Regions,
					Error: m.Error, Message: m.Message,
				}
			default:
				slog.Debug("waitForUpdate: discarding unrecognized message", "type", fmt.Sprintf("%T", m))
				continue
			}
		}
	}
}
