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

	mu       sync.Mutex
	regions  map[string]*Region
	clients  map[uint32]*Client
	sessions map[string]*Session
	programs map[string]config.ProgramConfig

	done     chan struct{}
	shutdown atomic.Bool
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
		version:     version,
		listeners:   listeners,
		startTime:   time.Now(),
		sessionsCfg: sessionsCfg,
		programs:    programs,
		regions:     make(map[string]*Region),
		clients:     make(map[uint32]*Client),
		sessions:    make(map[string]*Session),
		done:        make(chan struct{}),
	}
	s.nextClientID.Store(1)
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

	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	regions := make([]*Region, 0, len(s.regions))
	for _, r := range s.regions {
		regions = append(regions, r)
	}
	s.mu.Unlock()

	for _, c := range clients {
		c.Close()
	}
	for _, r := range regions {
		r.Close()
	}
}

func (s *Server) acceptClient(conn net.Conn) {
	id := s.nextClientID.Add(1) - 1
	client := NewClient(conn, s, id)

	s.mu.Lock()
	s.clients[id] = client
	s.mu.Unlock()

	slog.Debug("client connected", "id", id)
	client.sendIdentify()
	go client.ReadLoop()
}

func (s *Server) SpawnRegion(sessionName, cmd string, args []string, env map[string]string) (*Region, error) {
	region, err := NewRegion(cmd, args, env, 80, 24)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.regions[region.id] = region
	sess := s.sessions[sessionName]
	if sess == nil {
		sess = NewSession(sessionName)
		s.sessions[sessionName] = sess
	}
	sess.regions[region.id] = region
	region.session = sessionName
	s.mu.Unlock()

	slog.Info("spawned region", "region_id", region.id, "cmd", cmd, "session", sessionName)

	go s.watchRegion(region)
	return region, nil
}

func (s *Server) watchRegion(region *Region) {
	for range region.notify {
		s.sendTerminalEvents(region)
	}
	// Channel closed means region's process exited.
	// Wait for readLoop to finish draining the PTY buffer before the final flush.
	<-region.readerDone
	s.sendTerminalEvents(region)
	s.destroyRegion(region.id)
}

func (s *Server) destroyRegion(regionID string) {
	s.mu.Lock()
	region, ok := s.regions[regionID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.regions, regionID)

	// Remove from session; delete session if empty.
	if sess := s.sessions[region.session]; sess != nil {
		delete(sess.regions, regionID)
		if len(sess.regions) == 0 {
			delete(s.sessions, region.session)
			slog.Info("removed empty session", "session", region.session)
		}
	}

	var toNotify []*Client
	for _, c := range s.clients {
		if c.GetSubscribedRegionID() == regionID {
			c.SetSubscribedRegionID("")
			toNotify = append(toNotify, c)
		}
	}
	s.mu.Unlock()

	for _, c := range toNotify {
		c.SendMessage(protocol.RegionDestroyed{
			Type:     "region_destroyed",
			RegionID: regionID,
		})
	}

	slog.Info("destroyed region", "region_id", regionID, "session", region.session)
	region.Close()
}

func (s *Server) FindRegion(regionID string) *Region {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.regions[regionID]
}

func (s *Server) Broadcast(msg any) {
	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, c := range clients {
		c.SendMessage(msg)
	}
}

func (s *Server) KillRegion(regionID string) bool {
	s.mu.Lock()
	region, ok := s.regions[regionID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	region.Kill()
	return true
}

func (s *Server) KillClient(clientID uint32) bool {
	s.mu.Lock()
	client, ok := s.clients[clientID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	client.Close()
	return true
}

func (s *Server) removeClient(id uint32) {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
	slog.Debug("client disconnected", "id", id)
}

func (s *Server) sendTerminalEvents(region *Region) {
	events, needsSnapshot := region.FlushEvents()

	if !needsSnapshot && len(events) == 0 {
		return
	}

	s.mu.Lock()
	var subscribers []*Client
	for _, c := range s.clients {
		if c.GetSubscribedRegionID() == region.id {
			subscribers = append(subscribers, c)
		}
	}
	s.mu.Unlock()

	if len(subscribers) == 0 {
		return
	}

	if needsSnapshot {
		// Synchronized output completed — send an atomic screen snapshot
		// instead of individual events to avoid rendering intermediate states.
		snap := region.Snapshot()
		snapMsg := protocol.ScreenUpdate{
			Type:      "screen_update",
			RegionID:  region.id,
			CursorRow: snap.CursorRow,
			CursorCol: snap.CursorCol,
			Lines:     snap.Lines,
			Cells:     snap.Cells,
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
	s.mu.Lock()
	numClients := len(s.clients)
	numRegions := len(s.regions)
	numSessions := len(s.sessions)
	s.mu.Unlock()

	hostname, _ := os.Hostname()
	return protocol.StatusResponse{
		Type:          "status_response",
		Hostname:      hostname,
		Version:       s.version,
		Pid:           os.Getpid(),
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
		SocketPath:    s.listenerAddrs(),
		NumClients:    numClients,
		NumRegions:    numRegions,
		NumSessions:   numSessions,
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
	s.mu.Lock()
	prog, ok := s.programs[programName]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown program: %s", programName)
	}
	return s.SpawnRegion(sessionName, prog.Cmd, prog.Args, prog.Env)
}

// findOrCreateSession returns an existing session or creates a new one with
// default programs. The caller must NOT hold s.mu.
func (s *Server) findOrCreateSession(name string) (*Session, []protocol.RegionInfo, error) {
	if name == "" {
		name = s.sessionsCfg.DefaultName
	}

	s.mu.Lock()
	sess, exists := s.sessions[name]
	if exists {
		infos := s.sessionRegionInfos(sess)
		s.mu.Unlock()
		return sess, infos, nil
	}
	s.mu.Unlock()

	// Determine which programs to spawn.
	programNames := s.sessionsCfg.DefaultPrograms
	if len(programNames) == 0 {
		// Spawn the first program (or "default" if it exists).
		s.mu.Lock()
		if _, ok := s.programs["default"]; ok {
			programNames = []string{"default"}
		} else {
			for pname := range s.programs {
				programNames = []string{pname}
				break
			}
		}
		s.mu.Unlock()
	}

	var infos []protocol.RegionInfo
	for _, pname := range programNames {
		region, err := s.SpawnProgram(name, pname)
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

	s.mu.Lock()
	sess = s.sessions[name]
	s.mu.Unlock()

	return sess, infos, nil
}

// sessionRegionInfos returns RegionInfo for all regions in a session.
// Caller must hold s.mu.
func (s *Server) sessionRegionInfos(sess *Session) []protocol.RegionInfo {
	infos := make([]protocol.RegionInfo, 0, len(sess.regions))
	for _, r := range sess.regions {
		infos = append(infos, protocol.RegionInfo{
			RegionID: r.id,
			Name:     r.name,
			Cmd:      r.cmd,
			Pid:      r.pid,
			Session:  sess.name,
		})
	}
	return infos
}

// serverShell returns the server user's shell.
func serverShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "sh"
}

func (s *Server) listProgramInfos() []protocol.ProgramInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	infos := make([]protocol.ProgramInfo, 0, len(s.programs))
	for _, p := range s.programs {
		infos = append(infos, protocol.ProgramInfo{Name: p.Name, Cmd: p.Cmd})
	}
	return infos
}

func (s *Server) addProgram(p config.ProgramConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.programs[p.Name]; exists {
		return fmt.Errorf("program %q already exists", p.Name)
	}
	s.programs[p.Name] = p
	return nil
}

func (s *Server) removeProgram(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.programs[name]; !exists {
		return fmt.Errorf("program %q not found", name)
	}
	delete(s.programs, name)
	return nil
}

func (s *Server) getRegionInfos(sessionFilter string) []protocol.RegionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sessionFilter != "" {
		sess := s.sessions[sessionFilter]
		if sess == nil {
			return nil
		}
		return s.sessionRegionInfos(sess)
	}

	infos := make([]protocol.RegionInfo, 0, len(s.regions))
	for _, r := range s.regions {
		infos = append(infos, protocol.RegionInfo{
			RegionID: r.id,
			Name:     r.name,
			Cmd:      r.cmd,
			Pid:      r.pid,
			Session:  r.session,
		})
	}
	return infos
}

func (s *Server) getClientInfos() []protocol.ClientInfoData {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]protocol.ClientInfoData, 0, len(s.clients))
	for _, c := range s.clients {
		infos = append(infos, protocol.ClientInfoData{
			ClientID:           c.id,
			Hostname:           c.GetHostname(),
			Username:           c.GetUsername(),
			Pid:                c.GetPid(),
			Process:            c.GetProcess(),
			Session:            c.GetSessionName(),
			SubscribedRegionID: c.GetSubscribedRegionID(),
		})
	}
	return infos
}

func (s *Server) getSessionInfos() []protocol.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]protocol.SessionInfo, 0, len(s.sessions))
	for _, sess := range s.sessions {
		infos = append(infos, protocol.SessionInfo{
			Name:       sess.name,
			NumRegions: len(sess.regions),
		})
	}
	return infos
}
