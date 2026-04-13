package tui

// SessionCmd is dispatched for commands handled by SessionLayer.
type SessionCmd struct {
	Name string // command name: "open-tab", "close-tab", "upgrade", etc.
	Args string // optional arguments: "3" for switch-tab 3
}

// SessionManagerCmd is dispatched for commands handled by SessionManagerLayer.
type SessionManagerCmd struct {
	Name string // command name: "open-session", "close-session", etc.
	Args string // optional arguments
}

// MainCmd is dispatched for commands handled by NxtermModel.
type MainCmd struct {
	Name string // command name: "detach", "show-help", etc.
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
