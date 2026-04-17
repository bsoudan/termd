package protocol

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	Type    string `json:"type,omitempty"`
	Session string `json:"session"`
	Program string `json:"program"`
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
	Width   uint16 `json:"width,omitempty"`
	Height  uint16 `json:"height,omitempty"`
}

type ListSessionsRequest struct {
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

type UpgradeCheckRequest struct {
	Type          string `json:"type,omitempty"`
	ClientVersion string `json:"client_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
}

type ServerUpgradeRequest struct {
	Type string `json:"type,omitempty"`
}

type ClientBinaryRequest struct {
	Type   string `json:"type,omitempty"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Offset int64  `json:"offset"`
}

type ListProgramsRequest struct {
	Type string `json:"type,omitempty"`
}

type AddProgramRequest struct {
	Type string            `json:"type,omitempty"`
	Name string            `json:"name"`
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args"`
	Env  map[string]string `json:"env,omitempty"`
}

type RemoveProgramRequest struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name"`
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
	A    uint8  `json:"a,omitempty"` // bitfield: 1=bold 2=italic 4=underline 8=strikethrough 16=reverse 32=blink 64=conceal 128=faint
}

type ScreenUpdate struct {
	Type          string         `json:"type"`
	RegionID      string         `json:"region_id"`
	CursorRow     uint16         `json:"cursor_row"`
	CursorCol     uint16         `json:"cursor_col"`
	Lines         []string       `json:"lines"`
	Cells         [][]ScreenCell `json:"cells,omitempty"`
	Modes         map[int]bool   `json:"modes,omitempty"`
	Title         string         `json:"title,omitempty"`
	IconName      string         `json:"icon_name,omitempty"`
	ScrollbackLen int            `json:"scrollback_len,omitempty"`
}

type RegionDestroyed struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type RegionInfo struct {
	RegionID       string `json:"region_id"`
	Name           string `json:"name"`
	Cmd            string `json:"cmd"`
	Pid            int    `json:"pid"`
	Session        string `json:"session"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	ScrollbackLen  int    `json:"scrollback_len,omitempty"`
	Native         bool   `json:"native,omitempty"`
}

type ListRegionsResponse struct {
	Type    string       `json:"type"`
	Regions []RegionInfo `json:"regions"`
	Error   bool         `json:"error"`
	Message string       `json:"message"`
}

type GetScreenResponse struct {
	Type          string         `json:"type"`
	RegionID      string         `json:"region_id"`
	CursorRow     uint16         `json:"cursor_row"`
	CursorCol     uint16         `json:"cursor_col"`
	Lines         []string       `json:"lines"`
	Cells         [][]ScreenCell `json:"cells,omitempty"`
	Modes         map[int]bool   `json:"modes,omitempty"`
	Title         string         `json:"title,omitempty"`
	IconName      string         `json:"icon_name,omitempty"`
	ScrollbackLen int            `json:"scrollback_len,omitempty"`
	Error         bool           `json:"error"`
	Message       string         `json:"message"`
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
	Total    int            `json:"total"`
	Done     bool           `json:"done"`
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
	Type     string        `json:"type"`
	Session  string        `json:"session"`
	Regions  []RegionInfo  `json:"regions"`
	Programs []ProgramInfo `json:"programs"`
	Error    bool          `json:"error"`
	Message  string        `json:"message"`
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

type ProgramInfo struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type ListProgramsResponse struct {
	Type     string        `json:"type"`
	Programs []ProgramInfo `json:"programs"`
	Error    bool          `json:"error"`
	Message  string        `json:"message"`
}

type AddProgramResponse struct {
	Type    string `json:"type,omitempty"`
	Name    string `json:"name"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

type RemoveProgramResponse struct {
	Type    string `json:"type,omitempty"`
	Name    string `json:"name"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

type UpgradeCheckResponse struct {
	Type             string `json:"type,omitempty"`
	ServerAvailable  bool   `json:"server_available"`
	ServerVersion    string `json:"server_version,omitempty"`
	ClientAvailable  bool   `json:"client_available"`
	ClientVersion    string `json:"client_version,omitempty"`
	Error            bool   `json:"error"`
	Message          string `json:"message"`
}

type ServerUpgradeResponse struct {
	Type    string `json:"type,omitempty"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

// Upgrade phases, in the order they occur during a successful upgrade.
// The server sets these on the tree's UpgradeNode so connected clients
// can track progress via tree events. UpgradePhaseFailed is set instead
// of the later phases when the handoff is rolled back.
const (
	UpgradePhaseStarting         = "starting"
	UpgradePhaseSpawned          = "spawned"
	UpgradePhaseSentListenerFDs  = "sent_listener_fds"
	UpgradePhaseStoppedAccepting = "stopped_accepting"
	UpgradePhaseDrained          = "drained"
	UpgradePhaseStoppedReadLoops = "stopped_readloops"
	UpgradePhaseSentState        = "sent_state"
	UpgradePhaseSentPTYFDs       = "sent_pty_fds"
	UpgradePhaseReady            = "ready"
	UpgradePhaseShuttingDown     = "shutting_down"
	UpgradePhaseFailed           = "failed"
)

type ClientBinaryChunk struct {
	Type   string `json:"type,omitempty"`
	Offset int64  `json:"offset"`
	Data   string `json:"data"`
	Final  bool   `json:"final"`
}

type ClientBinaryResponse struct {
	Type    string `json:"type,omitempty"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

type Warning struct {
	Type     string `json:"type"`
	WarnType string `json:"warn_type"`
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

// ── Overlay protocol ────────────────────────────────────────────────────────

type OverlayRegisterRequest struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type OverlayRegisterResponse struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type OverlayRender struct {
	Type      string         `json:"type,omitempty"`
	RegionID  string         `json:"region_id"`
	Cells     [][]ScreenCell `json:"cells"`
	CursorRow uint16         `json:"cursor_row"`
	CursorCol uint16         `json:"cursor_col"`
	Modes     map[int]bool   `json:"modes,omitempty"`
}

type OverlayClear struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
}

type OverlayInput struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Data     string `json:"data"`
}

// ── Native region protocol ──────────────────────────────────────────────────
//
// A native region is a Region whose backend is a connected protocol client
// (the "driver") rather than a PTY + child process. The driver spawns the
// region, streams output via NativeRegionOutput, and receives subscriber
// input as NativeInput messages. When the driver disconnects, the server
// destroys its native regions.

type NativeRegionSpawnRequest struct {
	Type    string `json:"type,omitempty"`
	Session string `json:"session"`
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type NativeRegionSpawnResponse struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

// NativeRegionOutput carries bytes from the driver into the region's VT
// parser. Fire-and-forget; ordering with other driver messages is preserved
// by the connection's FIFO.
type NativeRegionOutput struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Data     string `json:"data"` // base64
}

// NativeInput delivers subscriber input bytes to the driver. Server → driver.
type NativeInput struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	Data     string `json:"data"` // base64
}

// NativeRegionSync tells the server to emit a sync marker into the
// terminal_events stream for this region, ordered behind any pending
// output the driver has already sent. Subscribers see the sync marker
// as a TerminalEvent with Op="sync" and Data=<id>. Markers emitted
// before any subscriber joins are delivered to each new subscriber
// along with the initial snapshot, so drivers can sync without having
// to wait for subscription.
type NativeRegionSync struct {
	Type     string `json:"type,omitempty"`
	RegionID string `json:"region_id"`
	ID       string `json:"id"`
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

func parseAs[T any](data []byte) (any, error) {
	var msg T
	return msg, json.Unmarshal(data, &msg)
}

var payloadParsers = map[string]func([]byte) (any, error){
	"identify":                  parseAs[Identify],
	"spawn_response":            parseAs[SpawnResponse],
	"subscribe_response":        parseAs[SubscribeResponse],
	"resize_response":           parseAs[ResizeResponse],
	"region_created":            parseAs[RegionCreated],
	"screen_update":             parseAs[ScreenUpdate],
	"region_destroyed":          parseAs[RegionDestroyed],
	"list_regions_response":     parseAs[ListRegionsResponse],
	"get_screen_response":       parseAs[GetScreenResponse],
	"kill_region_response":      parseAs[KillRegionResponse],
	"list_clients_response":     parseAs[ListClientsResponse],
	"kill_client_response":      parseAs[KillClientResponse],
	"get_scrollback_response":   parseAs[GetScrollbackResponse],
	"terminal_events":           parseAs[TerminalEvents],
	"unsubscribe_response":      parseAs[UnsubscribeResponse],
	"session_connect_response":  parseAs[SessionConnectResponse],
	"list_sessions_response":    parseAs[ListSessionsResponse],
	"list_programs_response":    parseAs[ListProgramsResponse],
	"add_program_response":      parseAs[AddProgramResponse],
	"remove_program_response":   parseAs[RemoveProgramResponse],
	"upgrade_check_response":    parseAs[UpgradeCheckResponse],
	"server_upgrade_response":   parseAs[ServerUpgradeResponse],
	"client_binary_chunk":       parseAs[ClientBinaryChunk],
	"client_binary_response":    parseAs[ClientBinaryResponse],
	"overlay_register_response":    parseAs[OverlayRegisterResponse],
	"overlay_input":                parseAs[OverlayInput],
	"native_region_spawn_request":  parseAs[NativeRegionSpawnRequest],
	"native_region_spawn_response": parseAs[NativeRegionSpawnResponse],
	"native_region_output":         parseAs[NativeRegionOutput],
	"native_region_sync":           parseAs[NativeRegionSync],
	"native_input":                 parseAs[NativeInput],
	"tree_snapshot":             parseAs[TreeSnapshot],
	"tree_events":               parseAs[TreeEvents],
	"tree_resync_request":       parseAs[TreeResyncRequest],
}

func parsePayload(typ string, line []byte) (any, error) {
	parser, ok := payloadParsers[typ]
	if !ok {
		return nil, fmt.Errorf("unknown message type: %s", typ)
	}
	return parser(line)
}

// UnwrapTagged returns the inner Msg if msg is a tagged wrapper,
// or nil if it is not. Used by the protocol logger to print the
// payload type instead of "protocol.tagged".
func UnwrapTagged(msg any) any {
	if t, ok := msg.(tagged); ok {
		return t.Msg
	}
	return nil
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

var typeTagMap = map[reflect.Type]string{
	reflect.TypeOf(Identify{}):              "identify",
	reflect.TypeOf(SpawnRequest{}):           "spawn_request",
	reflect.TypeOf(SubscribeRequest{}):       "subscribe_request",
	reflect.TypeOf(InputMsg{}):               "input",
	reflect.TypeOf(ResizeRequest{}):          "resize_request",
	reflect.TypeOf(ListRegionsRequest{}):     "list_regions_request",
	reflect.TypeOf(GetScreenRequest{}):       "get_screen_request",
	reflect.TypeOf(KillRegionRequest{}):      "kill_region_request",
	reflect.TypeOf(ListClientsRequest{}):     "list_clients_request",
	reflect.TypeOf(KillClientRequest{}):      "kill_client_request",
	reflect.TypeOf(GetScrollbackRequest{}):   "get_scrollback_request",
	reflect.TypeOf(UnsubscribeRequest{}):     "unsubscribe_request",
	reflect.TypeOf(SessionConnectRequest{}):  "session_connect_request",
	reflect.TypeOf(ListSessionsRequest{}):    "list_sessions_request",
	reflect.TypeOf(ListProgramsRequest{}):    "list_programs_request",
	reflect.TypeOf(AddProgramRequest{}):      "add_program_request",
	reflect.TypeOf(RemoveProgramRequest{}):   "remove_program_request",
	reflect.TypeOf(UpgradeCheckRequest{}):    "upgrade_check_request",
	reflect.TypeOf(ServerUpgradeRequest{}):   "server_upgrade_request",
	reflect.TypeOf(ClientBinaryRequest{}):    "client_binary_request",
	reflect.TypeOf(Disconnect{}):             "disconnect",
	reflect.TypeOf(OverlayRegisterRequest{}):   "overlay_register",
	reflect.TypeOf(OverlayRender{}):            "overlay_render",
	reflect.TypeOf(OverlayClear{}):             "overlay_clear",
	reflect.TypeOf(NativeRegionSpawnRequest{}): "native_region_spawn_request",
	reflect.TypeOf(NativeRegionOutput{}):       "native_region_output",
	reflect.TypeOf(NativeRegionSync{}):         "native_region_sync",
	reflect.TypeOf(TreeSnapshot{}):           "tree_snapshot",
	reflect.TypeOf(TreeEvents{}):             "tree_events",
	reflect.TypeOf(TreeResyncRequest{}):      "tree_resync_request",
}

func typeTag(msg any) string {
	if tag, ok := typeTagMap[reflect.TypeOf(msg)]; ok {
		return tag
	}
	return ""
}
