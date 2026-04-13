package server

import (
	"encoding/json"
	"maps"
	"os"
	"strconv"

	"nxtermd/internal/config"
	"nxtermd/internal/protocol"
)

// buildTreeFromMaps constructs the initial Tree from the event loop's maps.
// Called once at the start of eventLoop and after a live upgrade restore.
func buildTreeFromMaps(
	regions map[string]Region,
	sessions map[string]*Session,
	programs map[string]config.ProgramConfig,
	version string,
	startTime int64,
	socketPath string,
) protocol.Tree {
	t := protocol.Tree{
		Server: protocol.ServerNode{
			Version:    version,
			Pid:        os.Getpid(),
			StartTime:  startTime,
			SocketPath: socketPath,
		},
		Sessions: make(map[string]protocol.SessionNode, len(sessions)),
		Regions:  make(map[string]protocol.RegionNode, len(regions)),
		Programs: make(map[string]protocol.ProgramNode, len(programs)),
		Clients:  make(map[string]protocol.ClientNode),
	}

	for id, r := range regions {
		t.Regions[id] = regionToNode(r)
	}
	for name, sess := range sessions {
		ids := make([]string, 0, len(sess.regions))
		for rid := range sess.regions {
			ids = append(ids, rid)
		}
		t.Sessions[name] = protocol.SessionNode{Name: name, RegionIDs: ids}
	}
	for name, p := range programs {
		t.Programs[name] = programToNode(name, p)
	}
	return t
}

func regionToNode(r Region) protocol.RegionNode {
	return protocol.RegionNode{
		ID:            r.ID(),
		Name:          r.Name(),
		Cmd:           r.Cmd(),
		Pid:           r.Pid(),
		Session:       r.Session(),
		Width:         r.Width(),
		Height:        r.Height(),
		Native:        r.IsNative(),
		ScrollbackLen: r.ScrollbackLen(),
	}
}

func programToNode(name string, p config.ProgramConfig) protocol.ProgramNode {
	return protocol.ProgramNode{
		Name: name,
		Cmd:  p.Cmd,
		Args: p.Args,
	}
}

// deepCopyTree returns a deep copy of the tree suitable for sending
// as a snapshot. Maps and slices are cloned so the snapshot is
// independent of subsequent mutations.
func deepCopyTree(t protocol.Tree) protocol.Tree {
	cp := protocol.Tree{
		Server:  t.Server,
		Upgrade: t.Upgrade,
	}
	cp.Sessions = make(map[string]protocol.SessionNode, len(t.Sessions))
	for k, v := range t.Sessions {
		ids := make([]string, len(v.RegionIDs))
		copy(ids, v.RegionIDs)
		v.RegionIDs = ids
		cp.Sessions[k] = v
	}
	cp.Regions = make(map[string]protocol.RegionNode, len(t.Regions))
	maps.Copy(cp.Regions, t.Regions)
	cp.Programs = make(map[string]protocol.ProgramNode, len(t.Programs))
	for k, v := range t.Programs {
		args := make([]string, len(v.Args))
		copy(args, v.Args)
		v.Args = args
		cp.Programs[k] = v
	}
	cp.Clients = make(map[string]protocol.ClientNode, len(t.Clients))
	maps.Copy(cp.Clients, t.Clients)
	return cp
}

// ── patchBuilder ─────────────────────────────────────────────────────────────

// patchBuilder accumulates TreeOp entries during a single event loop
// iteration. After the mutation, call broadcastTreeEvents to send them.
type patchBuilder struct {
	ops []protocol.TreeOp
}

func (pb *patchBuilder) Set(path string, value any) {
	raw, _ := json.Marshal(value)
	pb.ops = append(pb.ops, protocol.TreeOp{Op: "set", Path: path, Value: raw})
}

func (pb *patchBuilder) Delete(path string) {
	pb.ops = append(pb.ops, protocol.TreeOp{Op: "delete", Path: path})
}

func (pb *patchBuilder) Add(path string, value any) {
	raw, _ := json.Marshal(value)
	pb.ops = append(pb.ops, protocol.TreeOp{Op: "add", Path: path, Value: raw})
}

func (pb *patchBuilder) Remove(path string, match any) {
	raw, _ := json.Marshal(match)
	pb.ops = append(pb.ops, protocol.TreeOp{Op: "remove", Path: path, Match: raw})
}

func (pb *patchBuilder) empty() bool { return len(pb.ops) == 0 }

// broadcastTreeEvents increments the version and sends a tree_events
// message to all connected clients. Called from inside the event loop
// where the clients map is directly available.
func broadcastTreeEvents(pb *patchBuilder, treeVersion *uint64, clients map[uint32]*Client) {
	if pb.empty() {
		return
	}
	*treeVersion++
	msg := protocol.TreeEvents{
		Type:    "tree_events",
		Version: *treeVersion,
		Ops:     pb.ops,
	}
	for _, c := range clients {
		c.SendMessage(msg)
	}
}

// ── Request types for tree ───────────────────────────────────────────────────

// identifyReq routes an identify message through the event loop so the
// tree's client node can be updated.
type identifyReq struct {
	clientID uint32
	identity clientIdentity
}

func (r identifyReq) handle(st *eventLoopState) {
	cid := strconv.FormatUint(uint64(r.clientID), 10)
	if cn, ok := st.tree.Clients[cid]; ok {
		cn.Hostname = r.identity.hostname
		cn.Username = r.identity.username
		cn.Pid = r.identity.pid
		cn.Process = r.identity.process
		st.tree.Clients[cid] = cn
		var pb patchBuilder
		pb.Set("/clients/"+cid, cn)
		st.broadcastTree(&pb)
	}
}

// treeSnapshotReq requests a deep copy of the current tree + version.
// Used for tree_resync_request handling.
type treeSnapshotReq struct {
	clientID uint32
}

func (r treeSnapshotReq) handle(st *eventLoopState) {
	if c, ok := st.clients[r.clientID]; ok {
		c.SendMessage(protocol.TreeSnapshot{
			Type:    "tree_snapshot",
			Version: st.treeVersion,
			Tree:    deepCopyTree(st.tree),
		})
	}
}

func removeString(ss []string, v string) []string {
	for i, s := range ss {
		if s == v {
			return append(ss[:i], ss[i+1:]...)
		}
	}
	return ss
}
