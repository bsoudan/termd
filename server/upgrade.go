package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"nxtermd/config"
	"nxtermd/frontend/protocol"
	"nxtermd/transport"
)

const upgradeTimeout = 60 * time.Second

// HandleUpgrade performs a live upgrade by spawning a new binary and
// handing off all listeners, PTY FDs, and terminal state. It also
// broadcasts a ServerUpgradeStatus message at each phase so connected
// clients can track progress and reliably distinguish the real handoff
// from an incidental reconnect.
func (s *Server) HandleUpgrade(specs []string, sshCfg transport.SSHListenerConfig) error {
	// Take a stable snapshot of the connected clients up front. We
	// keep these clients connected throughout the upgrade so they can
	// receive the broadcast; they are closed at the very end, right
	// before this function returns. Any client that happens to connect
	// in the narrow window before stopAccepting is reached will simply
	// miss the early phase broadcasts — harmless, since they'll see
	// the final shutting_down broadcast (if still present in the
	// post-drain map) or just a disconnect.
	clients := s.snapshotClients()
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseStarting, "starting upgrade")

	newBin, err := os.Executable()
	if err != nil {
		s.failUpgrade(clients, fmt.Sprintf("os.Executable: %v", err))
		return fmt.Errorf("os.Executable: %w", err)
	}
	slog.Info("upgrade: starting", "binary", newBin)

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		s.failUpgrade(clients, fmt.Sprintf("socketpair: %v", err))
		return fmt.Errorf("socketpair: %w", err)
	}
	parentFD, childFD := fds[0], fds[1]

	childFile := os.NewFile(uintptr(childFD), "upgrade-child")
	cmd := exec.Command(newBin, "--upgrade-fd", "3")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{childFile}
	if err := cmd.Start(); err != nil {
		unix.Close(parentFD)
		unix.Close(childFD)
		s.failUpgrade(clients, fmt.Sprintf("exec new binary: %v", err))
		return fmt.Errorf("exec new binary: %w", err)
	}
	childFile.Close()
	unix.Close(childFD)
	slog.Info("upgrade: new process started", "pid", cmd.Process.Pid)
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseSpawned,
		fmt.Sprintf("new process started (pid %d)", cmd.Process.Pid))

	parentFile := os.NewFile(uintptr(parentFD), "upgrade-parent")
	parentNetConn, err := net.FileConn(parentFile)
	parentFile.Close()
	if err != nil {
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("file conn: %v", err))
		return fmt.Errorf("file conn: %w", err)
	}
	conn := parentNetConn.(*net.UnixConn)
	defer conn.Close()

	listenerFDs := make([]int, 0, len(s.listeners))
	for _, ln := range s.listeners {
		f, err := transport.ListenerFile(ln)
		if err != nil {
			cmd.Process.Kill()
			s.failUpgrade(clients, fmt.Sprintf("extract listener FD: %v", err))
			return fmt.Errorf("extract listener FD: %w", err)
		}
		listenerFDs = append(listenerFDs, int(f.Fd()))
		defer f.Close()
	}
	if err := sendMsg(conn, upgradeMsg{
		Type:    "listener_fds",
		Specs:   specs,
		FDCount: len(listenerFDs),
	}, listenerFDs); err != nil {
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("send listener FDs: %v", err))
		return fmt.Errorf("send listener FDs: %w", err)
	}
	slog.Info("upgrade: sent listener FDs", "count", len(listenerFDs))
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseSentListenerFDs,
		fmt.Sprintf("sent %d listener fd(s)", len(listenerFDs)))

	slog.Info("upgrade: stopping accept loops...")
	s.stopAccepting()
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseStoppedAccepting, "stopped accepting new connections")

	slog.Info("upgrade: draining event loop...")
	result := s.drainForUpgrade()
	slog.Info("upgrade: event loop drained")
	// Use result.clients from now on — it captures anyone who connected
	// between our initial snapshot and stopAccepting.
	clients = result.clients
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseDrained, "event loop drained")

	// Stop all PTY readLoops before snapshotting screen state. With the
	// readLoops stopped, no more bytes can mutate hscreen, so the
	// snapshot is guaranteed consistent. Bytes that arrive on the PTY
	// after this point queue in the kernel buffer and will be picked up
	// by the new process's readLoop after handoff — nothing is lost.
	for id, r := range result.regions {
		if pr, ok := r.(*PTYRegion); ok {
			if err := pr.StopReadLoop(); err != nil {
				slog.Warn("upgrade: failed to stop readLoop", "region_id", id, "err", err)
			}
		}
	}
	slog.Info("upgrade: stopped PTY readLoops", "pty_regions", len(result.regions))
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseStoppedReadLoops,
		fmt.Sprintf("stopped %d pty read loop(s)", len(result.regions)))

	// Dup PTY FDs for handoff. Native regions don't have PTYs.
	ptyDups := make(map[string]*os.File) // regionID → dup'd PTY file
	for id, r := range result.regions {
		if pr, ok := r.(*PTYRegion); ok {
			ptyDups[id] = pr.DetachPTY()
		}
	}
	slog.Info("upgrade: detached PTY FDs", "pty_regions", len(ptyDups))

	state := buildUpgradeState(s, result, specs)
	stateJSON, err := json.Marshal(state)
	if err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("marshal state: %v", err))
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := sendMsg(conn, upgradeMsg{
		Type:  "state",
		State: stateJSON,
	}, nil); err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("send state: %v", err))
		return fmt.Errorf("send state: %w", err)
	}
	slog.Info("upgrade: sent state", "bytes", len(stateJSON))
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseSentState,
		fmt.Sprintf("sent state (%d bytes)", len(stateJSON)))

	var ptyFDs []int
	var regionIDs []string
	for _, rs := range state.Regions {
		if f, ok := ptyDups[rs.ID]; ok && f != nil {
			ptyFDs = append(ptyFDs, int(f.Fd()))
			regionIDs = append(regionIDs, rs.ID)
		}
	}
	if err := sendMsg(conn, upgradeMsg{
		Type:      "pty_fds",
		RegionIDs: regionIDs,
		FDCount:   len(ptyFDs),
	}, ptyFDs); err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("send pty FDs: %v", err))
		return fmt.Errorf("send pty FDs: %w", err)
	}
	slog.Info("upgrade: sent PTY FDs", "count", len(ptyFDs))
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseSentPTYFDs,
		fmt.Sprintf("sent %d pty fd(s)", len(ptyFDs)))

	if err := sendMsg(conn, upgradeMsg{Type: "handoff_complete"}, nil); err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("send handoff_complete: %v", err))
		return fmt.Errorf("send handoff_complete: %w", err)
	}

	resp, _, err := recvMsg(conn, upgradeTimeout)
	if err != nil {
		slog.Error("upgrade: no response from new process", "err", err)
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("waiting for ready: %v", err))
		return fmt.Errorf("waiting for ready: %w", err)
	}
	if resp.Type == "error" {
		slog.Error("upgrade: new process reported error", "message", resp.Message)
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		s.failUpgrade(clients, fmt.Sprintf("new process error: %s", resp.Message))
		return fmt.Errorf("new process error: %s", resp.Message)
	}
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseReady, "new server is ready")

	// Tell clients we're about to close the connections, then drain the
	// write buffers briefly so the message actually goes out over the
	// wire before Close() frees the writeCh. Only then do we close.
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseShuttingDown, "old server shutting down")
	flushAndCloseClients(clients)

	slog.Info("upgrade: success, old process exiting")
	return nil
}

func buildUpgradeState(s *Server, result upgradeResult, specs []string) *UpgradeState {
	state := &UpgradeState{
		Version:       s.version,
		ListenerSpecs: specs,
		NextClientID:  s.nextClientID.Load(),
		Programs:      make(map[string]ProgramConfigJSON),
		BinariesDir:   s.binariesDir,
	}
	state.SessionsCfg = SessionsCfgJSON{
		DefaultName:     s.sessionsCfg.DefaultName,
		DefaultPrograms: s.sessionsCfg.DefaultPrograms,
	}
	for name, p := range result.programs {
		state.Programs[name] = ProgramConfigJSON{
			Name: p.Name, Cmd: p.Cmd, Args: p.Args, Env: p.Env,
		}
	}
	for name, sess := range result.sessions {
		ss := SessionState{Name: name}
		for id := range sess.regions {
			ss.RegionIDs = append(ss.RegionIDs, id)
		}
		state.Sessions = append(state.Sessions, ss)
	}
	// Only serialize PTY regions — native regions are killed on upgrade.
	for _, r := range result.regions {
		pr, ok := r.(*PTYRegion)
		if !ok {
			slog.Info("upgrade: skipping native region", "region_id", r.ID())
			continue
		}
		pr.mu.Lock()
		histState := pr.hscreen.MarshalState()
		pr.mu.Unlock()
		state.Regions = append(state.Regions, RegionState{
			ID: pr.id, Name: pr.name, Cmd: pr.cmd, Pid: pr.pid,
			Session: pr.session, Width: pr.width, Height: pr.height,
			Screen: histState,
		})
	}
	return state
}

func (s *Server) stopAccepting() {
	// Set a dedicated flag (not s.shutdown — Shutdown uses CompareAndSwap
	// and setting it early would cause it to skip closing listeners and
	// the done channel, leaving Run blocked forever). The accept loop
	// still runs, but every newly-accepted connection is immediately
	// closed so the client retries past the upgrade window and lands on
	// the new process.
	s.noAccept.Store(true)
}

// snapshotClients takes a copy of the current client map via the event
// loop. The copy is safe to iterate from outside the event loop.
func (s *Server) snapshotClients() map[uint32]*Client {
	resp := make(chan map[uint32]*Client, 1)
	if !s.send(snapshotClientsReq{resp: resp}) {
		return map[uint32]*Client{}
	}
	return <-resp
}

// broadcastUpgradeStatus sends a ServerUpgradeStatus message to every
// client in the snapshot. SendMessage is non-blocking; the message may
// be dropped if the client's write channel is full, which is fine for
// status updates (the important one — shutting_down — is sent last and
// followed by flushAndCloseClients which waits for writeCh to drain).
func (s *Server) broadcastUpgradeStatus(clients map[uint32]*Client, phase, message string) {
	msg := protocol.ServerUpgradeStatus{
		Type:    "server_upgrade_status",
		Phase:   phase,
		Message: message,
	}
	for _, c := range clients {
		c.SendMessage(msg)
	}
}

// failUpgrade broadcasts a failure status to clients. Used on any
// error path out of HandleUpgrade. Does not close the clients —
// they remain connected after a failed upgrade.
func (s *Server) failUpgrade(clients map[uint32]*Client, message string) {
	s.broadcastUpgradeStatus(clients, protocol.UpgradePhaseFailed, message)
}

// flushAndCloseClients drains each client's write channel so any
// buffered messages (notably the final shutting_down status) actually
// reach the wire before the connection closes. Each client is given a
// short deadline; if it can't drain in time the close proceeds anyway.
func flushAndCloseClients(clients map[uint32]*Client) {
	const deadline = 250 * time.Millisecond
	var wg sync.WaitGroup
	wg.Add(len(clients))
	for _, c := range clients {
		go func(c *Client) {
			defer wg.Done()
			c.CloseGracefully(deadline)
		}(c)
	}
	wg.Wait()
}

func (s *Server) drainForUpgrade() upgradeResult {
	resp := make(chan upgradeResult)
	s.send(upgradeReq{resp: resp})
	return <-resp
}

func (s *Server) resumeAfterFailedUpgrade(result upgradeResult) {
	slog.Warn("upgrade: rolling back")
	s.noAccept.Store(false)
	s.send(resumeUpgradeReq{})
	for _, r := range result.regions {
		pr, ok := r.(*PTYRegion)
		if !ok {
			continue // native regions don't have readLoops to restart
		}
		if err := pr.ResumeReadLoop(); err != nil {
			slog.Error("upgrade: rollback failed to restart readLoop", "region_id", pr.id, "err", err)
		}
	}
	slog.Warn("upgrade: rollback complete, but listeners were closed; restart may be needed")
}

type upgradeReq struct{ resp chan upgradeResult }
type resumeUpgradeReq struct{}
type snapshotClientsReq struct{ resp chan map[uint32]*Client }

type upgradeResult struct {
	regions  map[string]Region
	sessions map[string]*Session
	programs map[string]config.ProgramConfig
	// clients is the snapshot of connected clients at the moment the
	// event loop paused. The event loop goroutine does not touch the
	// map after returning this result (it blocks on s.requests waiting
	// for resume/done), so HandleUpgrade can iterate it safely and
	// broadcast upgrade status messages. HandleUpgrade is responsible
	// for closing every client at the end of the upgrade.
	clients map[uint32]*Client
}
