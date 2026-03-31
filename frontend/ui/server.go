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

	downloadMu sync.Mutex
	download   *Download // set during client binary download
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

// SetDownload registers or clears the active binary download.
// When set, dispatchInbound writes chunks directly to the download
// instead of sending them through bubbletea.
func (s *Server) SetDownload(d *Download) {
	s.downloadMu.Lock()
	s.download = d
	s.downloadMu.Unlock()
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

	// Buffer inbound messages to bubbletea so that p.Send() blocking
	// (e.g. during PTY rendering) doesn't stall the recv drain.
	inboundBuf := make(chan any, 256)
	go func() {
		for msg := range inboundBuf {
			p.Send(msg)
		}
	}()
	defer close(inboundBuf)

	for {
		select {
		case msg, ok := <-s.ch:
			if !ok {
				c.Close()
				for range recv {
				}
				return true
			}
			s.dispatchOutbound(c, msg)

		case msg, ok := <-recv:
			if !ok {
				c.Close()
				return false
			}
			s.dispatchInbound(msg, recv, inboundBuf)
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

		// Verify the server has accepted the connection by waiting for
		// its identify message. During a live upgrade the TCP handshake
		// can succeed against the inherited listener's kernel backlog
		// before the new process has called Accept(). Without this
		// check, ReconnectedMsg fires prematurely and subsequent
		// requests hang because nobody is reading the connection.
		c := client.New(conn)
		timer := time.NewTimer(3 * time.Second)
		select {
		case _, ok := <-c.Recv():
			timer.Stop()
			if !ok {
				slog.Debug("reconnect: connection closed before identify")
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
		case <-timer.C:
			slog.Debug("reconnect: timed out waiting for server identify")
			c.Close()
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		case <-s.done:
			c.Close()
			return nil
		}

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

// dispatchInbound sends a message to bubbletea via the inbound buffer.
// TerminalEvents are batched for performance. Binary chunks are written
// directly to the active download to avoid backpressure through bubbletea.
func (s *Server) dispatchInbound(msg protocol.Message, recv <-chan protocol.Message, inbound chan<- any) {
	// Fast path: write binary chunks directly to the download file.
	if chunk, ok := msg.Payload.(protocol.ClientBinaryChunk); ok {
		s.downloadMu.Lock()
		dl := s.download
		s.downloadMu.Unlock()
		if dl != nil {
			dl.HandleChunk(chunk)
			return
		}
	}

	te, ok := msg.Payload.(protocol.TerminalEvents)
	if !ok {
		inbound <- msg
		return
	}

	// Batch consecutive TerminalEvents with the same RegionID.
	batch := te.Events
	regionID := te.RegionID
drain:
	for {
		select {
		case next, ok := <-recv:
			if !ok {
				break drain
			}
			if te2, ok := next.Payload.(protocol.TerminalEvents); ok && te2.RegionID == regionID {
				batch = append(batch, te2.Events...)
			} else {
				inbound <- protocol.Message{Payload: protocol.TerminalEvents{RegionID: regionID, Events: batch}}
				s.dispatchInbound(next, recv, inbound)
				return
			}
		default:
			break drain
		}
	}
	inbound <- protocol.Message{Payload: protocol.TerminalEvents{RegionID: regionID, Events: batch}}
}

func (s *Server) sendIdentify(c *client.Client) {
	c.SendIdentify(s.processName)
}
