package tui

// SessionCmd is dispatched for commands handled by SessionLayer.
type SessionCmd struct {
	Name string // command name: "open-tab", "close-tab", etc.
	Args string // optional arguments: "3" for switch-tab 3
}

// MainCmd is dispatched for commands handled by MainLayer.
type MainCmd struct {
	Name string // command name: "open-session", "detach", etc.
	Args string // optional arguments
}


// sendRawToServer forwards raw bytes as input to the active region.
func (s *SessionLayer) sendRawToServer(raw []byte) {
	id := s.activeRegionID()
	if id == "" || len(raw) == 0 {
		return
	}
	s.server.Send(InputMsg{
		RegionID: id,
		Data:     raw,
	})
}
