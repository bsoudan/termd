package log

import (
	"encoding/base64"
	"fmt"
	"log/slog"

	"nxtermd/frontend/protocol"
)

// LogProtocolMsg logs a protocol message with structured key=value fields.
func LogProtocolMsg(direction string, msg any) {
	switch m := msg.(type) {
	case protocol.Identify:
		slog.Debug(direction, "type", "identify", "hostname", m.Hostname, "username", m.Username, "pid", m.Pid, "process", m.Process)
	case protocol.SpawnRequest:
		slog.Debug(direction, "type", "spawn_request", "program", m.Program, "session", m.Session)
	case protocol.SpawnResponse:
		slog.Debug(direction, "type", "spawn_response", "region_id", m.RegionID, "name", m.Name, "error", m.Error, "message", m.Message)
	case protocol.SubscribeRequest:
		slog.Debug(direction, "type", "subscribe_request", "region_id", m.RegionID)
	case protocol.SubscribeResponse:
		slog.Debug(direction, "type", "subscribe_response", "region_id", m.RegionID, "error", m.Error, "message", m.Message)
	case protocol.InputMsg:
		decoded, _ := base64.StdEncoding.DecodeString(m.Data)
		slog.Debug(direction, "type", "input", "region_id", m.RegionID, "data", fmt.Sprintf("[%d bytes]", len(decoded)))
	case protocol.ResizeRequest:
		slog.Debug(direction, "type", "resize_request", "region_id", m.RegionID, "width", m.Width, "height", m.Height)
	case protocol.ResizeResponse:
		slog.Debug(direction, "type", "resize_response", "region_id", m.RegionID, "error", m.Error)
	case protocol.ListRegionsRequest:
		slog.Debug(direction, "type", "list_regions_request")
	case protocol.ListRegionsResponse:
		slog.Debug(direction, "type", "list_regions_response", "regions", len(m.Regions), "error", m.Error)
	case protocol.StatusRequest:
		slog.Debug(direction, "type", "status_request")
	case protocol.StatusResponse:
		slog.Debug(direction, "type", "status_response", "pid", m.Pid, "uptime", m.UptimeSeconds, "clients", m.NumClients, "regions", m.NumRegions)
	case protocol.GetScreenRequest:
		slog.Debug(direction, "type", "get_screen_request", "region_id", m.RegionID)
	case protocol.GetScreenResponse:
		slog.Debug(direction, "type", "get_screen_response", "region_id", m.RegionID, "cursor", fmt.Sprintf("(%d,%d)", m.CursorRow, m.CursorCol), "lines", fmt.Sprintf("[%d lines]", len(m.Lines)), "error", m.Error)
	case protocol.ScreenUpdate:
		slog.Debug(direction, "type", "screen_update", "region_id", m.RegionID, "cursor", fmt.Sprintf("(%d,%d)", m.CursorRow, m.CursorCol), "lines", fmt.Sprintf("[%d lines]", len(m.Lines)))
	case protocol.RegionCreated:
		slog.Debug(direction, "type", "region_created", "region_id", m.RegionID, "name", m.Name)
	case protocol.RegionDestroyed:
		slog.Debug(direction, "type", "region_destroyed", "region_id", m.RegionID)
	case protocol.KillRegionRequest:
		slog.Debug(direction, "type", "kill_region_request", "region_id", m.RegionID)
	case protocol.KillRegionResponse:
		slog.Debug(direction, "type", "kill_region_response", "region_id", m.RegionID, "error", m.Error)
	case protocol.ListClientsRequest:
		slog.Debug(direction, "type", "list_clients_request")
	case protocol.ListClientsResponse:
		slog.Debug(direction, "type", "list_clients_response", "clients", len(m.Clients), "error", m.Error)
	case protocol.KillClientRequest:
		slog.Debug(direction, "type", "kill_client_request", "client_id", m.ClientID)
	case protocol.KillClientResponse:
		slog.Debug(direction, "type", "kill_client_response", "client_id", m.ClientID, "error", m.Error)
	case protocol.TerminalEvents:
		slog.Debug(direction, "type", "terminal_events", "region_id", m.RegionID, "events", len(m.Events))
	default:
		slog.Debug(direction, "type", fmt.Sprintf("%T", msg))
	}
}
