package server

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

	"nxtermd/internal/config"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
)

type Server struct {
	version      string
	binariesDir  string
	listeners    []net.Listener
	startTime    time.Time
	nextClientID atomic.Uint32
	sessionsCfg  config.SessionsConfig
	// cfg is the effective server configuration (config file with CLI
	// flag overrides applied). Preserved intact so a live upgrade can
	// hand the same config to the new process.
	cfg config.ServerConfig

	requests     chan request
	done         chan struct{}
	shutdownResp chan shutdownResult
	shutdown     atomic.Bool
	// noAccept is set during a live upgrade to reject any new
	// connections that sneak through before the new process has
	// taken over. acceptLoop checks this flag after Accept and
	// immediately closes the conn if set. Cleared by
	// resumeAfterFailedUpgrade on rollback.
	noAccept atomic.Bool

	// sessionsChanged is invoked from the event loop (in a goroutine)
	// whenever the set of sessions changes. The argument is the sorted
	// list of session names. Set via SetSessionsChanged before Run.
	sessionsChanged atomic.Value // func([]string)

	// sessionCreateMus serializes findOrCreateSession per session name,
	// preventing two concurrent connects from racing to spawn duplicate
	// default programs into the same not-yet-existing session. Keys are
	// session names; values are *sync.Mutex.
	sessionCreateMus sync.Map

	// initPrograms transfers ownership of the programs map to the event
	// loop goroutine. Set in NewServer and consumed (nilled) by eventLoop
	// on startup.
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
		binariesDir:  resolveBinariesDir(cfg.Upgrade.BinariesDir),
		listeners:    listeners,
		startTime:    time.Now(),
		sessionsCfg:  sessionsCfg,
		cfg:          cfg,
		requests:     make(chan request, 256),
		done:         make(chan struct{}),
		shutdownResp: make(chan shutdownResult, 1),
		initPrograms: programs,
	}
	s.nextClientID.Store(1)

	go s.eventLoop()

	return s
}

// send sends a request to the event loop, returning false if the server
// is shutting down. Callers that need a response should check the return
// value before reading from their response channel.
func (s *Server) send(req request) bool {
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
		if s.noAccept.Load() {
			// Live upgrade is in progress; the new process will take
			// over. Drop this connection so the client retries and
			// lands on the new server instead of transiently hitting
			// the dying old one.
			conn.Close()
			continue
		}
		wrapped := transport.WrapTracing(conn, fmt.Sprintf("server:%s", conn.RemoteAddr()))
		s.acceptClient(transport.NegotiateCompressionServer(wrapped))
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
	go client.ReadLoop()
}

// SpawnRegion creates a new PTY region in the given session. width and
// height seed the initial PTY size; pass 0 to use the built-in default
// (80x24).
func (s *Server) SpawnRegion(sessionName, cmd string, args []string, env map[string]string, width, height uint16) (Region, error) {
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	region, err := NewRegion(cmd, args, env, int(width), int(height), s.socketAddr(), s.destroyRegion)
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

	return region, nil
}

// SpawnNativeRegion creates a new native region driven by the given client.
// The region is placed in sessionName (creating it if needed). width/height
// seed the initial screen size; pass 0 to use the default (80x24).
func (s *Server) SpawnNativeRegion(driver *Client, sessionName, name string, width, height uint16) (*NativeRegion, error) {
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	region := NewNativeRegion(driver, name, int(width), int(height), s.destroyRegion)

	resp := make(chan struct{}, 1)
	if !s.send(spawnRegionReq{region: region, sessionName: sessionName, resp: resp}) {
		region.Close()
		return nil, fmt.Errorf("server shutting down")
	}
	<-resp

	slog.Info("spawned native region", "region_id", region.ID(), "name", name,
		"session", sessionName, "driver_client_id", driver.id)

	return region, nil
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
	// Note: region_destroyed broadcast removed — clients are notified
	// via tree_events from the destroyRegionReq handler in the event loop.

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

// RouteInput looks up where input for a region should go. If an overlay is
// active, the overlay's Client is returned. Otherwise the Region is returned
// for direct PTY write.
func (s *Server) RouteInput(regionID string) (Region, *Client) {
	resp := make(chan inputRouteResult, 1)
	if !s.send(inputRouteReq{regionID: regionID, resp: resp}) {
		return nil, nil
	}
	result := <-resp
	return result.region, result.overlayClient
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

// newScreenUpdate builds a protocol.ScreenUpdate from a Snapshot for the
// given region. All construction sites go through this so fields stay in
// sync across snapshot, overlay, and subscribe paths.
func newScreenUpdate(regionID string, snap Snapshot) protocol.ScreenUpdate {
	return protocol.ScreenUpdate{
		Type:          "screen_update",
		RegionID:      regionID,
		CursorRow:     snap.CursorRow,
		CursorCol:     snap.CursorCol,
		Lines:         snap.Lines,
		Cells:         snap.Cells,
		Modes:         snap.Modes,
		Title:         snap.Title,
		IconName:      snap.IconName,
		ScrollbackLen: snap.ScrollbackLen,
	}
}

// compositeSnapshot overlays cells from an overlay on top of a base snapshot.
// Non-empty overlay cells replace base cells. The overlay's cursor and modes
// take precedence.
func compositeSnapshot(base Snapshot, ov *overlayState) Snapshot {
	result := Snapshot{
		CursorRow: ov.cursorRow,
		CursorCol: ov.cursorCol,
		Title:     base.Title,
		IconName:  base.IconName,
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

// socketAddr returns the address of the first listener, for passing to
// child processes as NXTERMD_SOCKET.
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
	return s.SpawnRegion(sessionName, prog.Cmd, prog.Args, prog.Env, 0, 0)
}

// sessionCreateMu returns the per-session-name mutex used to serialize
// concurrent findOrCreateSession calls. Without this serialization, two
// clients connecting to the same not-yet-existing session would both see
// "session does not exist" and race to spawn duplicate default programs.
func (s *Server) sessionCreateMu(name string) *sync.Mutex {
	v, _ := s.sessionCreateMus.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// findOrCreateSession returns an existing session's regions or spawns
// default programs into a new session and returns the resulting regions.
// width and height seed the initial PTY size of newly-spawned regions;
// pass 0 to use the built-in default (80x24).
//
// A per-name mutex serializes concurrent calls for the same session
// name so the find-and-spawn pair is atomic with respect to other
// clients. Calls for different session names run in parallel.
func (s *Server) findOrCreateSession(name string, width, height uint16) (string, []protocol.RegionInfo, error) {
	if name == "" {
		name = s.sessionsCfg.DefaultName
	}

	mu := s.sessionCreateMu(name)
	mu.Lock()
	defer mu.Unlock()

	resp := make(chan sessionConnectResult, 1)
	if !s.send(sessionConnectReq{name: name, width: width, height: height, resp: resp}) {
		return "", nil, fmt.Errorf("server shutting down")
	}
	result := <-resp

	if result.exists {
		return name, result.regionInfos, nil
	}

	// Session doesn't exist yet — spawn the programs.
	var infos []protocol.RegionInfo
	for _, prog := range result.programConfigs {
		region, err := s.SpawnRegion(name, prog.Cmd, prog.Args, prog.Env, width, height)
		if err != nil {
			return "", nil, err
		}
		infos = append(infos, protocol.RegionInfo{
			RegionID: region.ID(),
			Name:     region.Name(),
			Cmd:      region.Cmd(),
			Pid:      region.Pid(),
			Session:  name,
		})
	}

	return name, infos, nil
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

// SetSessionsChanged registers a callback invoked from the server event
// loop whenever the set of sessions changes. The callback receives a
// sorted snapshot of session names and is dispatched in its own
// goroutine, so it may block without stalling the event loop.
func (s *Server) SetSessionsChanged(fn func([]string)) {
	s.sessionsChanged.Store(fn)
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
