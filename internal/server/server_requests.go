package server

import (
	"fmt"
	"log/slog"
	"os"

	"nxtermd/internal/config"
	"nxtermd/internal/protocol"
)

// request is the interface all event loop requests must implement.
type request interface {
	handle(st *eventLoopState)
}

// eventLoopState holds all mutable state owned by the event loop.
// The tree is the single source of truth for regions, sessions,
// programs, and clients. The auxiliary maps track server-internal
// bookkeeping that clients don't need to see.
type eventLoopState struct {
	srv            *Server
	tree           *ServerTree
	subscriptions  map[uint32]string // clientID → regionID
	clientOverlays map[uint32]string // clientID → regionID
	regionOverlays map[string]uint32 // regionID → overlay clientID
	exit           bool
}

// commitAndBroadcast ends the current transaction and broadcasts
// any accumulated tree ops to all connected clients.
func (st *eventLoopState) commitAndBroadcast() {
	v, ops := st.tree.CommitTx()
	if len(ops) == 0 {
		return
	}
	msg := protocol.TreeEvents{
		Type:    "tree_events",
		Version: v,
		Ops:     ops,
	}
	st.tree.ForEachClient(func(_ uint32, c *Client) {
		c.SendMessage(msg)
	})
}

func (st *eventLoopState) notifySessionsChanged() {
	fn, _ := st.srv.sessionsChanged.Load().(func([]string))
	if fn == nil {
		return
	}
	go fn(st.tree.SessionNames())
}

// ── Request types ────────────────────────────────────────────────────────────

type addClientReq struct {
	client *Client
	resp   chan struct{}
}

type removeClientReq struct {
	clientID uint32
}

type spawnRegionReq struct {
	region      Region
	sessionName string
	resp        chan struct{}
}

type destroyRegionReq struct {
	regionID string
	resp     chan destroyResult
}

type destroyResult struct {
	region Region
	found  bool
}

type findRegionReq struct {
	regionID string
	resp     chan Region
}

type killRegionReq struct {
	regionID string
	resp     chan Region
}

type killClientReq struct {
	clientID uint32
	resp     chan *Client
}

type lookupProgramReq struct {
	name string
	resp chan *config.ProgramConfig
}

type sessionConnectResult struct {
	exists         bool
	regionInfos    []protocol.RegionInfo
	programConfigs []config.ProgramConfig
}

type sessionConnectReq struct {
	name   string
	width  uint16
	height uint16
	resp   chan sessionConnectResult
}

type listProgramsReq struct {
	resp chan []protocol.ProgramInfo
}

type addProgramReq struct {
	prog config.ProgramConfig
	resp chan error
}

type removeProgramReq struct {
	name string
	resp chan error
}

type getRegionInfosReq struct {
	session string
	resp    chan []protocol.RegionInfo
}

type getClientInfosReq struct {
	resp chan []protocol.ClientInfoData
}

type getSessionInfosReq struct {
	resp chan []protocol.SessionInfo
}

type subscribeResult struct {
	region   Region
	snapshot Snapshot
}

type subscribeReq struct {
	clientID uint32
	regionID string
	resp     chan *subscribeResult
}

type unsubscribeReq struct {
	clientID uint32
}

type setClientSessionReq struct {
	clientID    uint32
	sessionName string
}

type getClientSessionReq struct {
	clientID uint32
	resp     chan string
}

type identifyReq struct {
	clientID uint32
	identity clientIdentity
}

type treeSnapshotReq struct {
	clientID uint32
}

type shutdownResult struct {
	clients []*Client
	regions []Region
}

// ── Overlay types ────────────────────────────────────────────────────────────

type overlayState struct {
	clientID  uint32
	regionID  string
	cells     [][]protocol.ScreenCell
	cursorRow uint16
	cursorCol uint16
	modes     map[int]bool
}

type overlayRegisterReq struct {
	client   *Client
	regionID string
	resp     chan overlayRegisterResult
}

type overlayRegisterResult struct {
	width  int
	height int
	err    string
}

type overlayClearReq struct {
	clientID uint32
	regionID string
}

type inputRouteResult struct {
	region        Region
	overlayClient *Client
}

type inputRouteReq struct {
	regionID string
	resp     chan inputRouteResult
}

// ── Event loop ───────────────────────────────────────────────────────────────

func (s *Server) eventLoop() {
	hostname, _ := os.Hostname()
	tree := NewServerTree(s.version, hostname, s.startTime.Unix(), s.listenerAddrs())

	// Populate programs from config (no tx — no clients yet).
	for _, p := range s.initPrograms {
		tree.SetProgram(p)
	}
	s.initPrograms = nil

	st := &eventLoopState{
		srv:            s,
		tree:           tree,
		subscriptions:  make(map[uint32]string),
		clientOverlays: make(map[uint32]string),
		regionOverlays: make(map[string]uint32),
	}

	for {
		select {
		case req := <-s.requests:
			st.tree.StartTx()
			req.handle(st)
			st.commitAndBroadcast()
			if st.exit {
				return
			}
		case <-s.done:
			var clients []*Client
			st.tree.ForEachClient(func(_ uint32, c *Client) {
				clients = append(clients, c)
			})
			var regions []Region
			st.tree.ForEachRegion(func(_ string, r Region) {
				regions = append(regions, r)
			})
			s.shutdownResp <- shutdownResult{clients: clients, regions: regions}
			return
		}
	}
}

// ── Request handlers ─────────────────────────────────────────────────────────

func (r addClientReq) handle(st *eventLoopState) {
	// Deliver identify + snapshot BEFORE adding to the tree. The
	// snapshot captures the pre-add state at version V. After the
	// handler returns, commitAndBroadcast sends tree_events at
	// version V+1 with the "client added" patch — normal sequential
	// versioning, no special transaction management needed.
	r.client.sendIdentify()
	r.client.SendMessage(st.tree.Snapshot())
	st.tree.AddClient(r.client.id, r.client)
	r.resp <- struct{}{}
}

func (r removeClientReq) handle(st *eventLoopState) {
	if rid, ok := st.subscriptions[r.clientID]; ok {
		delete(st.subscriptions, r.clientID)
		if region := st.tree.Region(rid); region != nil {
			region.RemoveSubscriber(r.clientID)
		}
	}
	if rid, ok := st.clientOverlays[r.clientID]; ok {
		if region := st.tree.Region(rid); region != nil {
			region.ClearOverlay(r.clientID)
		}
		delete(st.clientOverlays, r.clientID)
		delete(st.regionOverlays, rid)
	}
	// Destroy any native regions owned by this client (driver). Each
	// region's DriverDisconnected pushes childExitedMsg onto the actor,
	// which triggers destroyRegion via the normal path.
	var orphaned []*NativeRegion
	st.tree.ForEachRegion(func(_ string, region Region) {
		if nr, ok := region.(*NativeRegion); ok && nr.Driver().id == r.clientID {
			orphaned = append(orphaned, nr)
		}
	})
	st.tree.DeleteClient(r.clientID)
	slog.Debug("client disconnected", "id", r.clientID, "native_regions_owned", len(orphaned))
	for _, nr := range orphaned {
		nr.DriverDisconnected()
	}
}

func (r spawnRegionReq) handle(st *eventLoopState) {
	r.region.SetSession(r.sessionName)
	st.tree.SetRegion(r.region)
	created := st.tree.EnsureSession(r.sessionName)
	st.tree.AddRegionToSession(r.sessionName, r.region.ID())
	r.resp <- struct{}{}
	if created {
		st.notifySessionsChanged()
	}
}

func (r destroyRegionReq) handle(st *eventLoopState) {
	region := st.tree.Region(r.regionID)
	if region == nil {
		r.resp <- destroyResult{found: false}
		return
	}
	sessionName := region.Session()
	st.tree.DeleteRegion(r.regionID)
	st.tree.RemoveRegionFromSession(sessionName, r.regionID)
	sessionRemoved := false
	if ids, ok := st.tree.SessionRegionIDs(sessionName); ok && len(ids) == 0 {
		st.tree.DeleteSession(sessionName)
		sessionRemoved = true
		slog.Info("removed empty session", "session", sessionName)
	}
	if overlayClientID, ok := st.regionOverlays[r.regionID]; ok {
		delete(st.regionOverlays, r.regionID)
		delete(st.clientOverlays, overlayClientID)
	}
	for clientID, rid := range st.subscriptions {
		if rid == r.regionID {
			delete(st.subscriptions, clientID)
		}
	}
	r.resp <- destroyResult{region: region, found: true}
	if sessionRemoved {
		st.notifySessionsChanged()
	}
}

func (r findRegionReq) handle(st *eventLoopState) {
	r.resp <- st.tree.Region(r.regionID)
}

func (r killRegionReq) handle(st *eventLoopState) {
	r.resp <- st.tree.Region(r.regionID)
}

func (r killClientReq) handle(st *eventLoopState) {
	r.resp <- st.tree.Client(r.clientID)
}

func (r lookupProgramReq) handle(st *eventLoopState) {
	r.resp <- st.tree.Program(r.name)
}

func (r sessionConnectReq) handle(st *eventLoopState) {
	name := r.name
	if name == "" {
		name = st.srv.sessionsCfg.DefaultName
	}
	if regionIDs, ok := st.tree.SessionRegionIDs(name); ok {
		infos := make([]protocol.RegionInfo, 0, len(regionIDs))
		for _, id := range regionIDs {
			reg := st.tree.Region(id)
			if reg == nil {
				continue
			}
			infos = append(infos, protocol.RegionInfo{
				RegionID: reg.ID(), Name: reg.Name(), Cmd: reg.Cmd(),
				Pid: reg.Pid(), Session: name,
			})
		}
		r.resp <- sessionConnectResult{exists: true, regionInfos: infos}
		return
	}
	programNames := st.srv.sessionsCfg.DefaultPrograms
	if len(programNames) == 0 {
		if st.tree.Program("default") != nil {
			programNames = []string{"default"}
		} else {
			st.tree.ForEachProgram(func(pname string, _ config.ProgramConfig) {
				if len(programNames) == 0 {
					programNames = []string{pname}
				}
			})
		}
	}
	var configs []config.ProgramConfig
	for _, pname := range programNames {
		if p := st.tree.Program(pname); p != nil {
			configs = append(configs, *p)
		}
	}
	r.resp <- sessionConnectResult{exists: false, programConfigs: configs}
}

func (r listProgramsReq) handle(st *eventLoopState) {
	var infos []protocol.ProgramInfo
	st.tree.ForEachProgram(func(_ string, p config.ProgramConfig) {
		infos = append(infos, protocol.ProgramInfo{Name: p.Name, Cmd: p.Cmd})
	})
	r.resp <- infos
}

func (r addProgramReq) handle(st *eventLoopState) {
	if st.tree.Program(r.prog.Name) != nil {
		r.resp <- fmt.Errorf("program %q already exists", r.prog.Name)
	} else {
		st.tree.SetProgram(r.prog)
		r.resp <- nil
	}
}

func (r removeProgramReq) handle(st *eventLoopState) {
	if st.tree.Program(r.name) == nil {
		r.resp <- fmt.Errorf("program %q not found", r.name)
	} else {
		st.tree.DeleteProgram(r.name)
		r.resp <- nil
	}
}

func (r getRegionInfosReq) handle(st *eventLoopState) {
	if r.session != "" {
		regionIDs, ok := st.tree.SessionRegionIDs(r.session)
		if !ok {
			r.resp <- nil
			return
		}
		infos := make([]protocol.RegionInfo, 0, len(regionIDs))
		for _, id := range regionIDs {
			reg := st.tree.Region(id)
			if reg == nil {
				continue
			}
			infos = append(infos, protocol.RegionInfo{
				RegionID: reg.ID(), Name: reg.Name(), Cmd: reg.Cmd(),
				Pid: reg.Pid(), Session: r.session,
				Width: reg.Width(), Height: reg.Height(),
				ScrollbackLen: reg.ScrollbackLen(), Native: reg.IsNative(),
			})
		}
		r.resp <- infos
		return
	}
	var infos []protocol.RegionInfo
	st.tree.ForEachRegion(func(_ string, reg Region) {
		infos = append(infos, protocol.RegionInfo{
			RegionID: reg.ID(), Name: reg.Name(), Cmd: reg.Cmd(),
			Pid: reg.Pid(), Session: reg.Session(),
			Width: reg.Width(), Height: reg.Height(),
			ScrollbackLen: reg.ScrollbackLen(), Native: reg.IsNative(),
		})
	})
	r.resp <- infos
}

func (r getClientInfosReq) handle(st *eventLoopState) {
	var infos []protocol.ClientInfoData
	st.tree.ForEachClient(func(_ uint32, c *Client) {
		infos = append(infos, protocol.ClientInfoData{
			ClientID:           c.id,
			Hostname:           c.GetHostname(),
			Username:           c.GetUsername(),
			Pid:                c.GetPid(),
			Process:            c.GetProcess(),
			Session:            st.tree.ClientSession(c.id),
			SubscribedRegionID: st.subscriptions[c.id],
		})
	})
	r.resp <- infos
}

func (r subscribeReq) handle(st *eventLoopState) {
	region := st.tree.Region(r.regionID)
	if region == nil {
		r.resp <- nil
		return
	}
	if prev, ok := st.subscriptions[r.clientID]; ok && prev != r.regionID {
		if prevRegion := st.tree.Region(prev); prevRegion != nil {
			prevRegion.RemoveSubscriber(r.clientID)
		}
	}
	client := st.tree.Client(r.clientID)
	if client == nil {
		r.resp <- nil
		return
	}
	snap := region.AddSubscriber(client)
	st.subscriptions[r.clientID] = r.regionID
	st.tree.SetClientSubscription(r.clientID, r.regionID)
	r.resp <- &subscribeResult{region: region, snapshot: snap}
}

func (r unsubscribeReq) handle(st *eventLoopState) {
	if rid, ok := st.subscriptions[r.clientID]; ok {
		delete(st.subscriptions, r.clientID)
		if region := st.tree.Region(rid); region != nil {
			region.RemoveSubscriber(r.clientID)
		}
	}
	st.tree.SetClientSubscription(r.clientID, "")
}

func (r setClientSessionReq) handle(st *eventLoopState) {
	st.tree.SetClientSession(r.clientID, r.sessionName)
}

func (r getClientSessionReq) handle(st *eventLoopState) {
	r.resp <- st.tree.ClientSession(r.clientID)
}

func (r identifyReq) handle(st *eventLoopState) {
	st.tree.SetClientIdentity(r.clientID, r.identity)
}

func (r treeSnapshotReq) handle(st *eventLoopState) {
	if c := st.tree.Client(r.clientID); c != nil {
		c.SendMessage(st.tree.Snapshot())
	}
}

func (r getSessionInfosReq) handle(st *eventLoopState) {
	var infos []protocol.SessionInfo
	st.tree.ForEachSession(func(name string, regionIDs []string) {
		infos = append(infos, protocol.SessionInfo{
			Name:       name,
			NumRegions: len(regionIDs),
		})
	})
	r.resp <- infos
}

// ── Overlay handlers ─────────────────────────────────────────────────────────

func (r overlayRegisterReq) handle(st *eventLoopState) {
	region := st.tree.Region(r.regionID)
	if region == nil {
		r.resp <- overlayRegisterResult{err: "region not found"}
		return
	}
	result := region.RegisterOverlay(r.client)
	if result.err == "" {
		st.clientOverlays[r.client.id] = r.regionID
		st.regionOverlays[r.regionID] = r.client.id
	}
	r.resp <- result
}

func (r overlayClearReq) handle(st *eventLoopState) {
	if ownerID, ok := st.regionOverlays[r.regionID]; !ok || ownerID != r.clientID {
		return
	}
	if region := st.tree.Region(r.regionID); region != nil {
		region.ClearOverlay(r.clientID)
	}
	delete(st.regionOverlays, r.regionID)
	delete(st.clientOverlays, r.clientID)
}

func (r inputRouteReq) handle(st *eventLoopState) {
	region := st.tree.Region(r.regionID)
	if region == nil {
		r.resp <- inputRouteResult{}
		return
	}
	if overlayClientID, ok := st.regionOverlays[r.regionID]; ok {
		if c := st.tree.Client(overlayClientID); c != nil {
			r.resp <- inputRouteResult{overlayClient: c}
			return
		}
	}
	r.resp <- inputRouteResult{region: region}
}
