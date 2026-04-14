package protocol

import "encoding/json"

// ── Object tree ──────────────────────────────────────────────────────────────
//
// The server maintains a Tree of structural/metadata state and synchronizes
// it to clients via TreeSnapshot (full state on connect) and TreeEvents
// (incremental patches). Terminal content (screen_update, terminal_events)
// is NOT part of the tree — it uses its own subscribe/stream model.

// Tree is the root of the server state tree.
type Tree struct {
	Server   ServerNode            `json:"server"`
	Sessions map[string]SessionNode `json:"sessions"`
	Stacks   map[string]StackNode   `json:"stacks"`
	Regions  map[string]RegionNode  `json:"regions"`
	Programs map[string]ProgramNode `json:"programs"`
	Clients  map[string]ClientNode  `json:"clients"`
	Upgrade  UpgradeNode            `json:"upgrade"`
}

type ServerNode struct {
	Version    string `json:"version"`
	Hostname   string `json:"hostname"`
	Pid        int    `json:"pid"`
	StartTime  int64  `json:"start_time"`
	SocketPath string `json:"socket_path"`
}

type SessionNode struct {
	Name     string   `json:"name"`
	StackIDs []string `json:"stack_ids"`
}

type StackNode struct {
	ID        string   `json:"id"`
	RegionIDs []string `json:"region_ids"`
	Session   string   `json:"session"`
}

type RegionNode struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Cmd           string `json:"cmd"`
	Pid           int    `json:"pid"`
	Session       string `json:"session"`
	StackID       string `json:"stack_id,omitempty"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Native        bool   `json:"native,omitempty"`
	ScrollbackLen int    `json:"scrollback_len,omitempty"`
}

type ProgramNode struct {
	Name string   `json:"name"`
	Cmd  string   `json:"cmd"`
	Args []string `json:"args,omitempty"`
}

type ClientNode struct {
	ID                 string `json:"id"`
	Hostname           string `json:"hostname,omitempty"`
	Username           string `json:"username,omitempty"`
	Pid                int    `json:"pid,omitempty"`
	Process            string `json:"process,omitempty"`
	Session            string `json:"session,omitempty"`
	SubscribedRegionID string `json:"subscribed_region_id,omitempty"`
}

type UpgradeNode struct {
	Active  bool   `json:"active"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
}

// ── Tree sync messages ───────────────────────────────────────────────────────

// TreeSnapshot is the full tree plus its current version.
// Sent on initial connect and on resync after a version gap.
type TreeSnapshot struct {
	Type    string `json:"type"`
	Version uint64 `json:"version"`
	Tree    Tree   `json:"tree"`
}

// TreeEvents carries incremental mutations to the tree.
// Version is a monotonic counter; if a client detects a gap it
// requests a fresh snapshot via TreeResyncRequest.
type TreeEvents struct {
	Type    string   `json:"type"`
	Version uint64   `json:"version"`
	Ops     []TreeOp `json:"ops"`
}

// TreeOp is a single mutation on the tree. Inspired by JSON Patch (RFC 6902)
// but uses value-based array addressing instead of positional indices.
//
// Operations:
//   - "set"    — create or replace the node at Path
//   - "delete" — remove the node at Path
//   - "add"    — append Value to the array at Path
//   - "remove" — remove the element matching Match from the array at Path
type TreeOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
	Match json.RawMessage `json:"match,omitempty"`
}

// TreeResyncRequest is sent by the client when it detects a version gap
// in tree_events. The server responds with a fresh TreeSnapshot.
type TreeResyncRequest struct {
	Type string `json:"type,omitempty"`
}
