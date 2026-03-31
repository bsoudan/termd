package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"termd/frontend/protocol"
)

const chunkSize = 64 * 1024 // 64KB raw → ~85KB base64

// handleUpgradeCheck reports whether newer server and/or client binaries
// are available in the configured binaries directory.
func (c *Client) handleUpgradeCheck(msg protocol.UpgradeCheckRequest, reply func(any)) {
	slog.Debug("upgrade check", "client_id", c.id,
		"client_version", msg.ClientVersion, "os", msg.OS, "arch", msg.Arch)

	dir := c.server.binariesDir
	if dir == "" {
		reply(protocol.UpgradeCheckResponse{
			Type: "upgrade_check_response",
		})
		return
	}

	resp := protocol.UpgradeCheckResponse{Type: "upgrade_check_response"}

	// Check server binary.
	serverBin := upgradeBinPath(dir, "termd", runtime.GOOS, runtime.GOARCH)
	if v, err := binaryVersion(serverBin); err != nil {
		slog.Warn("upgrade check: server binary version failed",
			"path", serverBin, "err", err)
	} else if v != c.server.version {
		resp.ServerAvailable = true
		resp.ServerVersion = v
	}

	// Check client binary.
	clientBin := upgradeBinPath(dir, "termd-tui", msg.OS, msg.Arch)
	if v, err := binaryVersion(clientBin); err != nil {
		slog.Warn("upgrade check: client binary version failed",
			"path", clientBin, "err", err)
	} else if v != msg.ClientVersion {
		resp.ClientAvailable = true
		resp.ClientVersion = v
	}

	slog.Debug("upgrade check result", "client_id", c.id,
		"server_available", resp.ServerAvailable, "server_bin_ver", resp.ServerVersion,
		"client_available", resp.ClientAvailable, "client_bin_ver", resp.ClientVersion,
		"running_server_ver", c.server.version, "running_client_ver", msg.ClientVersion)
	reply(resp)
}

// handleServerUpgrade copies the new server binary over the current
// executable, sends a success response, then signals itself with SIGUSR2
// to trigger the existing live-upgrade machinery.
func (c *Client) handleServerUpgrade(reply func(any)) {
	slog.Info("server upgrade requested", "client_id", c.id)

	dir := c.server.binariesDir
	if dir == "" {
		reply(protocol.ServerUpgradeResponse{
			Type: "server_upgrade_response", Error: true,
			Message: "no binaries directory configured",
		})
		return
	}

	srcPath := upgradeBinPath(dir, "termd", runtime.GOOS, runtime.GOARCH)
	dstPath, err := os.Executable()
	if err != nil {
		reply(protocol.ServerUpgradeResponse{
			Type: "server_upgrade_response", Error: true,
			Message: fmt.Sprintf("os.Executable: %v", err),
		})
		return
	}

	if err := copyFile(srcPath, dstPath); err != nil {
		reply(protocol.ServerUpgradeResponse{
			Type: "server_upgrade_response", Error: true,
			Message: fmt.Sprintf("copy binary: %v", err),
		})
		return
	}
	slog.Info("server binary replaced", "src", srcPath, "dst", dstPath)

	reply(protocol.ServerUpgradeResponse{Type: "server_upgrade_response"})

	// Trigger the existing live-upgrade path.
	slog.Info("sending SIGUSR2 to self")
	syscall.Kill(os.Getpid(), syscall.SIGUSR2)
}

// handleClientBinaryDownload streams the requested client binary in chunks,
// followed by a final response with the SHA-256 hash. Streaming runs in a
// separate goroutine to avoid blocking the client's readLoop.
func (c *Client) handleClientBinaryDownload(msg protocol.ClientBinaryRequest, reply func(any)) {
	slog.Debug("client binary download", "client_id", c.id,
		"os", msg.OS, "arch", msg.Arch, "offset", msg.Offset)

	dir := c.server.binariesDir
	if dir == "" {
		reply(protocol.ClientBinaryResponse{
			Type: "client_binary_response", Error: true,
			Message: "no binaries directory configured",
		})
		return
	}

	path := upgradeBinPath(dir, "termd-tui", msg.OS, msg.Arch)
	f, err := os.Open(path)
	if err != nil {
		reply(protocol.ClientBinaryResponse{
			Type: "client_binary_response", Error: true,
			Message: fmt.Sprintf("open binary: %v", err),
		})
		return
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		reply(protocol.ClientBinaryResponse{
			Type: "client_binary_response", Error: true,
			Message: fmt.Sprintf("stat binary: %v", err),
		})
		return
	}

	if msg.Offset > 0 {
		if _, err := f.Seek(msg.Offset, io.SeekStart); err != nil {
			f.Close()
			reply(protocol.ClientBinaryResponse{
				Type: "client_binary_response", Error: true,
				Message: fmt.Sprintf("seek: %v", err),
			})
			return
		}
	}

	// Stream in a goroutine so the readLoop isn't blocked during transfer.
	go c.streamBinary(f, info.Size(), msg.Offset, path, reply)
}

func (c *Client) streamBinary(f *os.File, fileSize, startOffset int64, path string, reply func(any)) {
	defer f.Close()

	hasher := sha256.New()
	if startOffset > 0 {
		f2, _ := os.Open(path)
		if f2 != nil {
			io.CopyN(hasher, f2, startOffset)
			f2.Close()
		}
	}

	buf := make([]byte, chunkSize)
	offset := startOffset
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			hasher.Write(buf[:n])
			final := readErr == io.EOF || (offset+int64(n)) >= fileSize
			c.sendReply(protocol.ClientBinaryChunk{
				Type:   "client_binary_chunk",
				Offset: offset,
				Data:   base64.StdEncoding.EncodeToString(buf[:n]),
				Final:  final,
			}, 0)
			offset += int64(n)
			// Throttle to avoid overwhelming the client's message
			// processing pipeline. The client shares a single connection
			// for both protocol messages and binary data, so sustained
			// bursts can create backpressure that stalls other traffic.
			time.Sleep(5 * time.Millisecond)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			reply(protocol.ClientBinaryResponse{
				Type: "client_binary_response", Error: true,
				Message: fmt.Sprintf("read: %v", readErr),
			})
			return
		}
	}

	reply(protocol.ClientBinaryResponse{
		Type:   "client_binary_response",
		SHA256: fmt.Sprintf("%x", hasher.Sum(nil)),
		Size:   fileSize,
	})
	slog.Info("client binary sent", "client_id", c.id,
		"path", path, "size", fileSize)
}

// upgradeBinPath returns the full path for an upgrade binary, adding
// .exe for Windows targets.
func upgradeBinPath(dir, base, goos, goarch string) string {
	name := fmt.Sprintf("%s-%s-%s", base, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

// binaryVersion returns the version string embedded in the binary at path.
// It first tries executing the binary with --version (fast, works for
// same-platform binaries). If that fails (cross-platform binary, missing
// interpreter, etc.), it falls back to reading Go build info from the
// file, which works regardless of platform.
func binaryVersion(path string) (string, error) {
	if v, err := binaryVersionExec(path); err == nil {
		return v, nil
	}
	return binaryVersionBuildInfo(path)
}

// binaryVersionExec runs the binary with --version and parses the output.
func binaryVersionExec(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", err
	}
	// urfave/cli outputs: "appname version vX.Y.Z" or similar.
	// Extract the last whitespace-delimited token from the first line.
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty version output from %s", path)
	}
	return fields[len(fields)-1], nil
}

// binaryVersionBuildInfo reads the Go build info embedded in the binary
// and extracts the version from -ldflags "-X main.version=...".
func binaryVersionBuildInfo(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read build info from %s: %w", path, err)
	}
	for _, s := range info.Settings {
		if s.Key != "-ldflags" {
			continue
		}
		// Parse "-X main.version=VALUE" from the ldflags string.
		const prefix = "-X main.version="
		idx := strings.Index(s.Value, prefix)
		if idx < 0 {
			break
		}
		v := s.Value[idx+len(prefix):]
		// The value extends to the next space or end of string.
		if sp := strings.IndexByte(v, ' '); sp >= 0 {
			v = v[:sp]
		}
		if v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("no version in build info of %s", path)
}

// copyFile atomically replaces dst with the contents of src.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	// Write to a temp file in the same directory, then rename.
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".termd-upgrade-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(info.Mode()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, dst)
}
