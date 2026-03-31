//go:build !windows

package ui

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// replaceAndExec atomically replaces the binary and exec's into it.
func replaceAndExec(tmpPath, targetPath string) error {
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, targetPath, err)
	}

	slog.Info("client upgrade: exec", "binary", targetPath)
	argv := os.Args
	if targetPath != os.Args[0] {
		argv = make([]string, len(os.Args))
		copy(argv, os.Args)
		argv[0] = targetPath
	}
	return syscall.Exec(targetPath, argv, os.Environ())
}
