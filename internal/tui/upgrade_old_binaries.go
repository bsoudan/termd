package tui

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cleanupStaleOldBinaries removes "<target>.old" and "<target>.old.<n>"
// files left over from previous upgrades. On Windows, files whose old
// process is still running fail to delete (locked) and are silently
// skipped — they'll be cleaned up by a future upgrade after that
// process exits. This is only used by the Windows upgrade path; on
// Unix the old binary is unlinked atomically.
func cleanupStaleOldBinaries(targetPath string) {
	dir := filepath.Dir(targetPath)
	base := filepath.Base(targetPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := base + ".old"
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Match either the legacy ".old" name or ".old.<n>" suffixes.
		suffix := name[len(prefix):]
		if suffix != "" {
			if !strings.HasPrefix(suffix, ".") {
				continue
			}
			if _, err := strconv.ParseInt(suffix[1:], 10, 64); err != nil {
				continue
			}
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			slog.Debug("upgrade: stale old binary still locked", "path", path, "err", err)
		} else {
			slog.Debug("upgrade: removed stale old binary", "path", path)
		}
	}
}
