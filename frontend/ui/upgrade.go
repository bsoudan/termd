package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"termd/frontend/protocol"
)

type upgradeState int

const (
	upgradeConfirm     upgradeState = iota // waiting for user to press enter
	upgradeServer                          // sent server_upgrade_request, waiting for response
	upgradeServerDone                      // server upgrade response received, waiting for reconnect
	upgradeDownloading                     // downloading client binary
	upgradeApplying                        // verifying + replacing binary
	upgradeDone                            // finished, about to exec
	upgradeError                           // error occurred
)

type startDownloadMsg struct{}

// Download accumulates binary chunks from the server goroutine.
// It is written to by dispatchInbound (server goroutine) and read
// by the UpgradeLayer (bubbletea goroutine) after completion.
type Download struct {
	mu      sync.Mutex
	file    *os.File
	hasher  hash.Hash
	written atomic.Int64
	done    chan DownloadResult // receives result when final response arrives
}

// DownloadResult is sent to bubbletea when the download completes.
type DownloadResult struct {
	SHA256 string
	Size   int64
	Err    string
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

// Complete is called when the final ClientBinaryResponse arrives.
func (d *Download) Complete(resp protocol.ClientBinaryResponse) {
	if resp.Error {
		d.done <- DownloadResult{Err: resp.Message}
	} else {
		d.done <- DownloadResult{
			SHA256: resp.SHA256,
			Size:   resp.Size,
		}
	}
}

// UpgradeLayer manages the interactive upgrade flow.
type UpgradeLayer struct {
	server    *Server
	requestFn RequestFunc
	version   string

	serverAvail bool
	serverVer   string
	clientAvail bool
	clientVer   string

	state  upgradeState
	status string
	errMsg string

	download *Download
}

func NewUpgradeLayer(server *Server, requestFn RequestFunc, version string,
	serverAvail bool, serverVer string, clientAvail bool, clientVer string,
) *UpgradeLayer {
	return &UpgradeLayer{
		server:      server,
		requestFn:   requestFn,
		version:     version,
		serverAvail: serverAvail,
		serverVer:   serverVer,
		clientAvail: clientAvail,
		clientVer:   clientVer,
		state:       upgradeConfirm,
	}
}

func (u *UpgradeLayer) Activate() tea.Cmd { return nil }
func (u *UpgradeLayer) Deactivate()       {}

func (u *UpgradeLayer) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc":
			u.cleanup()
			return QuitLayerMsg{}, nil, true
		case "enter":
			if u.state == upgradeConfirm {
				return nil, u.startUpgrade(), true
			}
		}
		return nil, nil, true

	case tea.MouseMsg:
		return nil, nil, true

	case ReconnectedMsg:
		if u.state == upgradeServerDone {
			if u.clientAvail {
				// Delay download start briefly to let the reconnect
				// message burst (session sync, screen updates) settle.
				// Starting immediately can create backpressure on the
				// shared connection that stalls both the download and
				// normal protocol traffic.
				u.state = upgradeDownloading
				u.status = "preparing download..."
				return nil, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
					return startDownloadMsg{}
				}), false
			}
			u.state = upgradeDone
			u.status = "server upgraded successfully"
			return nil, nil, false
		}
		return nil, nil, false

	case startDownloadMsg:
		if u.state == upgradeDownloading {
			return nil, u.startClientDownload(), true
		}
		return nil, nil, true

	case DownloadResult:
		if u.state != upgradeDownloading {
			return nil, nil, false
		}
		if msg.Err != "" {
			u.state = upgradeError
			u.errMsg = msg.Err
			u.cleanup()
			return nil, nil, true
		}
		u.applyClientUpgrade(msg.SHA256, msg.Size)
		return nil, nil, true
	}
	return nil, nil, false
}

func (u *UpgradeLayer) startUpgrade() tea.Cmd {
	if u.serverAvail {
		u.state = upgradeServer
		u.status = "upgrading server..."
		u.requestFn(protocol.ServerUpgradeRequest{}, func(payload any) {
			if resp, ok := payload.(protocol.ServerUpgradeResponse); ok {
				if resp.Error {
					u.state = upgradeError
					u.errMsg = resp.Message
				} else {
					u.state = upgradeServerDone
					u.status = "server upgrading, waiting for reconnect..."
				}
			}
		})
		return nil
	}
	return u.startClientDownload()
}

func (u *UpgradeLayer) startClientDownload() tea.Cmd {
	u.state = upgradeDownloading
	u.status = "downloading client binary..."

	exePath, err := os.Executable()
	if err != nil {
		u.state = upgradeError
		u.errMsg = fmt.Sprintf("os.Executable: %v", err)
		return nil
	}
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, ".termd-tui-upgrade-*")
	if err != nil {
		home, _ := os.UserHomeDir()
		binDir := filepath.Join(home, ".local", "bin")
		os.MkdirAll(binDir, 0755)
		tmp, err = os.CreateTemp(binDir, ".termd-tui-upgrade-*")
		if err != nil {
			u.state = upgradeError
			u.errMsg = fmt.Sprintf("create temp file: %v", err)
			return nil
		}
	}

	u.download = &Download{
		file:   tmp,
		hasher: sha256.New(),
		done:   make(chan DownloadResult, 1),
	}

	// Register the download with the server so dispatchInbound can
	// write chunks directly without going through bubbletea.
	u.server.SetDownload(u.download)

	u.requestFn(protocol.ClientBinaryRequest{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}, func(payload any) {
		if resp, ok := payload.(protocol.ClientBinaryResponse); ok {
			u.download.Complete(resp)
		}
	})

	// Return a tea.Cmd that blocks until the download completes.
	// Bubbletea runs this in a goroutine and delivers the result
	// as a message to the UpgradeLayer.
	dl := u.download
	srv := u.server
	return func() tea.Msg {
		result := <-dl.done
		srv.SetDownload(nil)
		return result
	}
}

func (u *UpgradeLayer) applyClientUpgrade(expectedHash string, expectedSize int64) {
	u.state = upgradeApplying
	u.status = "applying update..."

	dl := u.download
	downloaded := dl.written.Load()

	gotHash := fmt.Sprintf("%x", dl.hasher.Sum(nil))
	if gotHash != expectedHash {
		u.state = upgradeError
		u.errMsg = fmt.Sprintf("sha256 mismatch: got %s, want %s", gotHash[:16]+"...", expectedHash[:16]+"...")
		u.cleanup()
		return
	}
	if downloaded != expectedSize {
		u.state = upgradeError
		u.errMsg = fmt.Sprintf("size mismatch: got %d, want %d", downloaded, expectedSize)
		u.cleanup()
		return
	}

	tmpPath := dl.file.Name()
	dl.file.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		u.state = upgradeError
		u.errMsg = fmt.Sprintf("chmod: %v", err)
		os.Remove(tmpPath)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		u.state = upgradeError
		u.errMsg = fmt.Sprintf("os.Executable: %v", err)
		os.Remove(tmpPath)
		return
	}

	targetPath := exePath
	if filepath.Dir(tmpPath) != filepath.Dir(exePath) {
		targetPath = filepath.Join(filepath.Dir(tmpPath), "termd-tui")
	}

	if err := replaceAndExec(tmpPath, targetPath); err != nil {
		u.state = upgradeError
		u.errMsg = fmt.Sprintf("replace: %v", err)
		os.Remove(tmpPath)
		return
	}

	u.state = upgradeError
	u.errMsg = "exec returned unexpectedly"
}

func (u *UpgradeLayer) cleanup() {
	if u.download != nil {
		dl := u.download
		dl.mu.Lock()
		if dl.file != nil {
			name := dl.file.Name()
			dl.file.Close()
			os.Remove(name)
		}
		dl.mu.Unlock()
		u.server.SetDownload(nil)
		u.download = nil
	}
}

func (u *UpgradeLayer) Status() (string, lipgloss.Style) {
	switch u.state {
	case upgradeConfirm:
		return "upgrade ready", statusBold
	case upgradeServer:
		return "upgrading server...", statusBold
	case upgradeServerDone:
		return "server upgraded, reconnecting...", statusBold
	case upgradeDownloading:
		var n int64
		if u.download != nil {
			n = u.download.written.Load()
		}
		return fmt.Sprintf("downloading client... %s", formatBytes(n)), statusBold
	case upgradeApplying:
		return "applying client update...", statusBold
	case upgradeDone:
		return "upgrade complete", statusBold
	case upgradeError:
		return "upgrade failed: " + u.errMsg, statusBoldRed
	default:
		return u.status, statusBold
	}
}

func (u *UpgradeLayer) View(width, height int, active bool) []*lipgloss.Layer {
	var lines []string
	lines = append(lines, "Upgrade")
	lines = append(lines, "")

	if u.serverAvail {
		lines = append(lines, fmt.Sprintf("  Server: %s → %s", u.version, u.serverVer))
	}
	if u.clientAvail {
		lines = append(lines, fmt.Sprintf("  Client: %s → %s", u.version, u.clientVer))
	}
	lines = append(lines, "")

	switch u.state {
	case upgradeConfirm:
		lines = append(lines, "  Press enter to upgrade, q to cancel.")
	case upgradeServer:
		lines = append(lines, "  Upgrading server...")
	case upgradeServerDone:
		lines = append(lines, "  Server upgrading, waiting for reconnect...")
	case upgradeDownloading:
		var n int64
		if u.download != nil {
			n = u.download.written.Load()
		}
		lines = append(lines, fmt.Sprintf("  Downloading client binary... %s", formatBytes(n)))
	case upgradeApplying:
		lines = append(lines, "  Applying update...")
	case upgradeDone:
		lines = append(lines, "  "+u.status)
		lines = append(lines, "  Press q to close.")
	case upgradeError:
		lines = append(lines, "  Error: "+u.errMsg)
		lines = append(lines, "  Press q to close.")
	}

	content := strings.Join(lines, "\n")

	overlayW := 50
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := statusFaint.Render("• enter: upgrade • q/esc: cancel •")
	if u.state != upgradeConfirm {
		help = statusFaint.Render("• q/esc: close •")
	}
	dialogLines := strings.Split(dialog, "\n")
	helpPad := (overlayW + overlayBorder.GetHorizontalBorderSize() - lipgloss.Width(help)) / 2
	if helpPad < 0 {
		helpPad = 0
	}
	dialogLines = append(dialogLines, strings.Repeat(" ", helpPad)+help)
	dialog = strings.Join(dialogLines, "\n")

	dialogH := strings.Count(dialog, "\n") + 1
	x := (width - overlayW) / 2
	y := (height - dialogH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	return []*lipgloss.Layer{
		lipgloss.NewLayer(dialog).X(x).Y(y),
	}
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
