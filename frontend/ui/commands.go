package ui

import tea "charm.land/bubbletea/v2"

// Command messages dispatched by CommandLayer and HelpLayer for session to handle.
type (
	DetachRequestMsg     struct{}              // graceful detach
	SendLiteralPrefixMsg struct{}              // send literal ctrl+b to server
	OpenOverlayMsg       struct{ Name string } // open named overlay
	EnterScrollbackMsg   struct{}              // enter terminal scrollback mode
	RefreshScreenMsg     struct{}              // refresh terminal screen
	SpawnRegionMsg       struct{}              // spawn a new region (triggers picker if >1 program)
	SpawnProgramMsg      struct{ Name string } // spawn a specific program by name
	SwitchTabMsg         struct{ Index int }   // switch to tab by 0-based index
	CloseTabMsg          struct{}              // kill the active tab's region
)

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

type helpItem struct {
	key    string
	label  string
	action func() tea.Cmd
}

// helpItems defines the ctrl+b help menu. Actions return tea.Cmd that
// produce command messages — same messages as CommandLayer dispatches.
var helpItems = []helpItem{
	{"c", "new region", func() tea.Cmd { return cmdMsg(SpawnRegionMsg{}) }},
	{"x", "close tab", func() tea.Cmd { return cmdMsg(CloseTabMsg{}) }},
	{"1-9", "switch tab", nil},
	{"d", "detach", func() tea.Cmd { return cmdMsg(DetachRequestMsg{}) }},
	{"l", "log viewer", func() tea.Cmd { return cmdMsg(OpenOverlayMsg{Name: "logviewer"}) }},
	{"s", "status", func() tea.Cmd { return cmdMsg(OpenOverlayMsg{Name: "status"}) }},
	{"n", "release notes", func() tea.Cmd { return cmdMsg(OpenOverlayMsg{Name: "release notes"}) }},
	{"r", "refresh screen", func() tea.Cmd { return cmdMsg(RefreshScreenMsg{}) }},
	{"[", "scrollback", func() tea.Cmd { return cmdMsg(EnterScrollbackMsg{}) }},
	{"b", "send literal ctrl+b", func() tea.Cmd { return cmdMsg(SendLiteralPrefixMsg{}) }},
}
