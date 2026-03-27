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
	Cells     [][]protocol.ScreenCell
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

type ServerIdentifyMsg struct {
	Hostname string
}

type ServerErrorMsg struct {
	Context string
	Message string
}

type DisconnectedMsg struct{}
type ReconnectedMsg struct{}

// convertProtocolMsg converts a protocol-layer message to the corresponding
// tea.Msg. Returns nil for unrecognized types.
func convertProtocolMsg(msg any) tea.Msg {
	switch m := msg.(type) {
	case protocol.Identify:
		return ServerIdentifyMsg{Hostname: m.Hostname}
	case protocol.ScreenUpdate:
		return ScreenUpdateMsg{RegionID: m.RegionID, CursorRow: m.CursorRow, CursorCol: m.CursorCol, Lines: m.Lines, Cells: m.Cells}
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
	case protocol.StatusResponse:
		return m
	case client.DisconnectedMsg:
		return DisconnectedMsg{}
	case client.ReconnectedMsg:
		return ReconnectedMsg{}
	default:
		slog.Debug("convertProtocolMsg: unrecognized message", "type", fmt.Sprintf("%T", m))
		return nil
	}
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
			if teaMsg := convertProtocolMsg(msg); teaMsg != nil {
				return teaMsg
			}
		}
	}
}
