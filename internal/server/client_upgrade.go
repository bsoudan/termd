package server

// client_upgrade.go contains utility functions for binary version
// detection and file operations used by the upgrade handlers in
// handlers.go.

import (
	"debug/buildinfo"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveBinariesDir returns the directory where upgrade binaries are
// looked up. If configured is non-empty it is used as-is; otherwise
// the directory of the running nxtermd executable is used so that
// upgrades work out-of-the-box when binaries are placed alongside it.
func resolveBinariesDir(configured string) string {
	if configured != "" {
		return configured
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
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
	tmp, err := os.CreateTemp(dir, ".nxtermd-upgrade-*")
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
