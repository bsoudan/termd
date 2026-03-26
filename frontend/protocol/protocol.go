package protocol

import (
	"encoding/json"
	"fmt"
)

// ── Outbound (frontend/termctl → server) ────────────────────────────────────

type Identify struct {
	Type     string `json:"type"`
	Hostname string `json:"hostname"`
	Username string `json:"username"`
	Pid      int    `json:"pid"`
	Process  string `json:"process"`
}

type SpawnRequest struct {
	Type string   `json:"type"`
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

type SubscribeRequest struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
}

type InputMsg struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Data     string `json:"data"`
}

type ResizeRequest struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Width    uint16 `json:"width"`
	Height   uint16 `json:"height"`
}

type ListRegionsRequest struct {
	Type string `json:"type"`
}

type StatusRequest struct {
	Type string `json:"type"`
}

type GetScreenRequest struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
}

type KillRegionRequest struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
}

type ListClientsRequest struct {
	Type string `json:"type"`
}

type KillClientRequest struct {
	Type     string `json:"type"`
	ClientID uint32 `json:"client_id"`
}

// ── Inbound (server → frontend/termctl) ─────────────────────────────────────

type SpawnResponse struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Name     string `json:"name"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type SubscribeResponse struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type ResizeResponse struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type RegionCreated struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Name     string `json:"name"`
}

type ScreenUpdate struct {
	Type      string   `json:"type"`
	RegionID  string   `json:"region_id"`
	CursorRow uint16   `json:"cursor_row"`
	CursorCol uint16   `json:"cursor_col"`
	Lines     []string `json:"lines"`
}

type RegionDestroyed struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
}

type RegionInfo struct {
	RegionID string `json:"region_id"`
	Name     string `json:"name"`
	Cmd      string `json:"cmd"`
	Pid      int    `json:"pid"`
}

type ListRegionsResponse struct {
	Type    string       `json:"type"`
	Regions []RegionInfo `json:"regions"`
	Error   bool         `json:"error"`
	Message string       `json:"message"`
}

type StatusResponse struct {
	Type          string `json:"type"`
	Pid           int    `json:"pid"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	SocketPath    string `json:"socket_path"`
	NumClients    int    `json:"num_clients"`
	NumRegions    int    `json:"num_regions"`
	Error         bool   `json:"error"`
	Message       string `json:"message"`
}

type GetScreenResponse struct {
	Type      string   `json:"type"`
	RegionID  string   `json:"region_id"`
	CursorRow uint16   `json:"cursor_row"`
	CursorCol uint16   `json:"cursor_col"`
	Lines     []string `json:"lines"`
	Error     bool     `json:"error"`
	Message   string   `json:"message"`
}

type KillRegionResponse struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type ClientInfoData struct {
	ClientID           uint32 `json:"client_id"`
	Hostname           string `json:"hostname"`
	Username           string `json:"username"`
	Pid                int    `json:"pid"`
	Process            string `json:"process"`
	SubscribedRegionID string `json:"subscribed_region_id"`
}

type ListClientsResponse struct {
	Type    string           `json:"type"`
	Clients []ClientInfoData `json:"clients"`
	Error   bool             `json:"error"`
	Message string           `json:"message"`
}

type KillClientResponse struct {
	Type     string `json:"type"`
	ClientID uint32 `json:"client_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type TerminalEvent struct {
	Op      string `json:"op"`
	Data    string `json:"data,omitempty"`
	Params  []int  `json:"params,omitempty"`
	How     int    `json:"how,omitempty"`
	Attrs   []int  `json:"attrs,omitempty"`
	Private bool   `json:"private,omitempty"`
}

type TerminalEvents struct {
	Type     string          `json:"type"`
	RegionID string          `json:"region_id"`
	Events   []TerminalEvent `json:"events"`
}

// ── Parsing ─────────────────────────────────────────────────────────────────

type envelope struct {
	Type string `json:"type"`
}

func ParseInbound(line []byte) (any, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("parse type tag: %w", err)
	}

	switch env.Type {
	case "spawn_response":
		var msg SpawnResponse
		return msg, json.Unmarshal(line, &msg)
	case "subscribe_response":
		var msg SubscribeResponse
		return msg, json.Unmarshal(line, &msg)
	case "resize_response":
		var msg ResizeResponse
		return msg, json.Unmarshal(line, &msg)
	case "region_created":
		var msg RegionCreated
		return msg, json.Unmarshal(line, &msg)
	case "screen_update":
		var msg ScreenUpdate
		return msg, json.Unmarshal(line, &msg)
	case "region_destroyed":
		var msg RegionDestroyed
		return msg, json.Unmarshal(line, &msg)
	case "list_regions_response":
		var msg ListRegionsResponse
		return msg, json.Unmarshal(line, &msg)
	case "status_response":
		var msg StatusResponse
		return msg, json.Unmarshal(line, &msg)
	case "get_screen_response":
		var msg GetScreenResponse
		return msg, json.Unmarshal(line, &msg)
	case "kill_region_response":
		var msg KillRegionResponse
		return msg, json.Unmarshal(line, &msg)
	case "list_clients_response":
		var msg ListClientsResponse
		return msg, json.Unmarshal(line, &msg)
	case "kill_client_response":
		var msg KillClientResponse
		return msg, json.Unmarshal(line, &msg)
	case "terminal_events":
		var msg TerminalEvents
		return msg, json.Unmarshal(line, &msg)
	default:
		return nil, fmt.Errorf("unknown message type: %s", env.Type)
	}
}
