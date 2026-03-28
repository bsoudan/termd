package main

import (
	"encoding/base64"
	"log/slog"
	"sync"

	"termd/frontend/client"
	"termd/frontend/protocol"
	"termd/frontend/terminal"

	te "github.com/rcarmo/go-te/pkg/te"
)

// session manages the connection lifecycle with the termd server.
// It runs in a goroutine and communicates state changes back to the
// GUI via callbacks.
type session struct {
	client    *client.Client
	shell     string
	shellArgs []string

	mu         sync.Mutex
	screen     *te.Screen
	regionID   string
	regionName string
	connStatus string // "connected", "reconnecting"
	cols       int
	rows       int

	// onUpdate is called (from the session goroutine) whenever the screen
	// changes and the widget should refresh.
	onUpdate func()
	// onStatus is called when connection status changes.
	onStatus func(status string)
}

func newSession(c *client.Client, shell string, shellArgs []string) *session {
	return &session{
		client:     c,
		shell:      shell,
		shellArgs:  shellArgs,
		connStatus: "connected",
		cols:       80,
		rows:       24,
	}
}

// run is the main session loop. It sends the initial messages and processes
// all inbound messages from the server. Blocks until the client channel closes.
func (s *session) run() {
	// Request the region list to attach to an existing region or spawn a new one.
	_ = s.client.Send(protocol.ListRegionsRequest{Type: "list_regions_request"})

	for msg := range s.client.Updates() {
		switch m := msg.(type) {
		case protocol.Identify:
			slog.Debug("server identified", "hostname", m.Hostname)

		case protocol.ListRegionsResponse:
			if m.Error {
				slog.Error("list regions failed", "message", m.Message)
				continue
			}
			if len(m.Regions) > 0 {
				s.mu.Lock()
				s.regionID = m.Regions[0].RegionID
				s.regionName = m.Regions[0].Name
				s.mu.Unlock()
				_ = s.client.Send(protocol.SubscribeRequest{
					Type:     "subscribe_request",
					RegionID: m.Regions[0].RegionID,
				})
			} else {
				_ = s.client.Send(protocol.SpawnRequest{
					Type: "spawn_request",
					Cmd:  s.shell,
					Args: s.shellArgs,
				})
			}

		case protocol.SpawnResponse:
			if m.Error {
				slog.Error("spawn failed", "message", m.Message)
				continue
			}
			s.mu.Lock()
			s.regionID = m.RegionID
			s.regionName = m.Name
			s.mu.Unlock()
			_ = s.client.Send(protocol.SubscribeRequest{
				Type:     "subscribe_request",
				RegionID: m.RegionID,
			})

		case protocol.SubscribeResponse:
			if m.Error {
				slog.Error("subscribe failed", "message", m.Message)
				continue
			}
			s.mu.Lock()
			regionID := s.regionID
			cols := s.cols
			rows := s.rows
			s.mu.Unlock()
			_ = s.client.Send(protocol.ResizeRequest{
				Type:     "resize_request",
				RegionID: regionID,
				Width:    uint16(cols),
				Height:   uint16(rows),
			})

		case protocol.ScreenUpdate:
			s.handleScreenUpdate(m.Lines, m.Cells, int(m.CursorRow), int(m.CursorCol))

		case protocol.GetScreenResponse:
			s.handleScreenUpdate(m.Lines, m.Cells, int(m.CursorRow), int(m.CursorCol))

		case protocol.TerminalEvents:
			s.handleTerminalEvents(m.Events)

		case protocol.RegionDestroyed:
			slog.Info("region destroyed", "region_id", m.RegionID)

		case client.DisconnectedMsg:
			s.mu.Lock()
			s.connStatus = "reconnecting"
			s.mu.Unlock()
			if s.onStatus != nil {
				s.onStatus("reconnecting")
			}

		case client.ReconnectedMsg:
			s.mu.Lock()
			s.connStatus = "connected"
			regionID := s.regionID
			cols := s.cols
			rows := s.rows
			s.mu.Unlock()
			if s.onStatus != nil {
				s.onStatus("connected")
			}
			if regionID != "" {
				_ = s.client.Send(protocol.SubscribeRequest{
					Type:     "subscribe_request",
					RegionID: regionID,
				})
				_ = s.client.Send(protocol.ResizeRequest{
					Type:     "resize_request",
					RegionID: regionID,
					Width:    uint16(cols),
					Height:   uint16(rows),
				})
			}

		default:
			slog.Debug("unhandled message", "type_", msg)
		}
	}
}

func (s *session) handleScreenUpdate(lines []string, cells [][]protocol.ScreenCell, cursorRow, cursorCol int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cols := s.cols
	rows := s.rows
	s.screen = te.NewScreen(cols, rows)

	if len(cells) > 0 {
		terminal.InitScreenFromCells(s.screen, cells)
	} else {
		for i, line := range lines {
			if i > 0 {
				s.screen.LineFeed()
				s.screen.CarriageReturn()
			}
			s.screen.Draw(line)
		}
	}
	s.screen.CursorPosition(cursorRow+1, cursorCol+1)

	if s.onUpdate != nil {
		s.onUpdate()
	}
}

func (s *session) handleTerminalEvents(events []protocol.TerminalEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.screen == nil {
		return
	}
	terminal.ReplayEvents(s.screen, events)

	// Drain any additional pending terminal events.
drain:
	for {
		select {
		case pending, ok := <-s.client.Updates():
			if !ok {
				break drain
			}
			if te, ok := pending.(protocol.TerminalEvents); ok {
				terminal.ReplayEvents(s.screen, te.Events)
			} else {
				// Put it back — but we can't un-receive from a channel.
				// For now, just log and skip. A proper solution would use
				// a separate pending queue.
				slog.Debug("non-events message during drain, skipped", "type_", pending)
				break drain
			}
		default:
			break drain
		}
	}

	if s.onUpdate != nil {
		s.onUpdate()
	}
}

// sendInput sends raw bytes to the server as PTY input.
func (s *session) sendInput(data []byte) {
	s.mu.Lock()
	regionID := s.regionID
	s.mu.Unlock()

	if regionID == "" {
		return
	}
	_ = s.client.Send(protocol.InputMsg{
		Type:     "input",
		RegionID: regionID,
		Data:     base64.StdEncoding.EncodeToString(data),
	})
}

// resize tells the server to resize the PTY.
func (s *session) resize(cols, rows int) {
	s.mu.Lock()
	s.cols = cols
	s.rows = rows
	regionID := s.regionID
	if s.screen != nil {
		s.screen = te.NewScreen(cols, rows)
	}
	s.mu.Unlock()

	if regionID == "" {
		return
	}
	_ = s.client.Send(protocol.ResizeRequest{
		Type:     "resize_request",
		RegionID: regionID,
		Width:    uint16(cols),
		Height:   uint16(rows),
	})
	_ = s.client.Send(protocol.GetScreenRequest{
		Type:     "get_screen_request",
		RegionID: regionID,
	})
}

// getScreen returns a snapshot of the current go-te screen under lock.
// The caller must not retain the screen pointer.
func (s *session) getScreen() *te.Screen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen
}

func (s *session) getRegionName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.regionName
}

func (s *session) getConnStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connStatus
}
