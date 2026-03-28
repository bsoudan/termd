package ui

import (
	"encoding/base64"
	"log/slog"

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

// Server owns the client connection and runs as a separate goroutine.
// Bubbletea and the input loop communicate with it via Send().
type Server struct {
	ch chan any
}

func NewServer(bufSize int) *Server {
	return &Server{ch: make(chan any, bufSize)}
}

// Send enqueues a message for the server goroutine. Non-blocking — drops
// if the channel is full.
func (s *Server) Send(msg any) {
	select {
	case s.ch <- msg:
	default:
		slog.Debug("server send dropped (channel full)")
	}
}

// Close signals the server goroutine to exit.
func (s *Server) Close() {
	close(s.ch)
}

// Run processes outbound requests and pumps inbound server messages to
// bubbletea. It blocks until the send channel is closed.
func (s *Server) Run(c *client.Client, p *tea.Program) {
	// Inbound: read from server, batch terminal events, send to bubbletea.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range c.Updates() {
			// Batch consecutive TerminalEvents for performance
			if te, ok := msg.(protocol.TerminalEvents); ok {
				batch := te.Events
			drain:
				for {
					select {
					case next, ok := <-c.Updates():
						if !ok {
							break drain
						}
						if te2, ok := next.(protocol.TerminalEvents); ok {
							batch = append(batch, te2.Events...)
						} else {
							p.Send(protocol.TerminalEvents{
								Type:   "terminal_events",
								Events: batch,
							})
							p.Send(msg)
							continue drain
						}
					default:
						break drain
					}
				}
				p.Send(protocol.TerminalEvents{
					Type:   "terminal_events",
					Events: batch,
				})
				continue
			}

			p.Send(msg)
		}
	}()

	// Outbound: read from send channel, dispatch to client.
	for msg := range s.ch {
		switch m := msg.(type) {
		case InputMsg:
			data := base64.StdEncoding.EncodeToString(m.Data)
			_ = c.Send(protocol.InputMsg{
				Type:     "input",
				RegionID: m.RegionID,
				Data:     data,
			})
		default:
			_ = c.Send(m)
		}
	}

	// Send channel closed — shut down the client, wait for inbound to finish.
	c.Close()
	<-done
}
