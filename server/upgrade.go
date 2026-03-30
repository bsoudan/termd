package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
	"termd/config"
	"termd/transport"
)

const upgradeTimeout = 10 * time.Second

// HandleUpgrade performs a live upgrade by spawning a new binary and
// handing off all listeners, PTY FDs, and terminal state.
func (s *Server) HandleUpgrade(specs []string, sshCfg transport.SSHListenerConfig) error {
	newBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	slog.Info("upgrade: starting", "binary", newBin)

	// Create socketpair.
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("socketpair: %w", err)
	}
	parentFD, childFD := fds[0], fds[1]

	// Exec new binary with child end as fd 3.
	childFile := os.NewFile(uintptr(childFD), "upgrade-child")
	cmd := exec.Command(newBin, "--upgrade-fd", "3")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{childFile}
	if err := cmd.Start(); err != nil {
		unix.Close(parentFD)
		unix.Close(childFD)
		return fmt.Errorf("exec new binary: %w", err)
	}
	childFile.Close()
	unix.Close(childFD)
	slog.Info("upgrade: new process started", "pid", cmd.Process.Pid)

	parentFile := os.NewFile(uintptr(parentFD), "upgrade-parent")
	parentNetConn, err := net.FileConn(parentFile)
	parentFile.Close()
	if err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("file conn: %w", err)
	}
	conn := parentNetConn.(*net.UnixConn)
	defer conn.Close()

	// Send listener FDs + specs in one message.
	listenerFDs := make([]int, 0, len(s.listeners))
	for _, ln := range s.listeners {
		f, err := transport.ListenerFile(ln)
		if err != nil {
			cmd.Process.Kill()
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
		return fmt.Errorf("send listener FDs: %w", err)
	}
	slog.Info("upgrade: sent listener FDs", "count", len(listenerFDs))

	// Stop accepting, disconnect clients, drain event loop.
	slog.Info("upgrade: stopping accept loops...")
	s.stopAccepting()
	slog.Info("upgrade: disconnecting clients...")
	s.disconnectAllClients()
	slog.Info("upgrade: draining event loop...")
	result := s.drainForUpgrade()
	slog.Info("upgrade: event loop drained")

	// Stop readLoops — StopReadLoop dups the FD and closes the original.
	ptyDups := make(map[string]*os.File) // regionID → dup'd PTY file
	for id, r := range result.regions {
		ptyDups[id] = r.StopReadLoop()
	}
	slog.Info("upgrade: stopped readLoops", "regions", len(result.regions))

	// Build and send state.
	state := buildUpgradeState(s, result, specs)
	stateJSON, err := json.Marshal(state)
	if err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := sendMsg(conn, upgradeMsg{
		Type:  "state",
		State: stateJSON,
	}, nil); err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		return fmt.Errorf("send state: %w", err)
	}
	slog.Info("upgrade: sent state", "bytes", len(stateJSON))

	// Send PTY FDs (using the dup'd copies from StopReadLoop).
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
		return fmt.Errorf("send pty FDs: %w", err)
	}
	slog.Info("upgrade: sent PTY FDs", "count", len(ptyFDs))

	// Send handoff_complete.
	if err := sendMsg(conn, upgradeMsg{Type: "handoff_complete"}, nil); err != nil {
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		return fmt.Errorf("send handoff_complete: %w", err)
	}

	// Wait for "ready" or "error".
	resp, _, err := recvMsg(conn, upgradeTimeout)
	if err != nil {
		slog.Error("upgrade: no response from new process", "err", err)
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		return fmt.Errorf("waiting for ready: %w", err)
	}
	if resp.Type == "error" {
		slog.Error("upgrade: new process reported error", "message", resp.Message)
		s.resumeAfterFailedUpgrade(result)
		cmd.Process.Kill()
		return fmt.Errorf("new process error: %s", resp.Message)
	}

	slog.Info("upgrade: success, old process exiting")
	return nil
}

func buildUpgradeState(s *Server, result upgradeResult, specs []string) *UpgradeState {
	state := &UpgradeState{
		Version:       s.version,
		ListenerSpecs: specs,
		NextClientID:  s.nextClientID.Load(),
		Programs:      make(map[string]ProgramConfigJSON),
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
	for _, r := range result.regions {
		r.mu.Lock()
		histState := r.hscreen.MarshalState()
		r.mu.Unlock()
		state.Regions = append(state.Regions, RegionState{
			ID: r.id, Name: r.name, Cmd: r.cmd, Pid: r.pid,
			Session: r.session, Width: r.width, Height: r.height,
			Screen: histState,
		})
	}
	return state
}

func (s *Server) stopAccepting() {
	// Just set the flag. Accept loops will exit when Shutdown() closes
	// the listeners. During upgrade, the accept loops are irrelevant
	// since the new process takes over the dup'd listener FDs.
	s.shutdown.Store(true)
}

func (s *Server) disconnectAllClients() {
	resp := make(chan struct{})
	if s.send(disconnectAllReq{resp: resp}) {
		<-resp
	}
}

func (s *Server) drainForUpgrade() upgradeResult {
	resp := make(chan upgradeResult)
	s.send(upgradeReq{resp: resp})
	return <-resp
}

func (s *Server) resumeAfterFailedUpgrade(result upgradeResult) {
	slog.Warn("upgrade: rolling back")
	s.send(resumeUpgradeReq{})
	for _, r := range result.regions {
		r.stopRead = make(chan struct{})
		r.readerDone = make(chan struct{})
		go r.readLoop()
	}
	slog.Warn("upgrade: rollback complete, but listeners were closed; restart may be needed")
}

type disconnectAllReq struct{ resp chan struct{} }
type upgradeReq struct{ resp chan upgradeResult }
type resumeUpgradeReq struct{}

type upgradeResult struct {
	regions  map[string]*Region
	sessions map[string]*Session
	programs map[string]config.ProgramConfig
}
