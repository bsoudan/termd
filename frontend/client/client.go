// Package client manages the Unix socket connection to the termd server.
package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sync"

	termlog "termd/frontend/log"
	"termd/frontend/protocol"
)

// Client manages a connection to a termd server.
type Client struct {
	conn      net.Conn
	updates   chan any
	sendCh    chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

// New wraps an existing connection as a termd client. It starts the
// read/write loops and sends an identify message.
func New(conn net.Conn, processName string) *Client {
	c := &Client{
		conn:    conn,
		updates: make(chan any, 128),
		sendCh:  make(chan []byte, 64),
		done:    make(chan struct{}),
	}

	go c.readLoop()
	go c.writeLoop()

	hostname, _ := os.Hostname()
	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	proc := processName
	if proc == "" {
		proc = filepath.Base(os.Args[0])
	}
	_ = c.Send(protocol.Identify{
		Type: "identify", Hostname: hostname,
		Username: username, Pid: os.Getpid(), Process: proc,
	})

	return c
}

// Send encodes msg as JSON and enqueues it for transmission.
func (c *Client) Send(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	termlog.LogProtocolMsg("send", msg)
	select {
	case c.sendCh <- data:
		return nil
	case <-c.done:
		return fmt.Errorf("client closed")
	}
}

// Updates returns a read-only channel of inbound messages.
func (c *Client) Updates() <-chan any {
	return c.updates
}

// Close shuts down the connection and drains goroutines.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		// Drain pending sends before closing the connection
		for {
			select {
			case data := <-c.sendCh:
				c.conn.Write(data)
			default:
				close(c.done)
				c.conn.Close()
				return
			}
		}
	})
}

func (c *Client) readLoop() {
	defer close(c.updates)
	defer c.Close() // unblock writeLoop on server disconnect
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB for large screen updates
	for scanner.Scan() {
		line := scanner.Bytes()
		msg, err := protocol.ParseInbound(line)
		if err != nil {
			slog.Debug("recv parse error", "error", err)
			continue
		}
		termlog.LogProtocolMsg("recv", msg)
		select {
		case c.updates <- msg:
		case <-c.done:
			return
		}
	}
	slog.Debug("read loop exiting", "error", scanner.Err())
}

func (c *Client) writeLoop() {
	for {
		select {
		case data := <-c.sendCh:
			if _, err := c.conn.Write(data); err != nil {
				slog.Debug("write error", "error", err)
				return
			}
		case <-c.done:
			return
		}
	}
}
