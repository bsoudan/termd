package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"termd/frontend/protocol"
)

// PreUpgradeCleanup is called before starting the new process during a
// client upgrade. The main package sets this to close the duplicated
// stdin handle so InputLoop stops reading and only the new process
// receives console input. On Unix this is unused (syscall.Exec replaces
// the process).
var PreUpgradeCleanup func()

// Download accumulates binary chunks from the server goroutine.
// It is written to by dispatchInbound (server goroutine) and read
// by the task goroutine after the final response arrives.
type Download struct {
	mu      sync.Mutex
	file    *os.File
	hasher  hash.Hash
	written atomic.Int64
}

// HandleChunk decodes and writes a single chunk. Called from the server
// goroutine — must not touch bubbletea state.
func (d *Download) HandleChunk(chunk protocol.ClientBinaryChunk) {
	data, err := base64.StdEncoding.DecodeString(chunk.Data)
	if err != nil {
		return // final response will catch hash mismatch
	}
	d.mu.Lock()
	d.hasher.Write(data)
	d.file.Write(data)
	d.mu.Unlock()
	d.written.Add(int64(len(data)))
}

func cleanupDownload(dl *Download, server *Server) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	if dl.file != nil {
		name := dl.file.Name()
		dl.file.Close()
		os.Remove(name)
	}
	dl.mu.Unlock()
	server.SetDownload(nil)
}

// upgradeTask runs the interactive upgrade flow as a synchronous task.
func upgradeTask(t *TermdHandle, server *Server,
	serverAvail bool, serverVer string,
	clientAvail bool, clientVer string, version string) {

	// Build confirmation lines.
	var infoLines []string
	if serverAvail {
		infoLines = append(infoLines, fmt.Sprintf("  Server: %s → %s", version, serverVer))
	}
	if clientAvail {
		infoLines = append(infoLines, fmt.Sprintf("  Client: %s → %s", version, clientVer))
	}
	infoLines = append(infoLines, "", "  Press enter to upgrade, q to cancel.")

	overlay := &Overlay{
		Title:      "Upgrade",
		Lines:      infoLines,
		Help:       "enter: upgrade • q/esc: cancel",
		StatusText: "upgrade ready",
	}
	t.PushLayer(overlay)
	defer t.PopLayer(overlay)

	// Wait for confirmation.
	msg, err := t.WaitFor(IsKeyPress)
	if err != nil {
		return
	}
	if msg.(tea.KeyPressMsg).String() != "enter" {
		return
	}

	// Upgrade server.
	if serverAvail {
		overlay.Lines = infoLines[:len(infoLines)-1] // remove prompt
		overlay.Lines = append(overlay.Lines, "  Upgrading server...")
		overlay.Help = "q/esc: cancel"
		overlay.StatusText = "upgrading server..."

		resp, err := t.Request(protocol.ServerUpgradeRequest{})
		if err != nil {
			return
		}
		sr, ok := resp.(protocol.ServerUpgradeResponse)
		if !ok {
			return
		}
		if sr.Error {
			ShowError(overlay, t.Handle, sr.Message)
			return
		}

		overlay.Lines = infoLines[:len(infoLines)-1]
		overlay.Lines = append(overlay.Lines, "  Server upgrading, waiting for reconnect...")
		overlay.StatusText = "server upgraded, reconnecting..."

		_, err = t.WaitFor(func(msg any) (bool, bool) {
			_, ok := msg.(ReconnectedMsg)
			return ok, false // deliver but don't consume — layers need it
		})
		if err != nil {
			return
		}

		if !clientAvail {
			overlay.Lines = infoLines[:len(infoLines)-1]
			overlay.Lines = append(overlay.Lines, "  Server upgraded successfully.", "", "  Press any key to close.")
			overlay.Help = "any key: close"
			overlay.StatusText = "upgrade complete"
			t.WaitFor(IsKeyPress)
			return
		}

		// Delay download start briefly to let the reconnect message burst
		// (session sync, screen updates) settle.
		time.Sleep(500 * time.Millisecond)
	}

	// Download client binary.
	dl, err := setupDownload(server)
	if err != nil {
		ShowError(overlay, t.Handle, err.Error())
		return
	}
	defer cleanupDownload(dl, server)

	overlay.Lines = infoLines[:len(infoLines)-1]
	overlay.Lines = append(overlay.Lines, "  Downloading client binary...")
	overlay.StatusText = "downloading client..."

	resp, err := t.Request(protocol.ClientBinaryRequest{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	})
	if err != nil {
		return
	}
	cbr, ok := resp.(protocol.ClientBinaryResponse)
	if !ok {
		return
	}
	if cbr.Error {
		ShowError(overlay, t.Handle, cbr.Message)
		return
	}
	server.SetDownload(nil)

	// Verify and apply.
	applyClientUpgrade(overlay, t, dl, server, cbr.SHA256, cbr.Size)
}

func setupDownload(server *Server) (*Download, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("os.Executable: %v", err)
	}
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, ".termd-tui-upgrade-*")
	if err != nil {
		home, _ := os.UserHomeDir()
		binDir := filepath.Join(home, ".local", "bin")
		os.MkdirAll(binDir, 0755)
		tmp, err = os.CreateTemp(binDir, ".termd-tui-upgrade-*")
		if err != nil {
			return nil, fmt.Errorf("create temp file: %v", err)
		}
	}

	dl := &Download{
		file:   tmp,
		hasher: sha256.New(),
	}
	server.SetDownload(dl)
	return dl, nil
}

func applyClientUpgrade(overlay *Overlay, t *TermdHandle, dl *Download, server *Server, expectedHash string, expectedSize int64) {
	overlay.Lines = []string{"  Applying update..."}
	overlay.StatusText = "applying client update..."

	downloaded := dl.written.Load()
	gotHash := fmt.Sprintf("%x", dl.hasher.Sum(nil))
	if gotHash != expectedHash {
		ShowError(overlay, t.Handle, fmt.Sprintf("sha256 mismatch: got %s, want %s", gotHash[:16]+"...", expectedHash[:16]+"..."))
		return
	}
	if downloaded != expectedSize {
		ShowError(overlay, t.Handle, fmt.Sprintf("size mismatch: got %d, want %d", downloaded, expectedSize))
		return
	}

	tmpPath := dl.file.Name()
	dl.file.Close()
	dl.file = nil // prevent cleanup from removing it

	if err := os.Chmod(tmpPath, 0755); err != nil {
		ShowError(overlay, t.Handle, fmt.Sprintf("chmod: %v", err))
		os.Remove(tmpPath)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		ShowError(overlay, t.Handle, fmt.Sprintf("os.Executable: %v", err))
		os.Remove(tmpPath)
		return
	}

	targetPath := exePath
	if filepath.Dir(tmpPath) != filepath.Dir(exePath) {
		name := filepath.Base(exePath)
		if name == "" || name == "." {
			name = "termd-tui"
		}
		targetPath = filepath.Join(filepath.Dir(tmpPath), name)
	}

	// Disconnect from the server before exec'ing the new process.
	// On Windows the old process stays alive (waiting for the child),
	// so without this both processes would be connected to the server
	// and rendering to stdout, causing doubled keystrokes and output.
	server.Close()

	if err := replaceAndExec(tmpPath, targetPath); err != nil {
		ShowError(overlay, t.Handle, fmt.Sprintf("replace: %v", err))
		os.Remove(tmpPath)
		return
	}

	// replaceAndExec should not return, but just in case:
	ShowError(overlay, t.Handle, "exec returned unexpectedly")
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
