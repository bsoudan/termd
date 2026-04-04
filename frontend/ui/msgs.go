package ui

// LogEntryMsg is sent by the log handler to trigger a re-render when new
// log entries arrive (throttled to 100ms).
type LogEntryMsg struct{}

// ServerErrorMsg is a fatal error from the server connection.
type ServerErrorMsg struct {
	Context string
	Message string
}

type showHintMsg struct{}
type hideHintMsg struct{}
type reconnectTickMsg struct{}

// ConnectToServerMsg is emitted by ConnectLayer when the user selects an address.
type ConnectToServerMsg struct {
	Endpoint string
}

// ConnectedMsg is sent after a server connection is established from the
// connect overlay flow. Server carries the new Server instance when
// reconnecting replaces the previous connection.
type ConnectedMsg struct {
	Endpoint string
	Server   *Server
}

// ConnectErrorMsg is sent when a connection attempt from the overlay fails.
type ConnectErrorMsg struct {
	Endpoint string
	Error    string
}

// DiscoveredServerMsg is sent when mDNS discovers an nxtermd server.
type DiscoveredServerMsg struct {
	Name     string
	Endpoint string
}
