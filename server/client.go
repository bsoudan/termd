package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
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

func (c *Client) SendMessage(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Debug("marshal error", "client_id", c.id, "err", err)
		return
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}

	_, err = c.conn.Write(data)
	if err != nil {
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

type envelope struct {
	Type string `json:"type"`
}

func (c *Client) handleMessage(line []byte) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		slog.Debug("parse error", "client_id", c.id, "err", err)
		return
	}

	switch env.Type {
	case "identify":
		var msg protocol.Identify
		if json.Unmarshal(line, &msg) == nil {
			c.handleIdentify(msg)
		}
	case "spawn_request":
		var msg protocol.SpawnRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleSpawn(msg)
		}
	case "subscribe_request":
		var msg protocol.SubscribeRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleSubscribe(msg)
		}
	case "input":
		var msg protocol.InputMsg
		if json.Unmarshal(line, &msg) == nil {
			c.handleInput(msg)
		}
	case "resize_request":
		var msg protocol.ResizeRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleResize(msg)
		}
	case "list_regions_request":
		c.handleListRegions()
	case "status_request":
		c.handleStatus()
	case "get_screen_request":
		var msg protocol.GetScreenRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleGetScreen(msg)
		}
	case "get_scrollback_request":
		var msg protocol.GetScrollbackRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleGetScrollback(msg)
		}
	case "kill_region_request":
		var msg protocol.KillRegionRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleKillRegion(msg)
		}
	case "list_clients_request":
		c.handleListClients()
	case "kill_client_request":
		var msg protocol.KillClientRequest
		if json.Unmarshal(line, &msg) == nil {
			c.handleKillClient(msg)
		}
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

func (c *Client) handleSpawn(msg protocol.SpawnRequest) {
	region, err := c.server.SpawnRegion(msg.Cmd, msg.Args)
	if err != nil {
		c.SendMessage(protocol.SpawnResponse{
			Type:     "spawn_response",
			RegionID: "",
			Name:     "",
			Error:    true,
			Message:  err.Error(),
		})
		return
	}

	c.SendMessage(protocol.SpawnResponse{
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
	})
}

func (c *Client) handleSubscribe(msg protocol.SubscribeRequest) {
	if len(msg.RegionID) != 36 {
		c.SendMessage(protocol.SubscribeResponse{
			Type:     "subscribe_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "invalid region_id length",
		})
		return
	}

	region := c.server.FindRegion(msg.RegionID)
	if region == nil {
		c.SendMessage(protocol.SubscribeResponse{
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

	c.SendMessage(protocol.SubscribeResponse{
		Type:     "subscribe_response",
		RegionID: region.id,
		Error:    false,
		Message:  "",
	})

	slog.Debug("client subscribed", "client_id", c.id, "region_id", region.id)
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

func (c *Client) handleResize(msg protocol.ResizeRequest) {
	region := c.server.FindRegion(msg.RegionID)
	if region == nil {
		c.SendMessage(protocol.ResizeResponse{
			Type:     "resize_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	if err := region.Resize(msg.Width, msg.Height); err != nil {
		c.SendMessage(protocol.ResizeResponse{
			Type:     "resize_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  err.Error(),
		})
		return
	}

	c.SendMessage(protocol.ResizeResponse{
		Type:     "resize_response",
		RegionID: region.id,
		Error:    false,
		Message:  "",
	})
}

func (c *Client) handleListRegions() {
	infos := c.server.getRegionInfos()
	c.SendMessage(protocol.ListRegionsResponse{
		Type:    "list_regions_response",
		Regions: infos,
		Error:   false,
		Message: "",
	})
}

func (c *Client) handleStatus() {
	c.SendMessage(c.server.getStatus())
}

func (c *Client) handleGetScrollback(msg protocol.GetScrollbackRequest) {
	region := c.server.FindRegion(msg.RegionID)
	if region == nil {
		c.SendMessage(protocol.GetScrollbackResponse{
			Type:     "get_scrollback_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	lines := region.GetScrollback()
	c.SendMessage(protocol.GetScrollbackResponse{
		Type:     "get_scrollback_response",
		RegionID: region.id,
		Lines:    lines,
	})
}

func (c *Client) handleGetScreen(msg protocol.GetScreenRequest) {
	region := c.server.FindRegion(msg.RegionID)
	if region == nil {
		c.SendMessage(protocol.GetScreenResponse{
			Type:     "get_screen_response",
			RegionID: msg.RegionID,
			Lines:    []string{},
			Error:    true,
			Message:  "region not found",
		})
		return
	}

	snap := region.Snapshot()
	c.SendMessage(protocol.GetScreenResponse{
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

func (c *Client) handleKillRegion(msg protocol.KillRegionRequest) {
	if c.server.KillRegion(msg.RegionID) {
		c.SendMessage(protocol.KillRegionResponse{
			Type:     "kill_region_response",
			RegionID: msg.RegionID,
			Error:    false,
			Message:  "",
		})
	} else {
		c.SendMessage(protocol.KillRegionResponse{
			Type:     "kill_region_response",
			RegionID: msg.RegionID,
			Error:    true,
			Message:  "region not found",
		})
	}
}

func (c *Client) handleListClients() {
	infos := c.server.getClientInfos()
	c.SendMessage(protocol.ListClientsResponse{
		Type:    "list_clients_response",
		Clients: infos,
		Error:   false,
		Message: "",
	})
}

func (c *Client) handleKillClient(msg protocol.KillClientRequest) {
	if msg.ClientID == c.id {
		c.SendMessage(protocol.KillClientResponse{
			Type:     "kill_client_response",
			ClientID: msg.ClientID,
			Error:    true,
			Message:  "cannot kill self",
		})
		return
	}

	if c.server.KillClient(msg.ClientID) {
		c.SendMessage(protocol.KillClientResponse{
			Type:     "kill_client_response",
			ClientID: msg.ClientID,
			Error:    false,
			Message:  "",
		})
	} else {
		c.SendMessage(protocol.KillClientResponse{
			Type:     "kill_client_response",
			ClientID: msg.ClientID,
			Error:    true,
			Message:  "client not found",
		})
	}
}
