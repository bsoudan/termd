package ui

import (
	"time"

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

type ScrollbackResponseMsg struct {
	Lines [][]protocol.ScreenCell
}

type ServerIdentifyMsg struct {
	Hostname string
}

type ServerErrorMsg struct {
	Context string
	Message string
}

type DisconnectedMsg struct {
	RetryAt time.Time
}
type ReconnectedMsg struct{}


