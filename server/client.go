package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	"termd/frontend/protocol"
)

type Client struct {
	conn   net.Conn
	server *Server
	id     uint32

	mu                 sync.Mutex
	hostname           string
	username           string
	pid                int
	process            string
	sessionName        string
	subscribedRegionID string
	closed             bool
}

func NewClient(conn net.Conn, server *Server, id uint32) *Client {
	return &Client{
		conn:     conn,
		server:   server,
		id:       id,
		hostname: "unknown",
		username: "unknown",
		process:  "unknown",
	}
}

func (c *Client) ReadLoop() {
	defer func() {
		c.server.removeClient(c.id)
		c.Close()
	}()

	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		c.handleMessage(line)
	}
}

// replyFunc returns a function that sends a response with the given req_id.
func (c *Client) replyFunc(reqID uint64) func(any) {
	return func(msg any) {
		c.sendReply(msg, reqID)
	}
}

// sendReply marshals a response and injects req_id into the JSON.
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
	c.writeRaw(data)
}

// SendMessage sends a message to the client (no req_id).
func (c *Client) SendMessage(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Debug("marshal error", "client_id", c.id, "err", err)
		return
	}
	data = append(data, '\n')
	c.writeRaw(data)
}

func (c *Client) writeRaw(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if _, err := c.conn.Write(data); err != nil {
		slog.Debug("client write error", "client_id", c.id, "err", err)
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.conn.Close()
}

func (c *Client) GetSubscribedRegionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscribedRegionID
}

func (c *Client) SetSubscribedRegionID(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscribedRegionID = id
}

func (c *Client) GetHostname() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hostname
}

func (c *Client) GetUsername() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.username
}

func (c *Client) GetPid() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pid
}

func (c *Client) GetProcess() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.process
}

func (c *Client) GetSessionName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionName
}

func (c *Client) SetSessionName(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionName = name
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
	case "disconnect":
		slog.Info("client disconnecting gracefully", "client_id", c.id)
		c.Close()
	default:
		slog.Debug("unknown message type", "client_id", c.id, "type", env.Type)
	}
}

func (c *Client) sendIdentify() {
	hostname, _ := os.Hostname()
	c.SendMessage(protocol.Identify{
		Type:     "identify",
		Hostname: hostname,
		Process:  "termd",
		Pid:      os.Getpid(),
	})
}

func (c *Client) handleIdentify(msg protocol.Identify) {
	c.mu.Lock()
	c.hostname = msg.Hostname
	c.username = msg.Username
	c.pid = msg.Pid
	c.process = msg.Process
	c.mu.Unlock()

	slog.Debug("client identified", "client_id", c.id,
		"hostname", msg.Hostname, "username", msg.Username,
		"pid", msg.Pid, "process", msg.Process)
}

func (c *Client) handleSpawn(msg protocol.SpawnRequest, reply func(any)) {
	sessionName := msg.Session
	if sessionName == "" {
		sessionName = c.GetSessionName()
	}
	if sessionName == "" {
		sessionName = c.server.sessionsCfg.DefaultName
	}

	region, err := c.server.SpawnRegion(sessionName, msg.Cmd, msg.Args)
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
		RegionID: region.id,
		Name:     region.name,
		Error:    false,
		Message:  "",
	})

	c.server.Broadcast(protocol.RegionCreated{
		Type:     "region_created",
		RegionID: region.id,
		Name:     region.name,
		Session:  sessionName,
	})
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

	region := c.server.FindRegion(msg.RegionID)
	if region == nil {
		reply(protocol.SubscribeResponse{
			Type:     "subscribe_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	c.SetSubscribedRegionID(region.id)

	snap := region.Snapshot()
	c.SendMessage(protocol.ScreenUpdate{
		Type:      "screen_update",
		RegionID:  region.id,
		CursorRow: snap.CursorRow,
		CursorCol: snap.CursorCol,
		Lines:     snap.Lines,
		Cells:     snap.Cells,
	})

	reply(protocol.SubscribeResponse{
		Type:     "subscribe_response",
		RegionID: region.id,
		Error:    false,
		Message:  "",
	})

	slog.Debug("client subscribed", "client_id", c.id, "region_id", region.id)
}

func (c *Client) handleUnsubscribe(msg protocol.UnsubscribeRequest, reply func(any)) {
	c.SetSubscribedRegionID("")
	reply(protocol.UnsubscribeResponse{
		Type:     "unsubscribe_response",
		RegionID: msg.RegionID,
	})
	slog.Debug("client unsubscribed", "client_id", c.id, "region_id", msg.RegionID)
}

func (c *Client) handleInput(msg protocol.InputMsg) {
	region := c.server.FindRegion(msg.RegionID)
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
		RegionID: region.id,
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
	reply(protocol.GetScrollbackResponse{
		Type:     "get_scrollback_response",
		RegionID: region.id,
		Lines:    lines,
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
	reply(protocol.GetScreenResponse{
		Type:      "get_screen_response",
		RegionID:  region.id,
		CursorRow: snap.CursorRow,
		CursorCol: snap.CursorCol,
		Lines:     snap.Lines,
		Cells:     snap.Cells,
		Error:     false,
		Message:   "",
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

	sess, infos, err := c.server.findOrCreateSession(sessionName)
	if err != nil {
		reply(protocol.SessionConnectResponse{
			Type:    "session_connect_response",
			Session: sessionName,
			Error:   true,
			Message: fmt.Sprintf("session connect: %v", err),
		})
		return
	}

	c.SetSessionName(sess.name)

	reply(protocol.SessionConnectResponse{
		Type:    "session_connect_response",
		Session: sess.name,
		Regions: infos,
		Error:   false,
		Message: "",
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
