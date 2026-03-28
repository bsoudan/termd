package protocol

import (
	"encoding/json"
	"fmt"
)

// ── Outbound (frontend/termctl → server) ────────────────────────────────────

type Identify struct {
	Type     string `json:"type,omitempty"`
	Hostname string `json:"hostname"`
	Username string `json:"username"`
	Pid      int    `json:"pid"`
	Process  string `json:"process"`
}

type SpawnRequest struct {
	Type    string   `json:"type,omitempty"`
	Session string   `json:"session"`
	Cmd     string   `json:"cmd"`
	Args    []string `json:"args"`
}

type SubscribeRequest struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type InputMsg struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Data     string `json:"data"`
}

type ResizeRequest struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Width    uint16 `json:"width"`
	Height   uint16 `json:"height"`
}

type ListRegionsRequest struct {
	Type    string `json:"type,omitempty"`
	Session string `json:"session,omitempty"`
}

type SessionConnectRequest struct {
	Type    string `json:"type,omitempty"`
	Session string `json:"session,omitempty"`
}

type ListSessionsRequest struct {
	Type string `json:"type,omitempty"`
}

type StatusRequest struct {
	Type string `json:"type,omitempty"`
}

type GetScreenRequest struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type KillRegionRequest struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type ListClientsRequest struct {
	Type string `json:"type,omitempty"`
}

type KillClientRequest struct {
	Type     string `json:"type,omitempty"`
	ClientID uint32 `json:"client_id"`
}

type GetScrollbackRequest struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type UnsubscribeRequest struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type Disconnect struct {
	Type string `json:"type,omitempty"`
}

// ── Inbound (server → frontend/termctl) ─────────────────────────────────────

type SpawnResponse struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Name     string `json:"name"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type SubscribeResponse struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type ResizeResponse struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type RegionCreated struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Name     string `json:"name"`
	Session  string `json:"session"`
}

// ScreenCell carries per-cell color and attribute data for initial sync.
type ScreenCell struct {
	Char string `json:"c,omitempty"`
	Fg   string `json:"fg,omitempty"` // SGR color: "31", "38;5;208", "38;2;R;G;B", or "" for default
	Bg   string `json:"bg,omitempty"`
	A    uint8  `json:"a,omitempty"` // bitfield: 1=bold 2=italic 4=underline 8=strikethrough 16=reverse 32=blink 64=conceal
}

type ScreenUpdate struct {
	Type      string         `json:"type"`
	RegionID  string         `json:"region_id"`
	CursorRow uint16         `json:"cursor_row"`
	CursorCol uint16         `json:"cursor_col"`
	Lines     []string       `json:"lines"`
	Cells     [][]ScreenCell `json:"cells,omitempty"`
}

type RegionDestroyed struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type RegionInfo struct {
	RegionID string `json:"region_id"`
	Name     string `json:"name"`
	Cmd      string `json:"cmd"`
	Pid      int    `json:"pid"`
	Session  string `json:"session"`
}

type ListRegionsResponse struct {
	Type    string       `json:"type"`
	Regions []RegionInfo `json:"regions"`
	Error   bool         `json:"error"`
	Message string       `json:"message"`
}

type StatusResponse struct {
	Type          string `json:"type"`
	Hostname      string `json:"hostname"`
	Version       string `json:"version"`
	Pid           int    `json:"pid"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	SocketPath    string `json:"socket_path"`
	NumClients    int    `json:"num_clients"`
	NumRegions    int    `json:"num_regions"`
	NumSessions   int    `json:"num_sessions"`
	Error         bool   `json:"error"`
	Message       string `json:"message"`
}

type GetScreenResponse struct {
	Type      string         `json:"type"`
	RegionID  string         `json:"region_id"`
	CursorRow uint16         `json:"cursor_row"`
	CursorCol uint16         `json:"cursor_col"`
	Lines     []string       `json:"lines"`
	Cells     [][]ScreenCell `json:"cells,omitempty"`
	Error     bool           `json:"error"`
	Message   string         `json:"message"`
}

type KillRegionResponse struct {
	Type     string `json:"type,omitempty"`
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
	Session            string `json:"session"`
	SubscribedRegionID string `json:"subscribed_region_id"`
}

type ListClientsResponse struct {
	Type    string           `json:"type"`
	Clients []ClientInfoData `json:"clients"`
	Error   bool             `json:"error"`
	Message string           `json:"message"`
}

type KillClientResponse struct {
	Type     string `json:"type,omitempty"`
	ClientID uint32 `json:"client_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type GetScrollbackResponse struct {
	Type     string         `json:"type"`
	RegionID string         `json:"region_id"`
	Lines    [][]ScreenCell `json:"lines"`
	Error    bool           `json:"error"`
	Message  string         `json:"message"`
}

type UnsubscribeResponse struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type SessionConnectResponse struct {
	Type    string       `json:"type"`
	Session string       `json:"session"`
	Regions []RegionInfo `json:"regions"`
	Error   bool         `json:"error"`
	Message string       `json:"message"`
}

type SessionInfo struct {
	Name       string `json:"name"`
	NumRegions int    `json:"num_regions"`
}

type ListSessionsResponse struct {
	Type     string        `json:"type"`
	Sessions []SessionInfo `json:"sessions"`
	Error    bool          `json:"error"`
	Message  string        `json:"message"`
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
	Type  string `json:"type,omitempty"`
	ReqID uint64 `json:"req_id,omitempty"`
}

// Message wraps a parsed protocol message with its envelope metadata.
type Message struct {
	ReqID   uint64
	Payload any
}

func ParseInbound(line []byte) (Message, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Message{}, fmt.Errorf("parse type tag: %w", err)
	}
	payload, err := parsePayload(env.Type, line)
	if err != nil {
		return Message{}, err
	}
	return Message{ReqID: env.ReqID, Payload: payload}, nil
}

func parsePayload(typ string, line []byte) (any, error) {
	switch typ {
	case "identify":
		var msg Identify
		return msg, json.Unmarshal(line, &msg)
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
	case "get_scrollback_response":
		var msg GetScrollbackResponse
		return msg, json.Unmarshal(line, &msg)
	case "terminal_events":
		var msg TerminalEvents
		return msg, json.Unmarshal(line, &msg)
	case "unsubscribe_response":
		var msg UnsubscribeResponse
		return msg, json.Unmarshal(line, &msg)
	case "session_connect_response":
		var msg SessionConnectResponse
		return msg, json.Unmarshal(line, &msg)
	case "list_sessions_response":
		var msg ListSessionsResponse
		return msg, json.Unmarshal(line, &msg)
	default:
		return nil, fmt.Errorf("unknown message type: %s", typ)
	}
}

// tagged wraps a message with its type tag for JSON marshaling.
// The Type field is set automatically so callers don't need to.
type tagged struct {
	Type  string `json:"type"`
	ReqID uint64 `json:"req_id,omitempty"`
	Msg   any    `json:"-"`
}

func (t tagged) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(t.Msg)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(data)+len(t.Type)+30)
	result = append(result, `{"type":"`...)
	result = append(result, t.Type...)
	result = append(result, '"')
	if t.ReqID > 0 {
		result = append(result, `,"req_id":`...)
		result = fmt.Appendf(result, "%d", t.ReqID)
	}
	if len(data) > 2 {
		result = append(result, ',')
		result = append(result, data[1:]...)
	} else {
		result = append(result, '}')
	}
	return result, nil
}

// Tagged wraps a protocol message with its type tag for JSON marshaling.
func Tagged(msg any) any {
	tag := typeTag(msg)
	if tag == "" {
		return msg
	}
	return tagged{Type: tag, Msg: msg}
}

// TaggedWithReqID wraps a protocol message with its type tag and request ID.
func TaggedWithReqID(msg any, reqID uint64) any {
	tag := typeTag(msg)
	if tag == "" {
		return msg
	}
	return tagged{Type: tag, ReqID: reqID, Msg: msg}
}

func typeTag(msg any) string {
	switch msg.(type) {
	case Identify:
		return "identify"
	case SpawnRequest:
		return "spawn_request"
	case SubscribeRequest:
		return "subscribe_request"
	case InputMsg:
		return "input"
	case ResizeRequest:
		return "resize_request"
	case ListRegionsRequest:
		return "list_regions_request"
	case StatusRequest:
		return "status_request"
	case GetScreenRequest:
		return "get_screen_request"
	case KillRegionRequest:
		return "kill_region_request"
	case ListClientsRequest:
		return "list_clients_request"
	case KillClientRequest:
		return "kill_client_request"
	case GetScrollbackRequest:
		return "get_scrollback_request"
	case UnsubscribeRequest:
		return "unsubscribe_request"
	case SessionConnectRequest:
		return "session_connect_request"
	case ListSessionsRequest:
		return "list_sessions_request"
	case Disconnect:
		return "disconnect"
	default:
		return ""
	}
}
