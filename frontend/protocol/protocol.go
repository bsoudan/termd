// Package protocol mirrors the termd wire protocol for the Go frontend.
package protocol

import (
	"encoding/json"
	"fmt"
)

// ── Outbound (frontend → server) ────────────────────────────────────────────

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

// ── Inbound (server → frontend) ─────────────────────────────────────────────

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

// ── Parsing ─────────────────────────────────────────────────────────────────

type envelope struct {
	Type string `json:"type"`
}

// ParseInbound decodes a raw JSON line into the concrete message type.
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
	default:
		return nil, fmt.Errorf("unknown message type: %s", env.Type)
	}
}
