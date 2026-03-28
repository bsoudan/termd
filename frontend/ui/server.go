package ui

import (
	"encoding/base64"
	"fmt"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/client"
	"termd/frontend/protocol"
)

// InputMsg is sent by the raw input loop or bubbletea model to forward
// raw bytes to the child process via the server.
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
// if the channel is full (same behavior as the old client.Send default case).
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
							// Send the batched events, then the non-event message
							p.Send(TerminalEventsMsg{Events: batch})
							if teaMsg := convertProtocolMsg(next); teaMsg != nil {
								p.Send(teaMsg)
							}
							continue drain
						}
					default:
						break drain
					}
				}
				p.Send(TerminalEventsMsg{Events: batch})
				continue
			}

			if teaMsg := convertProtocolMsg(msg); teaMsg != nil {
				p.Send(teaMsg)
			}
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

// convertProtocolMsg converts a protocol-layer message to the corresponding
// tea.Msg for bubbletea. Returns nil for unrecognized types.
func convertProtocolMsg(msg any) tea.Msg {
	switch m := msg.(type) {
	case protocol.Identify:
		return ServerIdentifyMsg{Hostname: m.Hostname}
	case protocol.ScreenUpdate:
		return ScreenUpdateMsg{RegionID: m.RegionID, CursorRow: m.CursorRow, CursorCol: m.CursorCol, Lines: m.Lines, Cells: m.Cells}
	case protocol.TerminalEvents:
		return TerminalEventsMsg{RegionID: m.RegionID, Events: m.Events}
	case protocol.RegionCreated:
		return RegionCreatedMsg{RegionID: m.RegionID, Name: m.Name}
	case protocol.RegionDestroyed:
		return RegionDestroyedMsg{RegionID: m.RegionID}
	case protocol.SpawnResponse:
		return SpawnResponseMsg{
			RegionID: m.RegionID, Name: m.Name,
			Error: m.Error, Message: m.Message,
		}
	case protocol.SubscribeResponse:
		return SubscribeResponseMsg{
			RegionID: m.RegionID,
			Error: m.Error, Message: m.Message,
		}
	case protocol.ResizeResponse:
		return ResizeResponseMsg{
			RegionID: m.RegionID,
			Error: m.Error, Message: m.Message,
		}
	case protocol.ListRegionsResponse:
		return ListRegionsResponseMsg{
			Regions: m.Regions,
			Error: m.Error, Message: m.Message,
		}
	case protocol.GetScreenResponse:
		return ScreenUpdateMsg{RegionID: m.RegionID, CursorRow: m.CursorRow, CursorCol: m.CursorCol, Lines: m.Lines, Cells: m.Cells}
	case protocol.StatusResponse:
		return m
	case protocol.GetScrollbackResponse:
		return ScrollbackResponseMsg{Lines: m.Lines}
	case client.DisconnectedMsg:
		return DisconnectedMsg{RetryAt: m.RetryAt}
	case client.ReconnectedMsg:
		return ReconnectedMsg{}
	default:
		slog.Debug("unrecognized server message", "type", fmt.Sprintf("%T", m))
		return nil
	}
}

