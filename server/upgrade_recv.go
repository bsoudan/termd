package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"syscall"

	"termd/config"
	"termd/transport"
)

// RecvUpgrade receives a live upgrade handoff from an old termd process.
func RecvUpgrade(fd int, sshCfg transport.SSHListenerConfig) (*Server, []net.Listener, error) {
	file := os.NewFile(uintptr(fd), "upgrade-recv")
	netConn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("file conn: %w", err)
	}
	conn := netConn.(*net.UnixConn)
	defer conn.Close()

	// 1. Receive listener FDs.
	msg, files, err := recvMsg(conn, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("recv listener msg: %w", err)
	}
	if msg.Type != "listener_fds" {
		return nil, nil, fmt.Errorf("expected listener_fds, got %s", msg.Type)
	}
	slog.Info("upgrade-recv: got listener FDs", "count", len(files), "specs", msg.Specs)

	listeners := make([]net.Listener, len(files))
	for i, f := range files {
		ln, err := transport.ListenFromFile(f, msg.Specs[i], sshCfg)
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("reconstruct listener %s: %w", msg.Specs[i], err)
		}
		listeners[i] = ln
	}

	// 2. Receive state.
	msg, _, err = recvMsg(conn, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("recv state: %w", err)
	}
	if msg.Type != "state" {
		return nil, nil, fmt.Errorf("expected state, got %s", msg.Type)
	}
	var state UpgradeState
	if err := json.Unmarshal(msg.State, &state); err != nil {
		return nil, nil, fmt.Errorf("unmarshal state: %w", err)
	}
	slog.Info("upgrade-recv: got state", "regions", len(state.Regions), "sessions", len(state.Sessions))

	// 3. Receive PTY FDs.
	msg, ptyFiles, err := recvMsg(conn, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("recv pty FDs: %w", err)
	}
	if msg.Type != "pty_fds" {
		return nil, nil, fmt.Errorf("expected pty_fds, got %s", msg.Type)
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
		return nil, nil, fmt.Errorf("recv handoff_complete: %w", err)
	}
	if msg.Type != "handoff_complete" {
		return nil, nil, fmt.Errorf("expected handoff_complete, got %s", msg.Type)
	}

	// 5. Reconstruct server.
	cfg := config.ServerConfig{}
	cfg.Sessions.DefaultName = state.SessionsCfg.DefaultName
	cfg.Sessions.DefaultPrograms = state.SessionsCfg.DefaultPrograms
	for _, p := range state.Programs {
		cfg.Programs = append(cfg.Programs, config.ProgramConfig{
			Name: p.Name, Cmd: p.Cmd, Args: p.Args, Env: p.Env,
		})
	}

	srv := NewServer(listeners, state.Version, cfg)
	srv.nextClientID.Store(state.NextClientID)

	for _, rs := range state.Regions {
		ptmxFile, ok := ptyByRegion[rs.ID]
		if !ok {
			slog.Warn("upgrade-recv: no PTY FD for region", "region_id", rs.ID)
			continue
		}
		region := RestoreRegion(rs.ID, rs.Name, rs.Cmd, rs.Session, rs.Pid, rs.Width, rs.Height, ptmxFile, rs.Screen)
		resp := make(chan struct{})
		srv.send(restoreRegionReq{region: region, session: rs.Session, resp: resp})
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

	return srv, listeners, nil
}

type restoreRegionReq struct {
	region  *Region
	session string
	resp    chan struct{}
}
