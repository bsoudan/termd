package ui

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

// replaceAndExec replaces the running binary and starts the new one.
// Windows doesn't allow overwriting a running executable, but it does
// allow renaming it. So we rename the old binary out of the way, move
// the new one into place, then launch a new process and exit.
func replaceAndExec(tmpPath, targetPath string) error {
	oldPath := targetPath + ".old"
	os.Remove(oldPath) // clean up any previous .old file

	if err := os.Rename(targetPath, oldPath); err != nil {
		return fmt.Errorf("rename running binary %s -> %s: %w", targetPath, oldPath, err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		// Try to restore the old binary.
		os.Rename(oldPath, targetPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, targetPath, err)
	}

	slog.Info("client upgrade: starting new process", "binary", targetPath)
	argv := os.Args[1:]
	cmd := exec.Command(targetPath, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start new process: %w", err)
	}

	os.Exit(0)
	return nil // unreachable
}
