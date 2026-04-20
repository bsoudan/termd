package tui

import (
	"encoding/base64"
	"log/slog"
	"net"
	"sync"
	"time"

	"nxtermd/internal/client"
	"nxtermd/internal/protocol"
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

// PausedMsg is emitted on Lifecycle after the session transitions from
// running to paused. Informational — the server goroutine has already
// stopped draining inbound messages.
type PausedMsg struct{}

// ResumedMsg is emitted on Lifecycle after the session transitions from
// paused to running.
type ResumedMsg struct{}

// pauseMsg/resumeMsg are internal — sent through s.ch so the server
// goroutine toggles its paused state without a shared mutex.
type pauseMsg struct{}
type resumeMsg struct{}

// Server owns the client connection and runs as a separate goroutine.
// Bubbletea and the input loop communicate with it via Send().
// Inbound protocol messages and lifecycle events are exposed as channels
// for the main loop to select on.
type Server struct {
	ch          chan any
	done        chan struct{} // closed by Close() to prevent Send() panic
	closeOnce   sync.Once
	processName string

	// Inbound carries protocol messages from the server, read by the main loop.
	Inbound chan protocol.Message

	// Lifecycle carries DisconnectedMsg/ReconnectedMsg/PausedMsg/ResumedMsg,
	// read by the main loop.
	Lifecycle chan any

	downloadMu sync.Mutex
	download   *Download // set during client binary download

	// paused is owned exclusively by the server goroutine. It is mutated
	// only when processing pauseMsg/resumeMsg off s.ch. While true, the
	// goroutine stops draining inbound messages — TCP backpressure
	// propagates to the server's writeCh, surfacing the slow-client path.
	// Survives reconnects intentionally; resume must be explicit.
	paused bool
}

func NewServer(bufSize int, processName string) *Server {
	return &Server{
		ch:          make(chan any, bufSize),
		done:        make(chan struct{}),
		processName: processName,
		Inbound:     make(chan protocol.Message, 256),
		Lifecycle:   make(chan any, 4),
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

// Pause asks the server goroutine to stop draining inbound messages.
// Fire-and-forget; the transition is emitted on Lifecycle as PausedMsg.
func (s *Server) Pause() { s.Send(pauseMsg{}) }

// Resume asks the server goroutine to resume draining. Fire-and-forget;
// the transition is emitted on Lifecycle as ResumedMsg.
func (s *Server) Resume() { s.Send(resumeMsg{}) }

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
func (s *Server) Run(conn net.Conn, dialFn func() (net.Conn, error)) {
	c := s.newClient(conn)
	s.sendIdentify(c)

	for {
		exit := s.runConnection(c)
		if exit {
			return
		}

		// Connection lost — reconnect with exponential backoff
		c = s.reconnect(dialFn)
		if c == nil {
			return // send channel closed during reconnect
		}
	}
}

// runConnection processes messages on a single connection until it drops
// or the send channel closes. Returns true if we should exit entirely.
//
// When s.paused is true, the recv case is gated off by leaving its
// channel variable nil — select on a nil channel blocks forever, so
// inbound messages queue up in client.Client's internal recvCh. Once
// that fills, the client's reader blocks and TCP backpressure pushes
// all the way back to the server's writeCh, which starts dropping
// broadcasts. That's the test harness signal for slow-client behavior.
func (s *Server) runConnection(c *client.Client) (exit bool) {
	recv := c.Recv()

	for {
		var inbound <-chan protocol.Message
		if !s.paused {
			inbound = recv
		}

		select {
		case msg, ok := <-s.ch:
			if !ok {
				c.Close()
				for range recv {
				}
				return true
			}
			switch msg.(type) {
			case pauseMsg:
				if !s.paused {
					s.paused = true
					s.emitLifecycle(PausedMsg{})
				}
			case resumeMsg:
				if s.paused {
					s.paused = false
					s.emitLifecycle(ResumedMsg{})
				}
			default:
				s.dispatchOutbound(c, msg)
			}

		case msg, ok := <-inbound:
			if !ok {
				c.Close()
				return false
			}
			s.dispatchInbound(msg, recv, s.Inbound)
		}
	}
}

// emitLifecycle pushes a lifecycle event for the main loop to pick up.
// Non-blocking via the buffered channel; drops silently on shutdown.
func (s *Server) emitLifecycle(msg any) {
	select {
	case s.Lifecycle <- msg:
	case <-s.done:
	}
}

// reconnect attempts to restore the connection with exponential backoff.
// Returns the new client, or nil if the send channel was closed.
func (s *Server) reconnect(dialFn func() (net.Conn, error)) *client.Client {
	if dialFn == nil {
		return nil
	}

	backoff := 100 * time.Millisecond
	maxBackoff := 60 * time.Second

	for {
		retryAt := time.Now().Add(backoff)
		select {
		case s.Lifecycle <- DisconnectedMsg{RetryAt: retryAt}:
		case <-s.done:
			return nil
		}

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
		c := s.newClient(conn)
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
		select {
		case s.Lifecycle <- ReconnectedMsg{}:
		case <-s.done:
			c.Close()
			return nil
		}
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

// dispatchInbound sends a message to the inbound channel.
// TerminalEvents are batched for performance. Binary chunks are written
// directly to the active download to avoid backpressure through bubbletea.
func (s *Server) dispatchInbound(msg protocol.Message, recv <-chan protocol.Message, inbound chan<- protocol.Message) {
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

func (s *Server) newClient(conn net.Conn) *client.Client {
	return client.New(conn)
}

func (s *Server) sendIdentify(c *client.Client) {
	c.SendIdentify(s.processName)
}
