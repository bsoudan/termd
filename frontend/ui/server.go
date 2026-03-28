package ui

import (
	"encoding/base64"
	"log/slog"
	"net"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

// InputMsg is sent to the server goroutine to forward raw bytes to the
// child process. The server goroutine base64-encodes and wraps it.
type InputMsg struct {
	RegionID string
	Data     []byte
}

// DisconnectedMsg is sent to bubbletea when the server connection drops.
type DisconnectedMsg struct {
	RetryAt time.Time
}

// ReconnectedMsg is sent to bubbletea when the connection is restored.
type ReconnectedMsg struct{}

// Server owns the client connection and runs as a separate goroutine.
// Bubbletea and the input loop communicate with it via Send().
type Server struct {
	ch          chan any
	done        chan struct{} // closed by Close() to prevent Send() panic
	closeOnce   sync.Once
	processName string
}

func NewServer(bufSize int, processName string) *Server {
	return &Server{
		ch:          make(chan any, bufSize),
		done:        make(chan struct{}),
		processName: processName,
	}
}

// Send enqueues a message for the server goroutine. Non-blocking — drops
// if the channel is full or the server is shut down.
func (s *Server) Send(msg any) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.ch <- msg:
	case <-s.done:
	default:
	}
}

// Close signals the server goroutine to exit. Safe to call multiple times.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		close(s.ch)
	})
}

// Run connects to the server, processes messages, and handles reconnection.
// It blocks until the send channel is closed.
func (s *Server) Run(conn net.Conn, dialFn func() (net.Conn, error), p *tea.Program) {
	c := client.New(conn)
	s.sendIdentify(c)

	for {
		exit := s.runConnection(c, p)
		if exit {
			return
		}

		// Connection lost — reconnect with exponential backoff
		c = s.reconnect(dialFn, p)
		if c == nil {
			return // send channel closed during reconnect
		}
	}
}

// runConnection processes messages on a single connection until it drops
// or the send channel closes. Returns true if we should exit entirely.
func (s *Server) runConnection(c *client.Client, p *tea.Program) (exit bool) {
	recv := c.Recv()

	for {
		select {
		case msg, ok := <-s.ch:
			if !ok {
				// Send channel closed — shutdown
				c.Close()
				// Drain remaining recv
				for range recv {
				}
				return true
			}
			s.dispatchOutbound(c, msg)

		case msg, ok := <-recv:
			if !ok {
				// Connection dropped
				c.Close()
				return false
			}
			s.dispatchInbound(msg, recv, p)
		}
	}
}

// reconnect attempts to restore the connection with exponential backoff.
// Returns the new client, or nil if the send channel was closed.
func (s *Server) reconnect(dialFn func() (net.Conn, error), p *tea.Program) *client.Client {
	if dialFn == nil {
		return nil
	}

	backoff := 100 * time.Millisecond
	maxBackoff := 60 * time.Second

	for {
		retryAt := time.Now().Add(backoff)
		p.Send(DisconnectedMsg{RetryAt: retryAt})

		// Wait for backoff. Messages queue in s.ch (buffered) during this time.
		time.Sleep(backoff)

		slog.Debug("reconnecting", "backoff", backoff)
		conn, err := dialFn()
		if err != nil {
			slog.Debug("reconnect failed", "error", err)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		c := client.New(conn)
		s.sendIdentify(c)
		p.Send(ReconnectedMsg{})
		return c
	}
}

func (s *Server) dispatchOutbound(c *client.Client, msg any) {
	switch m := msg.(type) {
	case InputMsg:
		data := base64.StdEncoding.EncodeToString(m.Data)
		if err := c.Send(protocol.InputMsg{
			RegionID: m.RegionID,
			Data:     data,
		}); err != nil {
			slog.Debug("send error", "error", err)
		}
	default:
		if err := c.Send(m); err != nil {
			slog.Debug("send error", "error", err)
		}
	}
}

// dispatchInbound sends a message to bubbletea, batching consecutive
// TerminalEvents for performance.
func (s *Server) dispatchInbound(msg any, recv <-chan any, p *tea.Program) {
	te, ok := msg.(protocol.TerminalEvents)
	if !ok {
		p.Send(msg)
		return
	}

	// Batch consecutive TerminalEvents
	batch := te.Events
drain:
	for {
		select {
		case next, ok := <-recv:
			if !ok {
				break drain
			}
			if te2, ok := next.(protocol.TerminalEvents); ok {
				batch = append(batch, te2.Events...)
			} else {
				p.Send(protocol.TerminalEvents{Events: batch})
				p.Send(next)
				return
			}
		default:
			break drain
		}
	}
	p.Send(protocol.TerminalEvents{Events: batch})
}

func (s *Server) sendIdentify(c *client.Client) {
	c.SendIdentify(s.processName)
}
