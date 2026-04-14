package server

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
	"nxtermd/internal/config"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
)

const upgradeTimeout = 60 * time.Second

// HandleUpgrade performs a live upgrade by spawning a new binary and
// handing off all listeners, PTY FDs, and terminal state. Upgrade
// progress is published on the tree's UpgradeNode so connected clients
// can track phases via tree events.
func (s *Server) HandleUpgrade(specs []string, sshCfg transport.SSHListenerConfig) error {
	// Alias for brevity. Pre-drain calls pass nil tree/clients to
	// route through the event loop; post-drain calls pass the drained
	// tree and client snapshot for direct mutation.
	phase := s.setUpgradePhase
	fail := func(tree *ServerTree, clients map[uint32]*Client, msg string) {
		phase(tree, clients, protocol.UpgradePhaseFailed, msg)
	}

	phase(nil, nil, protocol.UpgradePhaseStarting, "starting upgrade")

	newBin, err := os.Executable()
	if err != nil {
		fail(nil, nil, fmt.Sprintf("os.Executable: %v", err))
		return fmt.Errorf("os.Executable: %w", err)
	}
	slog.Info("upgrade: starting", "binary", newBin)

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		fail(nil, nil, fmt.Sprintf("socketpair: %v", err))
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
		fail(nil, nil, fmt.Sprintf("exec new binary: %v", err))
		return fmt.Errorf("exec new binary: %w", err)
	}
	childFile.Close()
	unix.Close(childFD)
	slog.Info("upgrade: new process started", "pid", cmd.Process.Pid)
	phase(nil, nil, protocol.UpgradePhaseSpawned,
		fmt.Sprintf("new process started (pid %d)", cmd.Process.Pid))

	parentFile := os.NewFile(uintptr(parentFD), "upgrade-parent")
	parentNetConn, err := net.FileConn(parentFile)
	parentFile.Close()
	if err != nil {
		cmd.Process.Kill()
		fail(nil, nil, fmt.Sprintf("file conn: %v", err))
		return fmt.Errorf("file conn: %w", err)
	}
	conn := parentNetConn.(*net.UnixConn)
	defer conn.Close()

	listenerFDs := make([]int, 0, len(s.listeners))
	for _, ln := range s.listeners {
		f, err := transport.ListenerFile(ln)
		if err != nil {
			cmd.Process.Kill()
			fail(nil, nil, fmt.Sprintf("extract listener FD: %v", err))
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
		fail(nil, nil, fmt.Sprintf("send listener FDs: %v", err))
		return fmt.Errorf("send listener FDs: %w", err)
	}
	slog.Info("upgrade: sent listener FDs", "count", len(listenerFDs))
	phase(nil, nil, protocol.UpgradePhaseSentListenerFDs,
		fmt.Sprintf("sent %d listener fd(s)", len(listenerFDs)))

	slog.Info("upgrade: stopping accept loops...")
	s.stopAccepting()
	phase(nil, nil, protocol.UpgradePhaseStoppedAccepting, "stopped accepting new connections")

	slog.Info("upgrade: draining event loop...")
	result := s.drainForUpgrade()
	slog.Info("upgrade: event loop drained")
	// From here the event loop is paused — use direct tree mutation.
	tree, clients := result.tree, result.clients
	phase(tree, clients, protocol.UpgradePhaseDrained, "event loop drained")

	// Stop all PTY readLoops before snapshotting screen state. With the
	// readLoops stopped, no more bytes can mutate hscreen, so the
	// snapshot is guaranteed consistent. Bytes that arrive on the PTY
	// after this point queue in the kernel buffer and will be picked up
	// by the new process's readLoop after handoff — nothing is lost.
	var regionCount int
	result.tree.ForEachRegion(func(id string, r Region) {
		regionCount++
		if pr, ok := r.(*PTYRegion); ok {
			if err := pr.StopActor(); err != nil {
				slog.Warn("upgrade: failed to stop readLoop", "region_id", id, "err", err)
			}
		}
	})
	slog.Info("upgrade: stopped PTY readLoops", "pty_regions", regionCount)
	phase(tree, clients, protocol.UpgradePhaseStoppedReadLoops,
		fmt.Sprintf("stopped %d pty read loop(s)", regionCount))

	// Dup PTY FDs for handoff. Native regions don't have PTYs.
	ptyDups := make(map[string]*os.File) // regionID → dup'd PTY file
	result.tree.ForEachRegion(func(id string, r Region) {
		if pr, ok := r.(*PTYRegion); ok {
			ptyDups[id] = pr.DetachPTY()
		}
	})
	slog.Info("upgrade: detached PTY FDs", "pty_regions", len(ptyDups))

	state := buildUpgradeState(s, result, specs)
	stateJSON, err := json.Marshal(state)
	if err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		fail(nil, nil, fmt.Sprintf("marshal state: %v", err))
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := sendMsg(conn, upgradeMsg{
		Type:  "state",
		State: stateJSON,
	}, nil); err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		fail(nil, nil, fmt.Sprintf("send state: %v", err))
		return fmt.Errorf("send state: %w", err)
	}
	slog.Info("upgrade: sent state", "bytes", len(stateJSON))
	phase(tree, clients, protocol.UpgradePhaseSentState,
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
		fail(nil, nil, fmt.Sprintf("send pty FDs: %v", err))
		return fmt.Errorf("send pty FDs: %w", err)
	}
	slog.Info("upgrade: sent PTY FDs", "count", len(ptyFDs))
	phase(tree, clients, protocol.UpgradePhaseSentPTYFDs,
		fmt.Sprintf("sent %d pty fd(s)", len(ptyFDs)))

	if err := sendMsg(conn, upgradeMsg{Type: "handoff_complete"}, nil); err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		fail(nil, nil, fmt.Sprintf("send handoff_complete: %v", err))
		return fmt.Errorf("send handoff_complete: %w", err)
	}

	resp, _, err := recvMsg(conn, upgradeTimeout)
	if err != nil {
		slog.Error("upgrade: no response from new process", "err", err)
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		fail(nil, nil, fmt.Sprintf("waiting for ready: %v", err))
		return fmt.Errorf("waiting for ready: %w", err)
	}
	if resp.Type == "error" {
		slog.Error("upgrade: new process reported error", "message", resp.Message)
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		fail(nil, nil, fmt.Sprintf("new process error: %s", resp.Message))
		return fmt.Errorf("new process error: %s", resp.Message)
	}
	phase(tree, clients, protocol.UpgradePhaseReady, "new server is ready")

	// Tell clients we're about to close the connections, then drain the
	// write buffers briefly so the message actually goes out over the
	// wire before Close() frees the writeCh. Only then do we close.
	phase(tree, clients, protocol.UpgradePhaseShuttingDown, "old server shutting down")
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
	result.tree.ForEachProgram(func(name string, p config.ProgramConfig) {
		state.Programs[name] = ProgramConfigJSON{
			Name: p.Name, Cmd: p.Cmd, Args: p.Args, Env: p.Env,
		}
	})
	result.tree.ForEachSession(func(name string, _ []string) {
		stackIDs, _ := result.tree.SessionStackIDs(name)
		ss := SessionState{Name: name}
		ss.StackIDs = append(ss.StackIDs, stackIDs...)
		state.Sessions = append(state.Sessions, ss)
	})
	for stackID, se := range result.tree.stacks {
		state.Stacks = append(state.Stacks, StackState{
			ID:        stackID,
			RegionIDs: append([]string(nil), se.regionIDs...),
		})
	}
	// Only serialize PTY regions — native regions are killed on upgrade.
	result.tree.ForEachRegion(func(_ string, r Region) {
		pr, ok := r.(*PTYRegion)
		if !ok {
			slog.Info("upgrade: skipping native region", "region_id", r.ID())
			return
		}
		histState := pr.actor.hscreen.MarshalState()
		state.Regions = append(state.Regions, RegionState{
			ID: pr.id, Name: pr.name, Cmd: pr.cmd, Pid: pr.pid,
			Session: pr.session, Width: pr.actor.width, Height: pr.actor.height,
			Screen: histState,
		})
	})
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

// setUpgradePhase updates the tree's UpgradeNode and broadcasts the
// change to clients. When tree is nil the event loop is still running,
// so the update is routed through it as a setUpgradeReq. When tree is
// non-nil the event loop is paused and we mutate the tree directly.
func (s *Server) setUpgradePhase(tree *ServerTree, clients map[uint32]*Client, phase, message string) {
	node := protocol.UpgradeNode{Active: true, Phase: phase, Message: message}
	if tree == nil {
		resp := make(chan struct{}, 1)
		if !s.send(setUpgradeReq{node: node, resp: resp}) {
			return
		}
		<-resp
		return
	}
	tree.StartTx()
	tree.SetUpgrade(node)
	v, ops := tree.CommitTx()
	if len(ops) == 0 {
		return
	}
	msg := protocol.TreeEvents{Type: "tree_events", Version: v, Ops: ops}
	for _, c := range clients {
		c.SendMessage(msg)
	}
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
	result.tree.ForEachRegion(func(_ string, r Region) {
		pr, ok := r.(*PTYRegion)
		if !ok {
			return // native regions don't have readLoops to restart
		}
		if err := pr.ResumeActor(s.destroyRegion); err != nil {
			slog.Error("upgrade: rollback failed to restart readLoop", "region_id", pr.id, "err", err)
		}
	})
	slog.Warn("upgrade: rollback complete, but listeners were closed; restart may be needed")
}

type upgradeReq struct{ resp chan upgradeResult }
type resumeUpgradeReq struct{}

type setUpgradeReq struct {
	node protocol.UpgradeNode
	resp chan struct{}
}

func (r setUpgradeReq) handle(st *eventLoopState) {
	st.tree.SetUpgrade(r.node)
	if r.resp != nil {
		r.resp <- struct{}{}
	}
}

func (r upgradeReq) handle(st *eventLoopState) {
	r.resp <- upgradeResult{
		tree:    st.tree,
		clients: st.tree.ClientMap(),
	}
	// Pause: wait for resume (rollback) or done (successful upgrade).
	select {
	case <-st.srv.requests:
		// resumeUpgradeReq — put state back and continue.
	case <-st.srv.done:
		st.srv.shutdownResp <- shutdownResult{}
		st.exit = true
	}
}

// resumeUpgradeReq is a no-op; it is consumed by the pause select
// in upgradeReq.handle above.
func (r resumeUpgradeReq) handle(st *eventLoopState) {}

type upgradeResult struct {
	tree *ServerTree
	// clients is the snapshot of connected clients at the moment the
	// event loop paused. The event loop goroutine does not touch the
	// map after returning this result (it blocks on s.requests waiting
	// for resume/done), so HandleUpgrade can iterate it safely and
	// broadcast upgrade status messages. HandleUpgrade is responsible
	// for closing every client at the end of the upgrade.
	clients map[uint32]*Client
}
