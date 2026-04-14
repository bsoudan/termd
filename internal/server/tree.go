package server

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"

	"nxtermd/internal/config"
	"nxtermd/internal/protocol"
)

// ServerTree is the single source of truth for the server's object graph.
// It holds both live objects (Region, *Client) and their protocol nodes.
// Typed mutation methods automatically track TreeOps during a transaction.
// The event loop wraps each request handler in StartTx/CommitTx so
// handlers never need to build patches or broadcast manually.
type ServerTree struct {
	server   protocol.ServerNode
	regions  map[string]regionEntry
	stacks   map[string]stackEntry
	sessions map[string]sessionEntry
	programs map[string]programEntry
	clients  map[string]clientEntry
	upgrade  protocol.UpgradeNode

	version uint64
	ops     []protocol.TreeOp
	inTx    bool
}

type regionEntry struct {
	region Region
	node   protocol.RegionNode
}

type stackEntry struct {
	regionIDs []string
	node      protocol.StackNode
}

type sessionEntry struct {
	stackIDs []string
	node     protocol.SessionNode
}

type programEntry struct {
	config config.ProgramConfig
	node   protocol.ProgramNode
}

type clientEntry struct {
	client *Client
	node   protocol.ClientNode
}

func NewServerTree(version string, hostname string, startTime int64, socketPath string) *ServerTree {
	return &ServerTree{
		server: protocol.ServerNode{
			Version:    version,
			Hostname:   hostname,
			Pid:        os.Getpid(),
			StartTime:  startTime,
			SocketPath: socketPath,
		},
		regions:  make(map[string]regionEntry),
		stacks:   make(map[string]stackEntry),
		sessions: make(map[string]sessionEntry),
		programs: make(map[string]programEntry),
		clients:  make(map[string]clientEntry),
	}
}

// ── Transaction ──────────────────────────────────────────────────────────────

func (t *ServerTree) StartTx() {
	t.inTx = true
	t.ops = t.ops[:0]
}

// CommitTx ends the transaction and returns the version and accumulated ops.
// Returns 0, nil if no mutations occurred.
func (t *ServerTree) CommitTx() (uint64, []protocol.TreeOp) {
	t.inTx = false
	if len(t.ops) == 0 {
		return 0, nil
	}
	t.version++
	ops := make([]protocol.TreeOp, len(t.ops))
	copy(ops, t.ops)
	t.ops = t.ops[:0]
	return t.version, ops
}

func (t *ServerTree) emit(op protocol.TreeOp) {
	if t.inTx {
		t.ops = append(t.ops, op)
	}
}

func (t *ServerTree) emitSet(path string, value any) {
	raw, _ := json.Marshal(value)
	t.emit(protocol.TreeOp{Op: "set", Path: path, Value: raw})
}

func (t *ServerTree) emitDelete(path string) {
	t.emit(protocol.TreeOp{Op: "delete", Path: path})
}

func (t *ServerTree) emitAdd(path string, value any) {
	raw, _ := json.Marshal(value)
	t.emit(protocol.TreeOp{Op: "add", Path: path, Value: raw})
}

func (t *ServerTree) emitRemove(path string, match any) {
	raw, _ := json.Marshal(match)
	t.emit(protocol.TreeOp{Op: "remove", Path: path, Match: raw})
}

// ── Region mutations ─────────────────────────────────────────────────────────

func (t *ServerTree) SetRegion(r Region) {
	node := protocol.RegionNode{
		ID: r.ID(), Name: r.Name(), Cmd: r.Cmd(), Pid: r.Pid(),
		Session: r.Session(), Width: r.Width(), Height: r.Height(),
		Native: r.IsNative(), ScrollbackLen: r.ScrollbackLen(),
	}
	// Preserve StackID if the entry already exists (set by SetRegionStackID).
	if existing, ok := t.regions[r.ID()]; ok {
		node.StackID = existing.node.StackID
	}
	t.regions[r.ID()] = regionEntry{region: r, node: node}
	t.emitSet("/regions/"+r.ID(), node)
}

func (t *ServerTree) SetRegionStackID(regionID, stackID string) {
	e, ok := t.regions[regionID]
	if !ok {
		return
	}
	e.node.StackID = stackID
	t.regions[regionID] = e
	t.emitSet("/regions/"+regionID, e.node)
}

func (t *ServerTree) RegionStackID(regionID string) string {
	if e, ok := t.regions[regionID]; ok {
		return e.node.StackID
	}
	return ""
}

func (t *ServerTree) DeleteRegion(id string) {
	delete(t.regions, id)
	t.emitDelete("/regions/" + id)
}

// ── Stack mutations ─────────────────────────────────────────────────────────

func (t *ServerTree) SetStack(id, session string) {
	node := protocol.StackNode{ID: id, RegionIDs: []string{}, Session: session}
	t.stacks[id] = stackEntry{node: node}
	t.emitSet("/stacks/"+id, node)
}

func (t *ServerTree) DeleteStack(id string) {
	delete(t.stacks, id)
	t.emitDelete("/stacks/" + id)
}

func (t *ServerTree) AddRegionToStack(stackID, regionID string) {
	e, ok := t.stacks[stackID]
	if !ok {
		return
	}
	e.regionIDs = append(e.regionIDs, regionID)
	e.node.RegionIDs = e.regionIDs
	t.stacks[stackID] = e
	t.emitAdd("/stacks/"+stackID+"/region_ids", regionID)
}

func (t *ServerTree) RemoveRegionFromStack(stackID, regionID string) {
	e, ok := t.stacks[stackID]
	if !ok {
		return
	}
	e.regionIDs = removeString(e.regionIDs, regionID)
	e.node.RegionIDs = e.regionIDs
	t.stacks[stackID] = e
	t.emitRemove("/stacks/"+stackID+"/region_ids", regionID)
}

func (t *ServerTree) StackRegionIDs(stackID string) ([]string, bool) {
	if e, ok := t.stacks[stackID]; ok {
		return e.regionIDs, true
	}
	return nil, false
}

// ── Session mutations ────────────────────────────────────────────────────────

// EnsureSession creates the session if it doesn't exist. Returns true if created.
func (t *ServerTree) EnsureSession(name string) bool {
	if _, ok := t.sessions[name]; ok {
		return false
	}
	node := protocol.SessionNode{Name: name, StackIDs: []string{}}
	t.sessions[name] = sessionEntry{node: node}
	t.emitSet("/sessions/"+name, node)
	return true
}

func (t *ServerTree) DeleteSession(name string) {
	delete(t.sessions, name)
	t.emitDelete("/sessions/" + name)
}

func (t *ServerTree) AddStackToSession(session, stackID string) {
	e, ok := t.sessions[session]
	if !ok {
		return
	}
	e.stackIDs = append(e.stackIDs, stackID)
	e.node.StackIDs = e.stackIDs
	t.sessions[session] = e
	t.emitAdd("/sessions/"+session+"/stack_ids", stackID)
}

func (t *ServerTree) RemoveStackFromSession(session, stackID string) {
	e, ok := t.sessions[session]
	if !ok {
		return
	}
	e.stackIDs = removeString(e.stackIDs, stackID)
	e.node.StackIDs = e.stackIDs
	t.sessions[session] = e
	t.emitRemove("/sessions/"+session+"/stack_ids", stackID)
}

// SessionRegionIDs resolves all stacks in a session to a flat list of region IDs.
func (t *ServerTree) SessionRegionIDs(name string) ([]string, bool) {
	e, ok := t.sessions[name]
	if !ok {
		return nil, false
	}
	var ids []string
	for _, stackID := range e.stackIDs {
		if se, ok := t.stacks[stackID]; ok {
			ids = append(ids, se.regionIDs...)
		}
	}
	return ids, true
}

func (t *ServerTree) SessionStackIDs(name string) ([]string, bool) {
	if e, ok := t.sessions[name]; ok {
		return e.stackIDs, true
	}
	return nil, false
}

// ── Program mutations ────────────────────────────────────────────────────────

func (t *ServerTree) SetProgram(p config.ProgramConfig) {
	node := protocol.ProgramNode{Name: p.Name, Cmd: p.Cmd, Args: p.Args}
	t.programs[p.Name] = programEntry{config: p, node: node}
	t.emitSet("/programs/"+p.Name, node)
}

func (t *ServerTree) DeleteProgram(name string) {
	delete(t.programs, name)
	t.emitDelete("/programs/" + name)
}

// ── Client mutations ─────────────────────────────────────────────────────────

func (t *ServerTree) AddClient(id uint32, c *Client) {
	cid := clientIDStr(id)
	node := protocol.ClientNode{ID: cid}
	t.clients[cid] = clientEntry{client: c, node: node}
	t.emitSet("/clients/"+cid, node)
}

func (t *ServerTree) DeleteClient(id uint32) {
	cid := clientIDStr(id)
	delete(t.clients, cid)
	t.emitDelete("/clients/" + cid)
}

func (t *ServerTree) SetClientIdentity(id uint32, ident clientIdentity) {
	cid := clientIDStr(id)
	e, ok := t.clients[cid]
	if !ok {
		return
	}
	e.node.Hostname = ident.hostname
	e.node.Username = ident.username
	e.node.Pid = ident.pid
	e.node.Process = ident.process
	t.clients[cid] = e
	t.emitSet("/clients/"+cid, e.node)
}

func (t *ServerTree) SetClientSubscription(id uint32, regionID string) {
	cid := clientIDStr(id)
	e, ok := t.clients[cid]
	if !ok {
		return
	}
	e.node.SubscribedRegionID = regionID
	t.clients[cid] = e
	t.emitSet("/clients/"+cid, e.node)
}

func (t *ServerTree) SetClientSession(id uint32, sessionName string) {
	cid := clientIDStr(id)
	e, ok := t.clients[cid]
	if !ok {
		return
	}
	e.node.Session = sessionName
	t.clients[cid] = e
	t.emitSet("/clients/"+cid, e.node)
}

// ── Upgrade ──────────────────────────────────────────────────────────────────

func (t *ServerTree) SetUpgrade(u protocol.UpgradeNode) {
	t.upgrade = u
	t.emitSet("/upgrade", u)
}

// ── Queries ──────────────────────────────────────────────────────────────────

func (t *ServerTree) Region(id string) Region {
	if e, ok := t.regions[id]; ok {
		return e.region
	}
	return nil
}

func (t *ServerTree) Client(id uint32) *Client {
	if e, ok := t.clients[clientIDStr(id)]; ok {
		return e.client
	}
	return nil
}

func (t *ServerTree) ClientSession(id uint32) string {
	if e, ok := t.clients[clientIDStr(id)]; ok {
		return e.node.Session
	}
	return ""
}

func (t *ServerTree) Program(name string) *config.ProgramConfig {
	if e, ok := t.programs[name]; ok {
		cfg := e.config
		return &cfg
	}
	return nil
}

func (t *ServerTree) NumClients() int  { return len(t.clients) }
func (t *ServerTree) NumRegions() int  { return len(t.regions) }
func (t *ServerTree) NumSessions() int { return len(t.sessions) }

func (t *ServerTree) SessionNames() []string {
	names := make([]string, 0, len(t.sessions))
	for name := range t.sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (t *ServerTree) ForEachClient(fn func(uint32, *Client)) {
	for _, e := range t.clients {
		fn(e.client.id, e.client)
	}
}

func (t *ServerTree) ForEachRegion(fn func(string, Region)) {
	for id, e := range t.regions {
		fn(id, e.region)
	}
}

func (t *ServerTree) ForEachSession(fn func(string, []string)) {
	for name := range t.sessions {
		ids, _ := t.SessionRegionIDs(name)
		fn(name, ids)
	}
}

func (t *ServerTree) ForEachProgram(fn func(string, config.ProgramConfig)) {
	for name, e := range t.programs {
		fn(name, e.config)
	}
}

// ClientMap returns a snapshot of all clients keyed by ID.
func (t *ServerTree) ClientMap() map[uint32]*Client {
	m := make(map[uint32]*Client, len(t.clients))
	for _, e := range t.clients {
		m[e.client.id] = e.client
	}
	return m
}

func (t *ServerTree) Version() uint64 { return t.version }

// Snapshot returns a deep-copy TreeSnapshot for sending to a client.
func (t *ServerTree) Snapshot() protocol.TreeSnapshot {
	tree := protocol.Tree{
		Server:   t.server,
		Upgrade:  t.upgrade,
		Sessions: make(map[string]protocol.SessionNode, len(t.sessions)),
		Stacks:   make(map[string]protocol.StackNode, len(t.stacks)),
		Regions:  make(map[string]protocol.RegionNode, len(t.regions)),
		Programs: make(map[string]protocol.ProgramNode, len(t.programs)),
		Clients:  make(map[string]protocol.ClientNode, len(t.clients)),
	}
	for k, v := range t.sessions {
		node := v.node
		ids := make([]string, len(node.StackIDs))
		copy(ids, node.StackIDs)
		node.StackIDs = ids
		tree.Sessions[k] = node
	}
	for k, v := range t.stacks {
		node := v.node
		ids := make([]string, len(node.RegionIDs))
		copy(ids, node.RegionIDs)
		node.RegionIDs = ids
		tree.Stacks[k] = node
	}
	for k, v := range t.regions {
		tree.Regions[k] = v.node
	}
	for k, v := range t.programs {
		node := v.node
		if len(node.Args) > 0 {
			args := make([]string, len(node.Args))
			copy(args, node.Args)
			node.Args = args
		}
		tree.Programs[k] = node
	}
	for k, v := range t.clients {
		tree.Clients[k] = v.node
	}
	return protocol.TreeSnapshot{
		Type:    "tree_snapshot",
		Version: t.version,
		Tree:    tree,
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func clientIDStr(id uint32) string {
	return strconv.FormatUint(uint64(id), 10)
}

func removeString(ss []string, v string) []string {
	for i, s := range ss {
		if s == v {
			return append(ss[:i], ss[i+1:]...)
		}
	}
	return ss
}
