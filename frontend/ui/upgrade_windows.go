package ui

import (
	"fmt"
	"io"
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

	if err := moveFile(tmpPath, targetPath); err != nil {
		// Try to restore the old binary.
		os.Rename(oldPath, targetPath)
		return fmt.Errorf("move %s -> %s: %w", tmpPath, targetPath, err)
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

	// Close our stdin handles so InputLoop stops reading and only the
	// new process receives console input. The child already has its own
	// handle from CreateProcess — closing ours doesn't affect it.
	if PreUpgradeCleanup != nil {
		PreUpgradeCleanup()
	}
	os.Stdin.Close()

	// Wait for the new process instead of exiting immediately.
	// If we os.Exit(0) here, the parent shell (PowerShell/cmd) sees
	// its child exited and resumes reading stdin, competing with the
	// new ttui for console input.
	cmd.Wait()
	os.Exit(cmd.ProcessState.ExitCode())
	return nil // unreachable
}

// moveFile tries os.Rename first, falling back to copy+delete for
// cross-drive moves (os.Rename fails across drives on Windows).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	return copyAndRemove(src, dst)
}

func copyAndRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	os.Remove(src)
	return nil
}
