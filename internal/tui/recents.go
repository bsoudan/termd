package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const maxRecents = 20

// RecentServer is a previously connected server address.
type RecentServer struct {
	Address   string    `json:"address"`
	Label     string    `json:"label"`
	Timestamp time.Time `json:"timestamp"`
}

// recentAddress combines a dial endpoint with an optional session name
// into the canonical "session@endpoint" form used by the recents list,
// or returns the endpoint unchanged when no session was specified.
func recentAddress(endpoint, session string) string {
	if session == "" {
		return endpoint
	}
	return session + "@" + endpoint
}

// LoadRecents reads the recents file. Returns empty slice on any error.
func LoadRecents() []RecentServer {
	data, err := os.ReadFile(recentsPath())
	if err != nil {
		return nil
	}
	var recents []RecentServer
	if err := json.Unmarshal(data, &recents); err != nil {
		return nil
	}
	return recents
}

// SaveRecent adds or promotes an address to the top of the recents list.
func SaveRecent(address, label string) error {
	recents := LoadRecents()

	// Remove existing entry with same address.
	n := 0
	for _, r := range recents {
		if r.Address != address {
			recents[n] = r
			n++
		}
	}
	recents = recents[:n]

	// Prepend new entry.
	entry := RecentServer{Address: address, Label: label, Timestamp: time.Now()}
	recents = append([]RecentServer{entry}, recents...)

	if len(recents) > maxRecents {
		recents = recents[:maxRecents]
	}

	p := recentsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(recents, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// RemoveRecent removes the entry with the given address from the
// recents file. It is a no-op if no matching entry exists.
func RemoveRecent(address string) error {
	recents := LoadRecents()

	n := 0
	for _, r := range recents {
		if r.Address != address {
			recents[n] = r
			n++
		}
	}
	if n == len(recents) {
		return nil
	}
	recents = recents[:n]

	p := recentsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(recents, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func recentsPath() string {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg != "" {
		return filepath.Join(xdg, "nxterm", "recents.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "nxterm", "recents.json")
}
