package server

// client.go contains the Client type — a pure network I/O actor.
// Read loop, write loop, send/reply, backpressure, and identity
// accessors live here. Protocol message handling is in handlers.go.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

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
	// (region actor goroutines, broadcast), so we use atomic.
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
		c.server.dispatch(c, scanner.Bytes())
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

func (c *Client) sendIdentify() {
	hostname, _ := os.Hostname()
	c.SendMessage(protocol.Identify{
		Type:     "identify",
		Hostname: hostname,
		Process:  "nxtermd",
		Pid:      os.Getpid(),
	})
}
