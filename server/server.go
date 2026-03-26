package main

import (
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"termd/frontend/protocol"
)

type Server struct {
	socketPath   string
	listener     net.Listener
	startTime    time.Time
	nextClientID atomic.Uint32

	mu      sync.Mutex
	regions map[string]*Region
	clients map[uint32]*Client

	done     chan struct{}
	shutdown atomic.Bool
}

func NewServer(socketPath string) (*Server, error) {
	os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}

	slog.Info("listening", "socket", socketPath)

	s := &Server{
		socketPath: socketPath,
		listener:   listener,
		startTime:  time.Now(),
		regions:    make(map[string]*Region),
		clients:    make(map[uint32]*Client),
		done:       make(chan struct{}),
	}
	s.nextClientID.Store(1)
	return s, nil
}

func (s *Server) Run() {
	for {
		conn, err := s.listener.Accept()
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
	s.listener.Close()

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

	os.Remove(s.socketPath)
}

func (s *Server) acceptClient(conn net.Conn) {
	id := s.nextClientID.Add(1) - 1
	client := NewClient(conn, s, id)

	s.mu.Lock()
	s.clients[id] = client
	s.mu.Unlock()

	slog.Debug("client connected", "id", id)
	go client.ReadLoop()
}

func (s *Server) SpawnRegion(cmd string, args []string) (*Region, error) {
	region, err := NewRegion(cmd, args, 80, 24)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.regions[region.id] = region
	s.mu.Unlock()

	slog.Info("spawned region", "region_id", region.id, "cmd", cmd)

	go s.watchRegion(region)
	return region, nil
}

func (s *Server) watchRegion(region *Region) {
	for range region.notify {
		s.sendTerminalEvents(region)
	}
	// Channel closed means region's process exited.
	// Send a final update to capture any remaining output.
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

	slog.Info("destroyed region", "region_id", regionID)
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
	events := region.FlushEvents()
	if len(events) == 0 {
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

	msg := protocol.TerminalEvents{
		Type:     "terminal_events",
		RegionID: region.id,
		Events:   events,
	}

	for _, c := range subscribers {
		c.SendMessage(msg)
	}
}

func (s *Server) sendScreenUpdate(region *Region) {
	snap := region.Snapshot()

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

	msg := protocol.ScreenUpdate{
		Type:      "screen_update",
		RegionID:  region.id,
		CursorRow: snap.CursorRow,
		CursorCol: snap.CursorCol,
		Lines:     snap.Lines,
	}

	for _, c := range subscribers {
		c.SendMessage(msg)
	}
}

func (s *Server) getStatus() protocol.StatusResponse {
	s.mu.Lock()
	numClients := len(s.clients)
	numRegions := len(s.regions)
	s.mu.Unlock()

	return protocol.StatusResponse{
		Type:          "status_response",
		Pid:           os.Getpid(),
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
		SocketPath:    s.socketPath,
		NumClients:    numClients,
		NumRegions:    numRegions,
		Error:         false,
		Message:       "",
	}
}

func (s *Server) getRegionInfos() []protocol.RegionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]protocol.RegionInfo, 0, len(s.regions))
	for _, r := range s.regions {
		infos = append(infos, protocol.RegionInfo{
			RegionID: r.id,
			Name:     r.name,
			Cmd:      r.cmd,
			Pid:      r.pid,
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
			SubscribedRegionID: c.GetSubscribedRegionID(),
		})
	}
	return infos
}
