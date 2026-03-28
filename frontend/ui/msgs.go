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
