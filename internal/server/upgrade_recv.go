package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"syscall"

	"nxtermd/internal/config"
	"nxtermd/internal/transport"
)

// RecvUpgrade receives a live upgrade handoff from an old nxtermd process.
// The version parameter is the new binary's compiled-in version, which
// takes precedence over the version in the upgrade state (from the old binary).
func RecvUpgrade(fd int, sshCfg transport.SSHListenerConfig, version string) (*Server, []net.Listener, []string, error) {
	file := os.NewFile(uintptr(fd), "upgrade-recv")
	netConn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("file conn: %w", err)
	}
	conn := netConn.(*net.UnixConn)
	defer conn.Close()

	// 1. Receive listener FDs.
	msg, files, err := recvMsg(conn, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("recv listener msg: %w", err)
	}
	if msg.Type != "listener_fds" {
		return nil, nil, nil, fmt.Errorf("expected listener_fds, got %s", msg.Type)
	}
	specs := msg.Specs
	slog.Info("upgrade-recv: got listener FDs", "count", len(files), "specs", specs)

	listeners := make([]net.Listener, len(files))
	for i, f := range files {
		ln, err := transport.ListenFromFile(f, msg.Specs[i], sshCfg)
		f.Close()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("reconstruct listener %s: %w", msg.Specs[i], err)
		}
		listeners[i] = ln
	}

	// 2. Receive state.
	msg, _, err = recvMsg(conn, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("recv state: %w", err)
	}
	if msg.Type != "state" {
		return nil, nil, nil, fmt.Errorf("expected state, got %s", msg.Type)
	}
	var state UpgradeState
	if err := json.Unmarshal(msg.State, &state); err != nil {
		return nil, nil, nil, fmt.Errorf("unmarshal state: %w", err)
	}
	slog.Info("upgrade-recv: got state", "regions", len(state.Regions), "sessions", len(state.Sessions))

	// 3. Receive PTY FDs.
	msg, ptyFiles, err := recvMsg(conn, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("recv pty FDs: %w", err)
	}
	if msg.Type != "pty_fds" {
		return nil, nil, nil, fmt.Errorf("expected pty_fds, got %s", msg.Type)
	}
	ptyByRegion := make(map[string]*os.File, len(msg.RegionIDs))
	for i, id := range msg.RegionIDs {
		if i < len(ptyFiles) {
			ptyByRegion[id] = ptyFiles[i]
		}
	}

	// 4. Receive handoff_complete.
	msg, _, err = recvMsg(conn, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("recv handoff_complete: %w", err)
	}
	if msg.Type != "handoff_complete" {
		return nil, nil, nil, fmt.Errorf("expected handoff_complete, got %s", msg.Type)
	}

	// 5. Reconstruct server.
	// Re-read the config file so configuration changes take effect
	// without requiring a full restart.
	cfg, err := config.LoadServerConfig("")
	if err != nil {
		slog.Warn("upgrade-recv: failed to reload config, using inherited state", "err", err)
		cfg = config.ServerConfig{}
		cfg.Sessions.DefaultName = state.SessionsCfg.DefaultName
		cfg.Sessions.DefaultPrograms = state.SessionsCfg.DefaultPrograms
		cfg.Upgrade.BinariesDir = state.BinariesDir
		for _, p := range state.Programs {
			cfg.Programs = append(cfg.Programs, config.ProgramConfig{
				Name: p.Name, Cmd: p.Cmd, Args: p.Args, Env: p.Env,
			})
		}
	}

	srv := NewServer(listeners, version, cfg)
	srv.nextClientID.Store(state.NextClientID)

	// Build regionID→stackID map from upgrade state for stack reconstruction.
	regionToStack := make(map[string]string)
	for _, ss := range state.Stacks {
		for _, rid := range ss.RegionIDs {
			regionToStack[rid] = ss.ID
		}
	}
	// Backward compatibility: if no stacks in state, create one per region
	// using the legacy SessionState.RegionIDs.
	if len(state.Stacks) == 0 {
		for _, sess := range state.Sessions {
			for _, rid := range sess.RegionIDs {
				regionToStack[rid] = generateUUID()
			}
		}
	}

	for _, rs := range state.Regions {
		ptmxFile, ok := ptyByRegion[rs.ID]
		if !ok {
			slog.Warn("upgrade-recv: no PTY FD for region", "region_id", rs.ID)
			continue
		}
		region := RestoreRegion(rs.ID, rs.Name, rs.Cmd, rs.Session, rs.Pid, rs.Width, rs.Height, ptmxFile, rs.Screen, srv.destroyRegion)
		resp := make(chan struct{})
		srv.send(restoreRegionReq{region: region, session: rs.Session, stackID: regionToStack[rs.ID], resp: resp})
		<-resp
	}

	for _, rs := range state.Regions {
		if rs.Pid > 0 {
			syscall.Kill(rs.Pid, syscall.SIGWINCH)
		}
	}

	slog.Info("upgrade-recv: reconstruction complete", "regions", len(state.Regions))

	if err := sendMsg(conn, upgradeMsg{Type: "ready"}, nil); err != nil {
		slog.Warn("upgrade-recv: failed to send ready", "err", err)
	}

	return srv, listeners, specs, nil
}

type restoreRegionReq struct {
	region  Region
	session string
	stackID string // stack to add the region to (empty = create new)
	resp    chan struct{}
}

func (r restoreRegionReq) handle(st *eventLoopState) {
	r.region.SetSession(r.session)
	st.tree.SetRegion(r.region)
	created := st.tree.EnsureSession(r.session)
	stackID := r.stackID
	if stackID == "" {
		stackID = generateUUID()
	}
	if _, ok := st.tree.StackRegionIDs(stackID); !ok {
		st.tree.SetStack(stackID, r.session)
		st.tree.AddStackToSession(r.session, stackID)
	}
	st.tree.AddRegionToStack(stackID, r.region.ID())
	st.tree.SetRegionStackID(r.region.ID(), stackID)
	r.resp <- struct{}{}
	if created {
		st.notifySessionsChanged()
	}
}
