package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"termd/config"
	"termd/frontend/protocol"
)

type Server struct {
	version      string
	binariesDir  string
	listeners    []net.Listener
	startTime    time.Time
	nextClientID atomic.Uint32
	sessionsCfg  config.SessionsConfig

	requests     chan any
	done         chan struct{}
	shutdownResp chan shutdownResult
	shutdown     atomic.Bool

	// init* fields transfer ownership of maps to the event loop goroutine.
	// They are set in NewServer and consumed (nilled) by eventLoop on startup.
	initRegions  map[string]Region
	initClients  map[uint32]*Client
	initSessions map[string]*Session
	initPrograms map[string]config.ProgramConfig
}

func NewServer(listeners []net.Listener, version string, cfg config.ServerConfig) *Server {
	for _, ln := range listeners {
		slog.Info("listening", "addr", ln.Addr().String())
	}

	sessionsCfg := cfg.Sessions
	if sessionsCfg.DefaultName == "" {
		sessionsCfg.DefaultName = "main"
	}

	programs := make(map[string]config.ProgramConfig)
	for _, p := range cfg.Programs {
		programs[p.Name] = p
	}
	if len(programs) == 0 {
		programs["default"] = config.ProgramConfig{
			Name: "default",
			Cmd:  serverShell(),
		}
	}

	s := &Server{
		version:      version,
		binariesDir:  cfg.Upgrade.BinariesDir,
		listeners:    listeners,
		startTime:    time.Now(),
		sessionsCfg:  sessionsCfg,
		requests:     make(chan any, 256),
		done:         make(chan struct{}),
		shutdownResp: make(chan shutdownResult, 1),
		initRegions:  make(map[string]Region),
		initClients:  make(map[uint32]*Client),
		initSessions: make(map[string]*Session),
		initPrograms: programs,
	}
	s.nextClientID.Store(1)

	go s.eventLoop()

	return s
}

// send sends a request to the event loop, returning false if the server
// is shutting down. Callers that need a response should check the return
// value before reading from their response channel.
func (s *Server) send(req any) bool {
	select {
	case s.requests <- req:
		return true
	case <-s.done:
		return false
	}
}

func (s *Server) Run() {
	var wg sync.WaitGroup
	for _, ln := range s.listeners {
		wg.Add(1)
		go func(ln net.Listener) {
			defer wg.Done()
			s.acceptLoop(ln)
		}(ln)
	}
	wg.Wait()
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.shutdown.Load() {
				return
			}
			slog.Debug("accept error", "err", err)
			continue
		}
		s.acceptClient(conn)
	}
}

// SetUnlinkOnClose prevents Unix socket listeners from removing their
// socket files when closed. Call before Shutdown after a live upgrade
// so the new process's socket remains reachable.
func (s *Server) SetUnlinkOnClose(unlink bool) {
	for _, ln := range s.listeners {
		if ul, ok := ln.(interface{ SetUnlinkOnClose(bool) }); ok {
			ul.SetUnlinkOnClose(unlink)
		}
	}
}

func (s *Server) Shutdown() {
	if !s.shutdown.CompareAndSwap(false, true) {
		return
	}
	for _, ln := range s.listeners {
		ln.Close()
	}

	close(s.done)
	result := <-s.shutdownResp

	for _, c := range result.clients {
		c.Close()
	}
	for _, r := range result.regions {
		r.Close()
	}
}

func (s *Server) acceptClient(conn net.Conn) {
	id := s.nextClientID.Add(1) - 1
	client := NewClient(conn, s, id)

	resp := make(chan struct{}, 1)
	if !s.send(addClientReq{client: client, resp: resp}) {
		client.Close()
		return
	}
	<-resp

	slog.Debug("client connected", "id", id)
	client.sendIdentify()
	go client.ReadLoop()
}

func (s *Server) SpawnRegion(sessionName, cmd string, args []string, env map[string]string) (Region, error) {
	region, err := NewRegion(cmd, args, env, 80, 24, s.socketAddr())
	if err != nil {
		return nil, err
	}

	resp := make(chan struct{}, 1)
	if !s.send(spawnRegionReq{region: region, sessionName: sessionName, resp: resp}) {
		region.Close()
		return nil, fmt.Errorf("server shutting down")
	}
	<-resp

	slog.Info("spawned region", "region_id", region.ID(), "cmd", cmd, "session", sessionName)

	go s.watchRegion(region)
	return region, nil
}

func (s *Server) watchRegion(region Region) {
	for range region.Notify() {
		s.sendTerminalEvents(region)
	}
	<-region.ReaderDone()
	s.sendTerminalEvents(region)
	s.destroyRegion(region.ID())
}

func (s *Server) destroyRegion(regionID string) {
	resp := make(chan destroyResult, 1)
	if !s.send(destroyRegionReq{regionID: regionID, resp: resp}) {
		return
	}
	result := <-resp

	if !result.found {
		return
	}

	for _, c := range result.subscribers {
		c.SendMessage(protocol.RegionDestroyed{
			Type:     "region_destroyed",
			RegionID: regionID,
		})
	}

	slog.Info("destroyed region", "region_id", regionID, "session", result.region.Session())
	result.region.Close()
}

func (s *Server) FindRegion(regionID string) Region {
	resp := make(chan Region, 1)
	if !s.send(findRegionReq{regionID: regionID, resp: resp}) {
		return nil
	}
	return <-resp
}

func (s *Server) Broadcast(msg any) {
	resp := make(chan []*Client, 1)
	if !s.send(getClientsReq{resp: resp}) {
		return
	}
	for _, c := range <-resp {
		c.SendMessage(msg)
	}
}

func (s *Server) KillRegion(regionID string) bool {
	resp := make(chan Region, 1)
	if !s.send(killRegionReq{regionID: regionID, resp: resp}) {
		return false
	}
	region := <-resp
	if region == nil {
		return false
	}
	region.Kill()
	return true
}

func (s *Server) KillClient(clientID uint32) bool {
	resp := make(chan *Client, 1)
	if !s.send(killClientReq{clientID: clientID, resp: resp}) {
		return false
	}
	client := <-resp
	if client == nil {
		return false
	}
	client.Close()
	return true
}

func (s *Server) removeClient(id uint32) {
	s.send(removeClientReq{clientID: id})
}

func (s *Server) sendTerminalEvents(region Region) {
	events, needsSnapshot := region.FlushEvents()

	if !needsSnapshot && len(events) == 0 {
		return
	}

	resp := make(chan subscribersData, 1)
	if !s.send(getSubscribersReq{regionID: region.ID(), resp: resp}) {
		return
	}
	data := <-resp

	if len(data.clients) == 0 {
		return
	}

	// When an overlay is active, always send a composited snapshot.
	if data.overlay != nil {
		snap := region.Snapshot()
		composited := compositeSnapshot(snap, data.overlay)
		snapMsg := protocol.ScreenUpdate{
			Type:      "screen_update",
			RegionID:  region.ID(),
			CursorRow: composited.CursorRow,
			CursorCol: composited.CursorCol,
			Lines:     composited.Lines,
			Cells:     composited.Cells,
			Modes:     composited.Modes,
		}
		for _, c := range data.clients {
			c.SendMessage(snapMsg)
		}
		return
	}

	if needsSnapshot {
		snap := region.Snapshot()
		snapMsg := protocol.ScreenUpdate{
			Type:      "screen_update",
			RegionID:  region.ID(),
			CursorRow: snap.CursorRow,
			CursorCol: snap.CursorCol,
			Lines:     snap.Lines,
			Cells:     snap.Cells,
			Modes:     snap.Modes,
		}
		for _, c := range data.clients {
			c.SendMessage(snapMsg)
		}
		return
	}

	msg := protocol.TerminalEvents{
		Type:     "terminal_events",
		RegionID: region.ID(),
		Events:   events,
	}

	for _, c := range data.clients {
		c.SendMessage(msg)
	}
}

// compositeSnapshot overlays cells from an overlay on top of a base snapshot.
// Non-empty overlay cells replace base cells. The overlay's cursor and modes
// take precedence.
func compositeSnapshot(base Snapshot, ov *overlayState) Snapshot {
	result := Snapshot{
		CursorRow: ov.cursorRow,
		CursorCol: ov.cursorCol,
	}

	// Deep copy base cells.
	result.Cells = make([][]protocol.ScreenCell, len(base.Cells))
	for r := range base.Cells {
		result.Cells[r] = make([]protocol.ScreenCell, len(base.Cells[r]))
		copy(result.Cells[r], base.Cells[r])
	}

	// Overlay cells on top.
	for r := 0; r < len(ov.cells) && r < len(result.Cells); r++ {
		for c := 0; c < len(ov.cells[r]) && c < len(result.Cells[r]); c++ {
			oc := ov.cells[r][c]
			if oc.Char != "" && oc.Char != "\x00" {
				result.Cells[r][c] = oc
			}
		}
	}

	// Rebuild text lines from composited cells.
	result.Lines = make([]string, len(result.Cells))
	for r, row := range result.Cells {
		var b strings.Builder
		for _, cell := range row {
			if cell.Char == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(cell.Char)
			}
		}
		result.Lines[r] = b.String()
	}

	// Overlay modes take precedence when set.
	if len(ov.modes) > 0 {
		result.Modes = make(map[int]bool, len(ov.modes))
		for k, v := range ov.modes {
			result.Modes[k] = v
		}
	} else {
		result.Modes = base.Modes
	}

	return result
}

func (s *Server) getStatus() protocol.StatusResponse {
	resp := make(chan statusCounts, 1)
	if !s.send(getStatusReq{resp: resp}) {
		return protocol.StatusResponse{Type: "status_response", Error: true, Message: "server shutting down"}
	}
	counts := <-resp

	regions := s.getRegionInfos("")

	hostname, _ := os.Hostname()
	return protocol.StatusResponse{
		Type:          "status_response",
		Hostname:      hostname,
		Version:       s.version,
		Pid:           os.Getpid(),
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
		SocketPath:    s.listenerAddrs(),
		NumClients:    counts.numClients,
		NumRegions:    counts.numRegions,
		NumSessions:   counts.numSessions,
		Regions:       regions,
		Error:         false,
		Message:       "",
	}
}

// socketAddr returns the address of the first listener, for passing to
// child processes as TERMD_SOCKET.
func (s *Server) socketAddr() string {
	if len(s.listeners) > 0 {
		return s.listeners[0].Addr().String()
	}
	return ""
}

func (s *Server) listenerAddrs() string {
	addrs := make([]string, len(s.listeners))
	for i, ln := range s.listeners {
		addrs[i] = ln.Addr().String()
	}
	return strings.Join(addrs, ", ")
}

// SpawnProgram looks up a program by name and spawns it into the given session.
func (s *Server) SpawnProgram(sessionName, programName string) (Region, error) {
	resp := make(chan *config.ProgramConfig, 1)
	if !s.send(lookupProgramReq{name: programName, resp: resp}) {
		return nil, fmt.Errorf("server shutting down")
	}
	prog := <-resp
	if prog == nil {
		return nil, fmt.Errorf("unknown program: %s", programName)
	}
	return s.SpawnRegion(sessionName, prog.Cmd, prog.Args, prog.Env)
}

// findOrCreateSession returns an existing session's regions or spawns
// default programs into a new session and returns the resulting regions.
func (s *Server) findOrCreateSession(name string) (*Session, []protocol.RegionInfo, error) {
	if name == "" {
		name = s.sessionsCfg.DefaultName
	}

	resp := make(chan sessionConnectResult, 1)
	if !s.send(sessionConnectReq{name: name, resp: resp}) {
		return nil, nil, fmt.Errorf("server shutting down")
	}
	result := <-resp

	if result.exists {
		// Return a Session value for the caller (just needs the name).
		return &Session{name: name}, result.regionInfos, nil
	}

	// Session doesn't exist yet — spawn the programs.
	var infos []protocol.RegionInfo
	for _, prog := range result.programConfigs {
		region, err := s.SpawnRegion(name, prog.Cmd, prog.Args, prog.Env)
		if err != nil {
			return nil, nil, err
		}
		infos = append(infos, protocol.RegionInfo{
			RegionID: region.ID(),
			Name:     region.Name(),
			Cmd:      region.Cmd(),
			Pid:      region.Pid(),
			Session:  name,
		})
	}

	return &Session{name: name}, infos, nil
}

func (s *Server) listProgramInfos() []protocol.ProgramInfo {
	resp := make(chan []protocol.ProgramInfo, 1)
	if !s.send(listProgramsReq{resp: resp}) {
		return nil
	}
	return <-resp
}

func (s *Server) addProgram(p config.ProgramConfig) error {
	resp := make(chan error, 1)
	if !s.send(addProgramReq{prog: p, resp: resp}) {
		return fmt.Errorf("server shutting down")
	}
	return <-resp
}

func (s *Server) removeProgram(name string) error {
	resp := make(chan error, 1)
	if !s.send(removeProgramReq{name: name, resp: resp}) {
		return fmt.Errorf("server shutting down")
	}
	return <-resp
}

func (s *Server) getRegionInfos(sessionFilter string) []protocol.RegionInfo {
	resp := make(chan []protocol.RegionInfo, 1)
	if !s.send(getRegionInfosReq{session: sessionFilter, resp: resp}) {
		return nil
	}
	return <-resp
}

func (s *Server) getClientInfos() []protocol.ClientInfoData {
	resp := make(chan []protocol.ClientInfoData, 1)
	if !s.send(getClientInfosReq{resp: resp}) {
		return nil
	}
	return <-resp
}

func (s *Server) getSessionInfos() []protocol.SessionInfo {
	resp := make(chan []protocol.SessionInfo, 1)
	if !s.send(getSessionInfosReq{resp: resp}) {
		return nil
	}
	return <-resp
}

func (s *Server) Subscribe(clientID uint32, regionID string) (Region, Snapshot) {
	resp := make(chan *subscribeResult, 1)
	if !s.send(subscribeReq{clientID: clientID, regionID: regionID, resp: resp}) {
		return nil, Snapshot{}
	}
	result := <-resp
	if result == nil {
		return nil, Snapshot{}
	}
	return result.region, result.snapshot
}

func (s *Server) GetOverlay(regionID string) *overlayState {
	resp := make(chan *overlayState, 1)
	if !s.send(getOverlayReq{regionID: regionID, resp: resp}) {
		return nil
	}
	return <-resp
}

func (s *Server) Unsubscribe(clientID uint32) {
	s.send(unsubscribeReq{clientID: clientID})
}

func (s *Server) SetClientSession(clientID uint32, sessionName string) {
	s.send(setClientSessionReq{clientID: clientID, sessionName: sessionName})
}

func (s *Server) GetClientSession(clientID uint32) string {
	resp := make(chan string, 1)
	if !s.send(getClientSessionReq{clientID: clientID, resp: resp}) {
		return ""
	}
	return <-resp
}

func serverShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "sh"
}
