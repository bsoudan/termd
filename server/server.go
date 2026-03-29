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
	initRegions  map[string]*Region
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
		listeners:    listeners,
		startTime:    time.Now(),
		sessionsCfg:  sessionsCfg,
		requests:     make(chan any, 256),
		done:         make(chan struct{}),
		shutdownResp: make(chan shutdownResult, 1),
		initRegions:  make(map[string]*Region),
		initClients:  make(map[uint32]*Client),
		initSessions: make(map[string]*Session),
		initPrograms: programs,
	}
	s.nextClientID.Store(1)

	go s.eventLoop()

	return s
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

	s.requests <- addClientReq{client: client}

	slog.Debug("client connected", "id", id)
	client.sendIdentify()
	go client.ReadLoop()
}

func (s *Server) SpawnRegion(sessionName, cmd string, args []string, env map[string]string) (*Region, error) {
	region, err := NewRegion(cmd, args, env, 80, 24)
	if err != nil {
		return nil, err
	}

	resp := make(chan struct{}, 1)
	s.requests <- spawnRegionReq{region: region, sessionName: sessionName, resp: resp}
	<-resp

	slog.Info("spawned region", "region_id", region.id, "cmd", cmd, "session", sessionName)

	go s.watchRegion(region)
	return region, nil
}

func (s *Server) watchRegion(region *Region) {
	for range region.notify {
		s.sendTerminalEvents(region)
	}
	<-region.readerDone
	s.sendTerminalEvents(region)
	s.destroyRegion(region.id)
}

func (s *Server) destroyRegion(regionID string) {
	resp := make(chan destroyResult, 1)
	s.requests <- destroyRegionReq{regionID: regionID, resp: resp}
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

	slog.Info("destroyed region", "region_id", regionID, "session", result.region.session)
	result.region.Close()
}

func (s *Server) FindRegion(regionID string) *Region {
	resp := make(chan *Region, 1)
	s.requests <- findRegionReq{regionID: regionID, resp: resp}
	return <-resp
}

func (s *Server) Broadcast(msg any) {
	resp := make(chan []*Client, 1)
	s.requests <- getClientsReq{resp: resp}
	for _, c := range <-resp {
		c.SendMessage(msg)
	}
}

func (s *Server) KillRegion(regionID string) bool {
	resp := make(chan *Region, 1)
	s.requests <- killRegionReq{regionID: regionID, resp: resp}
	region := <-resp
	if region == nil {
		return false
	}
	region.Kill()
	return true
}

func (s *Server) KillClient(clientID uint32) bool {
	resp := make(chan *Client, 1)
	s.requests <- killClientReq{clientID: clientID, resp: resp}
	client := <-resp
	if client == nil {
		return false
	}
	client.Close()
	return true
}

func (s *Server) removeClient(id uint32) {
	s.requests <- removeClientReq{clientID: id}
}

func (s *Server) sendTerminalEvents(region *Region) {
	events, needsSnapshot := region.FlushEvents()

	if !needsSnapshot && len(events) == 0 {
		return
	}

	resp := make(chan []*Client, 1)
	s.requests <- getSubscribersReq{regionID: region.id, resp: resp}
	subscribers := <-resp

	if len(subscribers) == 0 {
		return
	}

	if needsSnapshot {
		snap := region.Snapshot()
		snapMsg := protocol.ScreenUpdate{
			Type:      "screen_update",
			RegionID:  region.id,
			CursorRow: snap.CursorRow,
			CursorCol: snap.CursorCol,
			Lines:     snap.Lines,
			Cells:     snap.Cells,
			Modes:     snap.Modes,
		}
		for _, c := range subscribers {
			c.SendMessage(snapMsg)
		}
		return
	}

	msg := protocol.TerminalEvents{
		Type:     "terminal_events",
		RegionID: region.id,
		Events:   events,
	}

	for _, c := range subscribers {
		c.SendMessage(msg)
	}
}

func (s *Server) getStatus() protocol.StatusResponse {
	resp := make(chan statusCounts, 1)
	s.requests <- getStatusReq{resp: resp}
	counts := <-resp

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
		Error:         false,
		Message:       "",
	}
}

func (s *Server) listenerAddrs() string {
	addrs := make([]string, len(s.listeners))
	for i, ln := range s.listeners {
		addrs[i] = ln.Addr().String()
	}
	return strings.Join(addrs, ", ")
}

// SpawnProgram looks up a program by name and spawns it into the given session.
func (s *Server) SpawnProgram(sessionName, programName string) (*Region, error) {
	resp := make(chan *config.ProgramConfig, 1)
	s.requests <- lookupProgramReq{name: programName, resp: resp}
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
	s.requests <- sessionConnectReq{name: name, resp: resp}
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
			RegionID: region.id,
			Name:     region.name,
			Cmd:      region.cmd,
			Pid:      region.pid,
			Session:  name,
		})
	}

	return &Session{name: name}, infos, nil
}

func (s *Server) listProgramInfos() []protocol.ProgramInfo {
	resp := make(chan []protocol.ProgramInfo, 1)
	s.requests <- listProgramsReq{resp: resp}
	return <-resp
}

func (s *Server) addProgram(p config.ProgramConfig) error {
	resp := make(chan error, 1)
	s.requests <- addProgramReq{prog: p, resp: resp}
	return <-resp
}

func (s *Server) removeProgram(name string) error {
	resp := make(chan error, 1)
	s.requests <- removeProgramReq{name: name, resp: resp}
	return <-resp
}

func (s *Server) getRegionInfos(sessionFilter string) []protocol.RegionInfo {
	resp := make(chan []protocol.RegionInfo, 1)
	s.requests <- getRegionInfosReq{session: sessionFilter, resp: resp}
	return <-resp
}

func (s *Server) getClientInfos() []protocol.ClientInfoData {
	resp := make(chan []protocol.ClientInfoData, 1)
	s.requests <- getClientInfosReq{resp: resp}
	return <-resp
}

func (s *Server) getSessionInfos() []protocol.SessionInfo {
	resp := make(chan []protocol.SessionInfo, 1)
	s.requests <- getSessionInfosReq{resp: resp}
	return <-resp
}

func (s *Server) Subscribe(clientID uint32, regionID string) bool {
	resp := make(chan bool, 1)
	s.requests <- subscribeReq{clientID: clientID, regionID: regionID, resp: resp}
	return <-resp
}

func (s *Server) Unsubscribe(clientID uint32) {
	s.requests <- unsubscribeReq{clientID: clientID}
}

func (s *Server) SetClientSession(clientID uint32, sessionName string) {
	s.requests <- setClientSessionReq{clientID: clientID, sessionName: sessionName}
}

func (s *Server) GetClientSession(clientID uint32) string {
	resp := make(chan string, 1)
	s.requests <- getClientSessionReq{clientID: clientID, resp: resp}
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
