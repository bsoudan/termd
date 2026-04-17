package server

// handlers.go contains the protocol message dispatch table and all
// handler functions. Handlers are standalone functions (not Client
// methods) — Client is a pure I/O actor that forwards raw messages
// here via Server.dispatch.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"syscall"
	"time"

	"nxtermd/internal/config"
	"nxtermd/internal/protocol"
)

// scrollbackChunkSize is the max lines per scrollback response chunk.
const scrollbackChunkSize = 1000

// chunkSize is the raw byte size per client binary download chunk.
const chunkSize = 64 * 1024 // 64KB raw → ~85KB base64

// msgHandler handles a raw protocol message from a client.
type msgHandler = func(s *Server, c *Client, payload []byte, reply func(any))

func withMsg[T any](fn func(*Server, *Client, T, func(any))) msgHandler {
	return func(s *Server, c *Client, payload []byte, reply func(any)) {
		var msg T
		if json.Unmarshal(payload, &msg) == nil {
			fn(s, c, msg, reply)
		}
	}
}

func withMsgOnly[T any](fn func(*Server, *Client, T)) msgHandler {
	return func(s *Server, c *Client, payload []byte, _ func(any)) {
		var msg T
		if json.Unmarshal(payload, &msg) == nil {
			fn(s, c, msg)
		}
	}
}

func withReplyOnly(fn func(*Server, *Client, func(any))) msgHandler {
	return func(s *Server, c *Client, _ []byte, reply func(any)) {
		fn(s, c, reply)
	}
}

var messageHandlers = map[string]msgHandler{
	"identify":                withMsgOnly(handleIdentify),
	"spawn_request":           withMsg(handleSpawn),
	"subscribe_request":       withMsg(handleSubscribe),
	"input":                   withMsgOnly(handleInput),
	"resize_request":          withMsg(handleResize),
	"list_regions_request":    withMsg(handleListRegions),
	"get_screen_request":      withMsg(handleGetScreen),
	"get_scrollback_request":  withMsg(handleGetScrollback),
	"kill_region_request":     withMsg(handleKillRegion),
	"list_clients_request":    withReplyOnly(handleListClients),
	"kill_client_request":     withMsg(handleKillClient),
	"unsubscribe_request":     withMsg(handleUnsubscribe),
	"session_connect_request": withMsg(handleSessionConnect),
	"list_sessions_request":   withReplyOnly(handleListSessions),
	"list_programs_request":   withReplyOnly(handleListPrograms),
	"add_program_request":     withMsg(handleAddProgram),
	"remove_program_request":  withMsg(handleRemoveProgram),
	"upgrade_check_request":   withMsg(handleUpgradeCheck),
	"server_upgrade_request":  withReplyOnly(handleServerUpgrade),
	"client_binary_request":   withMsg(handleClientBinaryDownload),
	"overlay_register":        withMsg(handleOverlayRegister),
	"overlay_render":          withMsgOnly(handleOverlayRender),
	"overlay_clear":           withMsgOnly(handleOverlayClear),
	"native_region_spawn_request": withMsg(handleNativeRegionSpawn),
	"native_region_output":        withMsgOnly(handleNativeRegionOutput),
	"tree_resync_request": func(s *Server, c *Client, _ []byte, _ func(any)) {
		s.send(treeSnapshotReq{clientID: c.id})
	},
	"disconnect": func(_ *Server, c *Client, _ []byte, _ func(any)) {
		slog.Info("client disconnecting gracefully", "client_id", c.id)
		c.Close()
	},
}

// envelope extracts the type and req_id from a protocol message.
type envelope struct {
	Type  string `json:"type"`
	ReqID uint64 `json:"req_id,omitempty"`
}

// dispatch routes a raw protocol message from a client to the
// appropriate handler.
func (s *Server) dispatch(c *Client, line []byte) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		slog.Debug("parse error", "client_id", c.id, "err", err)
		return
	}
	handler, ok := messageHandlers[env.Type]
	if !ok {
		slog.Debug("unknown message type", "client_id", c.id, "type", env.Type)
		return
	}
	handler(s, c, line, c.replyFunc(env.ReqID))
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func handleIdentify(s *Server, c *Client, msg protocol.Identify) {
	ident := clientIdentity{
		hostname: msg.Hostname,
		username: msg.Username,
		pid:      msg.Pid,
		process:  msg.Process,
	}
	c.identity.Store(&ident)

	// Route through event loop so the tree's client node is updated.
	s.send(identifyReq{clientID: c.id, identity: ident})

	slog.Debug("client identified", "client_id", c.id,
		"hostname", msg.Hostname, "username", msg.Username,
		"pid", msg.Pid, "process", msg.Process)
}

func handleNativeRegionSpawn(s *Server, c *Client, msg protocol.NativeRegionSpawnRequest, reply func(any)) {
	if msg.Session == "" || msg.Name == "" {
		reply(protocol.NativeRegionSpawnResponse{
			Type:    "native_region_spawn_response",
			Error:   true,
			Message: "session and name are required",
		})
		return
	}

	width := uint16(msg.Width)
	height := uint16(msg.Height)
	region, err := s.SpawnNativeRegion(c, msg.Session, msg.Name, width, height)
	if err != nil {
		reply(protocol.NativeRegionSpawnResponse{
			Type:    "native_region_spawn_response",
			Error:   true,
			Message: err.Error(),
		})
		return
	}

	reply(protocol.NativeRegionSpawnResponse{
		Type:     "native_region_spawn_response",
		RegionID: region.ID(),
		Width:    region.Width(),
		Height:   region.Height(),
	})
}

func handleNativeRegionOutput(s *Server, c *Client, msg protocol.NativeRegionOutput) {
	region := s.FindRegion(msg.RegionID)
	if region == nil {
		return
	}
	nr, ok := region.(*NativeRegion)
	if !ok {
		slog.Debug("native_region_output for non-native region", "region_id", msg.RegionID)
		return
	}
	if nr.Driver() != c {
		slog.Debug("native_region_output from non-owner client",
			"region_id", msg.RegionID, "client_id", c.id)
		return
	}
	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return
	}
	nr.Feed(data)
}

func handleSpawn(s *Server, c *Client, msg protocol.SpawnRequest, reply func(any)) {
	sessionName := msg.Session
	if sessionName == "" {
		sessionName = s.GetClientSession(c.id)
	}
	if sessionName == "" {
		sessionName = s.sessionsCfg.DefaultName
	}

	programName := msg.Program
	if programName == "" {
		programName = "default"
	}

	region, err := s.SpawnProgram(sessionName, programName)
	if err != nil {
		reply(protocol.SpawnResponse{
			Type:     "spawn_response",
			RegionID: "",
			Name:     "",
			Error:    true,
			Message:  err.Error(),
		})
		return
	}

	reply(protocol.SpawnResponse{
		Type:     "spawn_response",
		RegionID: region.ID(),
		Name:     region.Name(),
		Error:    false,
		Message:  "",
	})
	// Note: region_created broadcast removed — clients are notified
	// via tree_events from the spawnRegionReq handler in the event loop.
}

func handleSubscribe(s *Server, c *Client, msg protocol.SubscribeRequest, reply func(any)) {
	if len(msg.RegionID) != 36 {
		reply(protocol.SubscribeResponse{
			Type:     "subscribe_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "invalid region_id length",
		})
		return
	}

	region, _ := s.Subscribe(c.id, msg.RegionID)
	if region == nil {
		reply(protocol.SubscribeResponse{
			Type:     "subscribe_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	// Note: the initial screen_update snapshot is sent inside the
	// subscribeReq event-loop handler, before the client is added to
	// regionSubs. This ordering guarantees the snapshot reaches the
	// client's writeCh ahead of any terminal_events the watcher might
	// emit. Sending it from here would race with the watcher.

	reply(protocol.SubscribeResponse{
		Type:     "subscribe_response",
		RegionID: region.ID(),
		Error:    false,
		Message:  "",
	})

	slog.Debug("client subscribed", "client_id", c.id, "region_id", region.ID())
}

func handleUnsubscribe(s *Server, c *Client, msg protocol.UnsubscribeRequest, reply func(any)) {
	s.Unsubscribe(c.id)
	reply(protocol.UnsubscribeResponse{
		Type:     "unsubscribe_response",
		RegionID: msg.RegionID,
	})
	slog.Debug("client unsubscribed", "client_id", c.id, "region_id", msg.RegionID)
}

func handleInput(s *Server, _ *Client, msg protocol.InputMsg) {
	region, overlayClient := s.RouteInput(msg.RegionID)
	if overlayClient != nil {
		overlayClient.SendMessage(protocol.OverlayInput{
			Type:     "overlay_input",
			RegionID: msg.RegionID,
			Data:     msg.Data,
		})
		return
	}
	if region == nil {
		return
	}

	decoded, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return
	}
	region.WriteInput(decoded)
}

func handleResize(s *Server, _ *Client, msg protocol.ResizeRequest, reply func(any)) {
	region := s.FindRegion(msg.RegionID)
	if region == nil {
		reply(protocol.ResizeResponse{
			Type:     "resize_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	if err := region.Resize(msg.Width, msg.Height); err != nil {
		reply(protocol.ResizeResponse{
			Type:     "resize_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  err.Error(),
		})
		return
	}

	reply(protocol.ResizeResponse{
		Type:     "resize_response",
		RegionID: region.ID(),
		Error:    false,
		Message:  "",
	})
}

func handleListRegions(s *Server, _ *Client, msg protocol.ListRegionsRequest, reply func(any)) {
	infos := s.getRegionInfos(msg.Session)
	reply(protocol.ListRegionsResponse{
		Type:    "list_regions_response",
		Regions: infos,
		Error:   false,
		Message: "",
	})
}

func handleGetScrollback(s *Server, _ *Client, msg protocol.GetScrollbackRequest, reply func(any)) {
	region := s.FindRegion(msg.RegionID)
	if region == nil {
		reply(protocol.GetScrollbackResponse{
			Type:     "get_scrollback_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	lines := region.GetScrollback()
	total := len(lines)
	regionID := region.ID()

	// Send chunks newest-first so the client can render recent lines
	// (adjacent to its local history) before older ones stream in.
	for len(lines) > scrollbackChunkSize {
		start := len(lines) - scrollbackChunkSize
		chunk := lines[start:]
		lines = lines[:start]
		reply(protocol.GetScrollbackResponse{
			Type:     "get_scrollback_response",
			RegionID: regionID,
			Lines:    chunk,
			Total:    total,
		})
	}

	reply(protocol.GetScrollbackResponse{
		Type:     "get_scrollback_response",
		RegionID: regionID,
		Lines:    lines,
		Total:    total,
		Done:     true,
	})
}

func handleGetScreen(s *Server, _ *Client, msg protocol.GetScreenRequest, reply func(any)) {
	region := s.FindRegion(msg.RegionID)
	if region == nil {
		reply(protocol.GetScreenResponse{
			Type:     "get_screen_response",
			RegionID: msg.RegionID,
			Lines:    []string{},
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	snap := region.Snapshot()
	reply(protocol.GetScreenResponse{
		Type:          "get_screen_response",
		RegionID:      region.ID(),
		CursorRow:     snap.CursorRow,
		CursorCol:     snap.CursorCol,
		Lines:         snap.Lines,
		Cells:         snap.Cells,
		Modes:         snap.Modes,
		Title:         snap.Title,
		IconName:      snap.IconName,
		ScrollbackLen: snap.ScrollbackLen,
		Error:         false,
		Message:       "",
	})
}

func handleKillRegion(s *Server, _ *Client, msg protocol.KillRegionRequest, reply func(any)) {
	if s.KillRegion(msg.RegionID) {
		reply(protocol.KillRegionResponse{
			Type:     "kill_region_response",
			RegionID: msg.RegionID,
			Error:    false,
			Message:  "",
		})
	} else {
		reply(protocol.KillRegionResponse{
			Type:     "kill_region_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
	}
}

func handleListClients(s *Server, _ *Client, reply func(any)) {
	infos := s.getClientInfos()
	reply(protocol.ListClientsResponse{
		Type:    "list_clients_response",
		Clients: infos,
		Error:   false,
		Message: "",
	})
}

func handleKillClient(s *Server, c *Client, msg protocol.KillClientRequest, reply func(any)) {
	if msg.ClientID == c.id {
		reply(protocol.KillClientResponse{
			Type:     "kill_client_response",
			ClientID: msg.ClientID,
			Error:    true,
			Message:  "cannot kill self",
		})
		return
	}

	if s.KillClient(msg.ClientID) {
		reply(protocol.KillClientResponse{
			Type:     "kill_client_response",
			ClientID: msg.ClientID,
			Error:    false,
			Message:  "",
		})
	} else {
		reply(protocol.KillClientResponse{
			Type:     "kill_client_response",
			ClientID: msg.ClientID,
			Error:    true,
			Message:  "client not found",
		})
	}
}

func handleSessionConnect(s *Server, c *Client, msg protocol.SessionConnectRequest, reply func(any)) {
	sessionName := msg.Session
	if sessionName == "" {
		sessionName = s.sessionsCfg.DefaultName
	}

	sessionName, infos, err := s.findOrCreateSession(sessionName, msg.Width, msg.Height)
	if err != nil {
		reply(protocol.SessionConnectResponse{
			Type:    "session_connect_response",
			Session: sessionName,
			Error:   true,
			Message: fmt.Sprintf("session connect: %v", err),
		})
		return
	}

	s.SetClientSession(c.id, sessionName)

	reply(protocol.SessionConnectResponse{
		Type:     "session_connect_response",
		Session:  sessionName,
		Regions:  infos,
		Programs: s.listProgramInfos(),
		Error:    false,
		Message:  "",
	})

	slog.Debug("client connected to session", "client_id", c.id, "session", sessionName)
}

func handleListSessions(s *Server, _ *Client, reply func(any)) {
	infos := s.getSessionInfos()
	reply(protocol.ListSessionsResponse{
		Type:     "list_sessions_response",
		Sessions: infos,
		Error:    false,
		Message:  "",
	})
}

func handleListPrograms(s *Server, _ *Client, reply func(any)) {
	infos := s.listProgramInfos()
	reply(protocol.ListProgramsResponse{
		Type:     "list_programs_response",
		Programs: infos,
		Error:    false,
		Message:  "",
	})
}

func handleAddProgram(s *Server, _ *Client, msg protocol.AddProgramRequest, reply func(any)) {
	p := config.ProgramConfig{
		Name: msg.Name,
		Cmd:  msg.Cmd,
		Args: msg.Args,
		Env:  msg.Env,
	}
	if err := s.addProgram(p); err != nil {
		reply(protocol.AddProgramResponse{
			Type:    "add_program_response",
			Name:    msg.Name,
			Error:   true,
			Message: err.Error(),
		})
		return
	}
	reply(protocol.AddProgramResponse{
		Type: "add_program_response",
		Name: msg.Name,
	})
}

func handleRemoveProgram(s *Server, _ *Client, msg protocol.RemoveProgramRequest, reply func(any)) {
	if err := s.removeProgram(msg.Name); err != nil {
		reply(protocol.RemoveProgramResponse{
			Type:    "remove_program_response",
			Name:    msg.Name,
			Error:   true,
			Message: err.Error(),
		})
		return
	}
	reply(protocol.RemoveProgramResponse{
		Type: "remove_program_response",
		Name: msg.Name,
	})
}

// ── Overlay handlers ─────────────────────────────────────────────────────────

func handleOverlayRegister(s *Server, c *Client, msg protocol.OverlayRegisterRequest, reply func(any)) {
	resp := make(chan overlayRegisterResult, 1)
	if !s.send(overlayRegisterReq{client: c, regionID: msg.RegionID, resp: resp}) {
		return
	}
	result := <-resp
	if result.err != "" {
		reply(protocol.OverlayRegisterResponse{
			Type:     "overlay_register_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  result.err,
		})
		return
	}
	reply(protocol.OverlayRegisterResponse{
		Type:     "overlay_register_response",
		RegionID: msg.RegionID,
		Width:    result.width,
		Height:   result.height,
	})
}

func handleOverlayRender(s *Server, c *Client, msg protocol.OverlayRender) {
	region := s.FindRegion(msg.RegionID)
	if region == nil {
		return
	}
	region.RenderOverlay(c.id, msg.Cells, msg.CursorRow, msg.CursorCol, msg.Modes)
}

func handleOverlayClear(s *Server, c *Client, msg protocol.OverlayClear) {
	s.send(overlayClearReq{clientID: c.id, regionID: msg.RegionID})
}

// ── Upgrade handlers ─────────────────────────────────────────────────────────

// handleUpgradeCheck reports whether newer server and/or client binaries
// are available in the configured binaries directory.
func handleUpgradeCheck(s *Server, c *Client, msg protocol.UpgradeCheckRequest, reply func(any)) {
	slog.Debug("upgrade check", "client_id", c.id,
		"client_version", msg.ClientVersion, "os", msg.OS, "arch", msg.Arch)

	dir := s.binariesDir
	if dir == "" {
		reply(protocol.UpgradeCheckResponse{
			Type: "upgrade_check_response",
		})
		return
	}

	resp := protocol.UpgradeCheckResponse{Type: "upgrade_check_response"}

	// Check server binary.
	serverBin := upgradeBinPath(dir, "nxtermd", runtime.GOOS, runtime.GOARCH)
	if v, err := binaryVersion(serverBin); err != nil {
		slog.Warn("upgrade check: server binary version failed",
			"path", serverBin, "err", err)
	} else if v != s.version {
		resp.ServerAvailable = true
		resp.ServerVersion = v
	}

	// Check client binary.
	clientBin := upgradeBinPath(dir, "nxterm", msg.OS, msg.Arch)
	if v, err := binaryVersion(clientBin); err != nil {
		slog.Warn("upgrade check: client binary version failed",
			"path", clientBin, "err", err)
	} else if v != msg.ClientVersion {
		resp.ClientAvailable = true
		resp.ClientVersion = v
	}

	slog.Debug("upgrade check result", "client_id", c.id,
		"server_available", resp.ServerAvailable, "server_bin_ver", resp.ServerVersion,
		"client_available", resp.ClientAvailable, "client_bin_ver", resp.ClientVersion,
		"running_server_ver", s.version, "running_client_ver", msg.ClientVersion)
	reply(resp)
}

// handleServerUpgrade copies the new server binary over the current
// executable, sends a success response, then signals itself with SIGUSR2
// to trigger the existing live-upgrade machinery.
func handleServerUpgrade(s *Server, c *Client, reply func(any)) {
	slog.Info("server upgrade requested", "client_id", c.id)

	dir := s.binariesDir
	if dir == "" {
		reply(protocol.ServerUpgradeResponse{
			Type: "server_upgrade_response", Error: true,
			Message: "no binaries directory configured",
		})
		return
	}

	srcPath := upgradeBinPath(dir, "nxtermd", runtime.GOOS, runtime.GOARCH)
	dstPath, err := os.Executable()
	if err != nil {
		reply(protocol.ServerUpgradeResponse{
			Type: "server_upgrade_response", Error: true,
			Message: fmt.Sprintf("os.Executable: %v", err),
		})
		return
	}

	if err := copyFile(srcPath, dstPath); err != nil {
		reply(protocol.ServerUpgradeResponse{
			Type: "server_upgrade_response", Error: true,
			Message: fmt.Sprintf("copy binary: %v", err),
		})
		return
	}
	slog.Info("server binary replaced", "src", srcPath, "dst", dstPath)

	reply(protocol.ServerUpgradeResponse{Type: "server_upgrade_response"})

	// Trigger the existing live-upgrade path.
	slog.Info("sending SIGUSR2 to self")
	syscall.Kill(os.Getpid(), syscall.SIGUSR2)
}

// handleClientBinaryDownload streams the requested client binary in chunks,
// followed by a final response with the SHA-256 hash. Streaming runs in a
// separate goroutine to avoid blocking the client's readLoop.
func handleClientBinaryDownload(s *Server, c *Client, msg protocol.ClientBinaryRequest, reply func(any)) {
	slog.Debug("client binary download", "client_id", c.id,
		"os", msg.OS, "arch", msg.Arch, "offset", msg.Offset)

	dir := s.binariesDir
	if dir == "" {
		reply(protocol.ClientBinaryResponse{
			Type: "client_binary_response", Error: true,
			Message: "no binaries directory configured",
		})
		return
	}

	path := upgradeBinPath(dir, "nxterm", msg.OS, msg.Arch)
	f, err := os.Open(path)
	if err != nil {
		reply(protocol.ClientBinaryResponse{
			Type: "client_binary_response", Error: true,
			Message: fmt.Sprintf("open binary: %v", err),
		})
		return
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		reply(protocol.ClientBinaryResponse{
			Type: "client_binary_response", Error: true,
			Message: fmt.Sprintf("stat binary: %v", err),
		})
		return
	}

	if msg.Offset > 0 {
		if _, err := f.Seek(msg.Offset, io.SeekStart); err != nil {
			f.Close()
			reply(protocol.ClientBinaryResponse{
				Type: "client_binary_response", Error: true,
				Message: fmt.Sprintf("seek: %v", err),
			})
			return
		}
	}

	// Stream in a goroutine so the readLoop isn't blocked during transfer.
	go streamBinary(c, f, info.Size(), msg.Offset, path, reply)
}

func streamBinary(c *Client, f *os.File, fileSize, startOffset int64, path string, reply func(any)) {
	defer f.Close()

	hasher := sha256.New()
	if startOffset > 0 {
		f2, _ := os.Open(path)
		if f2 != nil {
			io.CopyN(hasher, f2, startOffset)
			f2.Close()
		}
	}

	buf := make([]byte, chunkSize)
	offset := startOffset
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			hasher.Write(buf[:n])
			final := readErr == io.EOF || (offset+int64(n)) >= fileSize
			c.sendReply(protocol.ClientBinaryChunk{
				Type:   "client_binary_chunk",
				Offset: offset,
				Data:   base64.StdEncoding.EncodeToString(buf[:n]),
				Final:  final,
			}, 0)
			offset += int64(n)
			// Throttle to avoid overwhelming the client's message
			// processing pipeline. The client shares a single connection
			// for both protocol messages and binary data, so sustained
			// bursts can create backpressure that stalls other traffic.
			time.Sleep(5 * time.Millisecond)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			reply(protocol.ClientBinaryResponse{
				Type: "client_binary_response", Error: true,
				Message: fmt.Sprintf("read: %v", readErr),
			})
			return
		}
	}

	reply(protocol.ClientBinaryResponse{
		Type:   "client_binary_response",
		SHA256: fmt.Sprintf("%x", hasher.Sum(nil)),
		Size:   fileSize,
	})
	slog.Info("client binary sent", "client_id", c.id,
		"path", path, "size", fileSize)
}
