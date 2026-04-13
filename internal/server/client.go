package server

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"nxtermd/internal/config"
	"nxtermd/internal/protocol"
)

type clientIdentity struct {
	hostname string
	username string
	pid      int
	process  string
}

type writeMsg struct {
	data      []byte
	byteIndex uint64
}

type Client struct {
	conn   net.Conn
	server *Server
	id     uint32

	writeCh   chan writeMsg
	closeCh   chan struct{}
	closeOnce sync.Once
	identity  atomic.Value // stores *clientIdentity

	// nextByteIndex tracks the byte offset for drop detection.
	// Only accessed by goroutines that call SendMessage/sendReply,
	// but multiple goroutines can call SendMessage concurrently
	// (watchRegion goroutines, broadcast), so we use atomic.
	nextByteIndex atomic.Uint64
}

func NewClient(conn net.Conn, server *Server, id uint32) *Client {
	c := &Client{
		conn:    conn,
		server:  server,
		id:      id,
		writeCh: make(chan writeMsg, 64),
		closeCh: make(chan struct{}),
	}
	c.identity.Store(&clientIdentity{
		hostname: "unknown",
		username: "unknown",
		process:  "unknown",
	})
	go c.writeLoop()
	return c
}

func (c *Client) writeLoop() {
	defer c.conn.Close()

	var writtenByteIndex uint64
	writeFailed := false

	for {
		select {
		case msg, ok := <-c.writeCh:
			if !ok {
				return
			}

			// After the first write error, skip all writes but keep
			// draining the channel so readLoop can finish processing
			// buffered input without senders blocking.
			if writeFailed {
				continue
			}

			if msg.byteIndex > writtenByteIndex {
				dropped := msg.byteIndex - writtenByteIndex
				warning := fmt.Sprintf(`{"type":"warning","warn_type":"dropped_data","message":"lost %d bytes"}`, dropped)
				warning += "\n"
				c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				c.conn.Write([]byte(warning))
				slog.Debug("sent drop warning", "client_id", c.id, "dropped_bytes", dropped)
			}

			c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err := c.conn.Write(msg.data)
			c.conn.SetWriteDeadline(time.Time{})
			if err != nil {
				slog.Debug("client write error", "client_id", c.id, "err", err)
				writeFailed = true
			}
			writtenByteIndex = msg.byteIndex + uint64(len(msg.data))

		case <-c.closeCh:
			// Drain any buffered messages so they don't pin memory.
			for range len(c.writeCh) {
				<-c.writeCh
			}
			return
		}
	}
}

func (c *Client) ReadLoop() {
	defer func() {
		c.server.removeClient(c.id)
		c.Close()
	}()

	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		c.handleMessage(line)
	}
}

func (c *Client) replyFunc(reqID uint64) func(any) {
	return func(msg any) {
		c.sendReply(msg, reqID)
	}
}

// sendReply marshals a response and injects req_id into the JSON.
// Blocks until the write channel has room (caller is this client's ReadLoop).
func (c *Client) sendReply(msg any, reqID uint64) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Debug("marshal error", "client_id", c.id, "err", err)
		return
	}
	if reqID > 0 {
		inject := fmt.Sprintf(`,"req_id":%d}`, reqID)
		data = append(data[:len(data)-1], []byte(inject)...)
	}
	data = append(data, '\n')

	idx := c.nextByteIndex.Add(uint64(len(data))) - uint64(len(data))
	select {
	case c.writeCh <- writeMsg{data: data, byteIndex: idx}:
	case <-c.closeCh:
	}
}

// SendMessage sends a message to the client (no req_id).
// Non-blocking: drops the message if the write channel is full.
func (c *Client) SendMessage(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Debug("marshal error", "client_id", c.id, "err", err)
		return
	}
	data = append(data, '\n')

	idx := c.nextByteIndex.Add(uint64(len(data))) - uint64(len(data))
	select {
	case c.writeCh <- writeMsg{data: data, byteIndex: idx}:
	default:
		slog.Debug("client write channel full, dropping", "client_id", c.id, "bytes", len(data))
	}
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closeCh)
	})
}

// CloseGracefully polls the writeCh until it drains (or the deadline
// expires) and then closes the client. This gives messages enqueued
// just before the close — notably the final upgrade status broadcast
// during a live upgrade — a chance to actually reach the wire before
// writeLoop sees closeCh and drops the remaining queue.
func (c *Client) CloseGracefully(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(c.writeCh) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.Close()
}

func (c *Client) GetHostname() string {
	return c.identity.Load().(*clientIdentity).hostname
}

func (c *Client) GetUsername() string {
	return c.identity.Load().(*clientIdentity).username
}

func (c *Client) GetPid() int {
	return c.identity.Load().(*clientIdentity).pid
}

func (c *Client) GetProcess() string {
	return c.identity.Load().(*clientIdentity).process
}

type envelope struct {
	Type  string `json:"type"`
	ReqID uint64 `json:"req_id,omitempty"`
}

func (c *Client) handleMessage(line []byte) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		slog.Debug("parse error", "client_id", c.id, "err", err)
		return
	}

	reply := c.replyFunc(env.ReqID)

	switch env.Type {
	case "identify":
		var msg protocol.Identify
		if json.Unmarshal(line, &msg) == nil {
			c.handleIdentify(msg)
		}
	case "spawn_request":
		var msg protocol.SpawnRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleSpawn(msg, reply)
		}
	case "subscribe_request":
		var msg protocol.SubscribeRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleSubscribe(msg, reply)
		}
	case "input":
		var msg protocol.InputMsg
		if json.Unmarshal(line, &msg) == nil {
			c.handleInput(msg)
		}
	case "resize_request":
		var msg protocol.ResizeRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleResize(msg, reply)
		}
	case "list_regions_request":
		var msg protocol.ListRegionsRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleListRegions(msg, reply)
		}
	case "status_request":
		c.handleStatus(reply)
	case "get_screen_request":
		var msg protocol.GetScreenRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleGetScreen(msg, reply)
		}
	case "get_scrollback_request":
		var msg protocol.GetScrollbackRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleGetScrollback(msg, reply)
		}
	case "kill_region_request":
		var msg protocol.KillRegionRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleKillRegion(msg, reply)
		}
	case "list_clients_request":
		c.handleListClients(reply)
	case "kill_client_request":
		var msg protocol.KillClientRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleKillClient(msg, reply)
		}
	case "unsubscribe_request":
		var msg protocol.UnsubscribeRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleUnsubscribe(msg, reply)
		}
	case "session_connect_request":
		var msg protocol.SessionConnectRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleSessionConnect(msg, reply)
		}
	case "list_sessions_request":
		c.handleListSessions(reply)
	case "list_programs_request":
		c.handleListPrograms(reply)
	case "add_program_request":
		var msg protocol.AddProgramRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleAddProgram(msg, reply)
		}
	case "remove_program_request":
		var msg protocol.RemoveProgramRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleRemoveProgram(msg, reply)
		}
	case "upgrade_check_request":
		var msg protocol.UpgradeCheckRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleUpgradeCheck(msg, reply)
		}
	case "server_upgrade_request":
		c.handleServerUpgrade(reply)
	case "client_binary_request":
		var msg protocol.ClientBinaryRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleClientBinaryDownload(msg, reply)
		}
	case "overlay_register":
		var msg protocol.OverlayRegisterRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleOverlayRegister(msg, reply)
		}
	case "overlay_render":
		var msg protocol.OverlayRender
		if json.Unmarshal(line, &msg) == nil {
			c.handleOverlayRender(msg)
		}
	case "overlay_clear":
		var msg protocol.OverlayClear
		if json.Unmarshal(line, &msg) == nil {
			c.handleOverlayClear(msg)
		}
	case "tree_resync_request":
		c.server.send(treeSnapshotReq{clientID: c.id})
	case "disconnect":
		slog.Info("client disconnecting gracefully", "client_id", c.id)
		c.Close()
	default:
		slog.Debug("unknown message type", "client_id", c.id, "type", env.Type)
	}
}

func (c *Client) handleOverlayRegister(msg protocol.OverlayRegisterRequest, reply func(any)) {
	resp := make(chan overlayRegisterResult, 1)
	if !c.server.send(overlayRegisterReq{clientID: c.id, regionID: msg.RegionID, resp: resp}) {
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

func (c *Client) handleOverlayRender(msg protocol.OverlayRender) {
	c.server.send(overlayRenderReq{
		clientID:  c.id,
		regionID:  msg.RegionID,
		cells:     msg.Cells,
		cursorRow: msg.CursorRow,
		cursorCol: msg.CursorCol,
		modes:     msg.Modes,
	})
}

func (c *Client) handleOverlayClear(msg protocol.OverlayClear) {
	c.server.send(overlayClearReq{clientID: c.id, regionID: msg.RegionID})
}

func (c *Client) sendIdentify() {
	hostname, _ := os.Hostname()
	c.SendMessage(protocol.Identify{
		Type:     "identify",
		Hostname: hostname,
		Process:  "nxtermd",
		Pid:      os.Getpid(),
	})
}

func (c *Client) handleIdentify(msg protocol.Identify) {
	ident := clientIdentity{
		hostname: msg.Hostname,
		username: msg.Username,
		pid:      msg.Pid,
		process:  msg.Process,
	}
	c.identity.Store(&ident)

	// Route through event loop so the tree's client node is updated.
	c.server.send(identifyReq{clientID: c.id, identity: ident})

	slog.Debug("client identified", "client_id", c.id,
		"hostname", msg.Hostname, "username", msg.Username,
		"pid", msg.Pid, "process", msg.Process)
}

func (c *Client) handleSpawn(msg protocol.SpawnRequest, reply func(any)) {
	sessionName := msg.Session
	if sessionName == "" {
		sessionName = c.server.GetClientSession(c.id)
	}
	if sessionName == "" {
		sessionName = c.server.sessionsCfg.DefaultName
	}

	programName := msg.Program
	if programName == "" {
		programName = "default"
	}

	region, err := c.server.SpawnProgram(sessionName, programName)
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

func (c *Client) handleSubscribe(msg protocol.SubscribeRequest, reply func(any)) {
	if len(msg.RegionID) != 36 {
		reply(protocol.SubscribeResponse{
			Type:     "subscribe_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "invalid region_id length",
		})
		return
	}

	region, _ := c.server.Subscribe(c.id, msg.RegionID)
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

func (c *Client) handleUnsubscribe(msg protocol.UnsubscribeRequest, reply func(any)) {
	c.server.Unsubscribe(c.id)
	reply(protocol.UnsubscribeResponse{
		Type:     "unsubscribe_response",
		RegionID: msg.RegionID,
	})
	slog.Debug("client unsubscribed", "client_id", c.id, "region_id", msg.RegionID)
}

func (c *Client) handleInput(msg protocol.InputMsg) {
	region, overlayClient := c.server.RouteInput(msg.RegionID)
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

func (c *Client) handleResize(msg protocol.ResizeRequest, reply func(any)) {
	region := c.server.FindRegion(msg.RegionID)
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

func (c *Client) handleListRegions(msg protocol.ListRegionsRequest, reply func(any)) {
	infos := c.server.getRegionInfos(msg.Session)
	reply(protocol.ListRegionsResponse{
		Type:    "list_regions_response",
		Regions: infos,
		Error:   false,
		Message: "",
	})
}

func (c *Client) handleStatus(reply func(any)) {
	reply(c.server.getStatus())
}

// scrollbackChunkSize is the max lines per scrollback response chunk.
const scrollbackChunkSize = 1000

func (c *Client) handleGetScrollback(msg protocol.GetScrollbackRequest, reply func(any)) {
	region := c.server.FindRegion(msg.RegionID)
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

func (c *Client) handleGetScreen(msg protocol.GetScreenRequest, reply func(any)) {
	region := c.server.FindRegion(msg.RegionID)
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
	if ov := c.server.GetOverlay(msg.RegionID); ov != nil {
		snap = compositeSnapshot(snap, ov)
	}
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

func (c *Client) handleKillRegion(msg protocol.KillRegionRequest, reply func(any)) {
	if c.server.KillRegion(msg.RegionID) {
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

func (c *Client) handleListClients(reply func(any)) {
	infos := c.server.getClientInfos()
	reply(protocol.ListClientsResponse{
		Type:    "list_clients_response",
		Clients: infos,
		Error:   false,
		Message: "",
	})
}

func (c *Client) handleKillClient(msg protocol.KillClientRequest, reply func(any)) {
	if msg.ClientID == c.id {
		reply(protocol.KillClientResponse{
			Type:     "kill_client_response",
			ClientID: msg.ClientID,
			Error:    true,
			Message:  "cannot kill self",
		})
		return
	}

	if c.server.KillClient(msg.ClientID) {
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

func (c *Client) handleSessionConnect(msg protocol.SessionConnectRequest, reply func(any)) {
	sessionName := msg.Session
	if sessionName == "" {
		sessionName = c.server.sessionsCfg.DefaultName
	}

	sess, infos, err := c.server.findOrCreateSession(sessionName, msg.Width, msg.Height)
	if err != nil {
		reply(protocol.SessionConnectResponse{
			Type:    "session_connect_response",
			Session: sessionName,
			Error:   true,
			Message: fmt.Sprintf("session connect: %v", err),
		})
		return
	}

	c.server.SetClientSession(c.id, sess.name)

	reply(protocol.SessionConnectResponse{
		Type:     "session_connect_response",
		Session:  sess.name,
		Regions:  infos,
		Programs: c.server.listProgramInfos(),
		Error:    false,
		Message:  "",
	})

	slog.Debug("client connected to session", "client_id", c.id, "session", sess.name)
}

func (c *Client) handleListSessions(reply func(any)) {
	infos := c.server.getSessionInfos()
	reply(protocol.ListSessionsResponse{
		Type:     "list_sessions_response",
		Sessions: infos,
		Error:    false,
		Message:  "",
	})
}

func (c *Client) handleListPrograms(reply func(any)) {
	infos := c.server.listProgramInfos()
	reply(protocol.ListProgramsResponse{
		Type:     "list_programs_response",
		Programs: infos,
		Error:    false,
		Message:  "",
	})
}

func (c *Client) handleAddProgram(msg protocol.AddProgramRequest, reply func(any)) {
	p := config.ProgramConfig{
		Name: msg.Name,
		Cmd:  msg.Cmd,
		Args: msg.Args,
		Env:  msg.Env,
	}
	if err := c.server.addProgram(p); err != nil {
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

func (c *Client) handleRemoveProgram(msg protocol.RemoveProgramRequest, reply func(any)) {
	if err := c.server.removeProgram(msg.Name); err != nil {
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
