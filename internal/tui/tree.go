package tui

import (
	"encoding/json"
	"log/slog"
	"strings"

	"nxtermd/internal/protocol"
)

// TreeChangedMsg is dispatched through the layer stack after the local
// tree is updated (from either a snapshot or incremental events).
// Layers re-derive their state from Tree.
type TreeChangedMsg struct {
	Tree protocol.Tree
}

// TreeStore maintains the client's local copy of the server state tree.
// All access happens on the bubbletea goroutine — no locking needed.
type TreeStore struct {
	tree    protocol.Tree
	version uint64
	valid   bool // false until first snapshot received
}

// Tree returns the current tree. Only valid after HandleSnapshot.
func (ts *TreeStore) Tree() protocol.Tree { return ts.tree }
func (ts *TreeStore) Valid() bool          { return ts.valid }

// HandleSnapshot replaces the local tree with a full snapshot.
func (ts *TreeStore) HandleSnapshot(msg protocol.TreeSnapshot) {
	ts.tree = msg.Tree
	ts.version = msg.Version
	ts.valid = true
	slog.Debug("tree: snapshot applied", "version", ts.version)
}

// HandleEvents applies incremental tree operations. Returns false if
// a version gap is detected (caller should request a resync).
func (ts *TreeStore) HandleEvents(msg protocol.TreeEvents) bool {
	if !ts.valid {
		slog.Debug("tree: events before snapshot, ignoring", "version", msg.Version)
		return false
	}
	if msg.Version != ts.version+1 {
		slog.Debug("tree: version gap", "have", ts.version, "got", msg.Version)
		return false
	}
	for _, op := range msg.Ops {
		ts.applyOp(op)
	}
	ts.version = msg.Version
	return true
}

func (ts *TreeStore) applyOp(op protocol.TreeOp) {
	segs := splitPath(op.Path)
	if len(segs) == 0 {
		return
	}
	switch segs[0] {
	case "regions":
		ts.applyRegionOp(segs[1:], op)
	case "sessions":
		ts.applySessionOp(segs[1:], op)
	case "programs":
		ts.applyProgramOp(segs[1:], op)
	case "clients":
		ts.applyClientOp(segs[1:], op)
	case "upgrade":
		ts.applyUpgradeOp(op)
	case "server":
		ts.applyServerOp(op)
	}
}

func (ts *TreeStore) applyRegionOp(segs []string, op protocol.TreeOp) {
	if len(segs) != 1 {
		return
	}
	id := segs[0]
	switch op.Op {
	case "set":
		var node protocol.RegionNode
		if json.Unmarshal(op.Value, &node) == nil {
			if ts.tree.Regions == nil {
				ts.tree.Regions = make(map[string]protocol.RegionNode)
			}
			ts.tree.Regions[id] = node
		}
	case "delete":
		delete(ts.tree.Regions, id)
	}
}

func (ts *TreeStore) applySessionOp(segs []string, op protocol.TreeOp) {
	if len(segs) == 1 {
		name := segs[0]
		switch op.Op {
		case "set":
			var node protocol.SessionNode
			if json.Unmarshal(op.Value, &node) == nil {
				if ts.tree.Sessions == nil {
					ts.tree.Sessions = make(map[string]protocol.SessionNode)
				}
				ts.tree.Sessions[name] = node
			}
		case "delete":
			delete(ts.tree.Sessions, name)
		}
		return
	}
	if len(segs) == 2 && segs[1] == "region_ids" {
		name := segs[0]
		snode, ok := ts.tree.Sessions[name]
		if !ok {
			return
		}
		switch op.Op {
		case "add":
			var val string
			if json.Unmarshal(op.Value, &val) == nil {
				snode.RegionIDs = append(snode.RegionIDs, val)
				ts.tree.Sessions[name] = snode
			}
		case "remove":
			var match string
			if json.Unmarshal(op.Match, &match) == nil {
				for i, s := range snode.RegionIDs {
					if s == match {
						snode.RegionIDs = append(snode.RegionIDs[:i], snode.RegionIDs[i+1:]...)
						break
					}
				}
				ts.tree.Sessions[name] = snode
			}
		}
	}
}

func (ts *TreeStore) applyProgramOp(segs []string, op protocol.TreeOp) {
	if len(segs) != 1 {
		return
	}
	name := segs[0]
	switch op.Op {
	case "set":
		var node protocol.ProgramNode
		if json.Unmarshal(op.Value, &node) == nil {
			if ts.tree.Programs == nil {
				ts.tree.Programs = make(map[string]protocol.ProgramNode)
			}
			ts.tree.Programs[name] = node
		}
	case "delete":
		delete(ts.tree.Programs, name)
	}
}

func (ts *TreeStore) applyClientOp(segs []string, op protocol.TreeOp) {
	if len(segs) != 1 {
		return
	}
	id := segs[0]
	switch op.Op {
	case "set":
		var node protocol.ClientNode
		if json.Unmarshal(op.Value, &node) == nil {
			if ts.tree.Clients == nil {
				ts.tree.Clients = make(map[string]protocol.ClientNode)
			}
			ts.tree.Clients[id] = node
		}
	case "delete":
		delete(ts.tree.Clients, id)
	}
}

func (ts *TreeStore) applyUpgradeOp(op protocol.TreeOp) {
	if op.Op == "set" {
		var node protocol.UpgradeNode
		if json.Unmarshal(op.Value, &node) == nil {
			ts.tree.Upgrade = node
		}
	}
}

func (ts *TreeStore) applyServerOp(op protocol.TreeOp) {
	if op.Op == "set" {
		var node protocol.ServerNode
		if json.Unmarshal(op.Value, &node) == nil {
			ts.tree.Server = node
		}
	}
}

func splitPath(path string) []string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}
