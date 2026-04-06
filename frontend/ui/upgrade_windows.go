package ui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var procFreeConsole = syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole")

// replaceAndExec replaces the running binary and starts the new one.
// Windows doesn't allow overwriting a running executable, but it does
// allow renaming it. So we rename the old binary out of the way, move
// the new one into place, then launch a new process and exit.
//
// We use a unique <target>.old.<nanos> suffix per upgrade, never a
// fixed ".old", because the old process keeps running while waiting
// on the new one (cmd.Wait below). On a chained upgrade #2 the file
// from upgrade #1 is still locked by process #1, so a fixed ".old"
// slot would collide. Stale .old.* files from earlier upgrades whose
// processes have exited are cleaned up on a best-effort basis.
func replaceAndExec(tmpPath, targetPath string) error {
	cleanupStaleOldBinaries(targetPath)

	oldPath := fmt.Sprintf("%s.old.%d", targetPath, time.Now().UnixNano())

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

	// Detach from the console. The child already has its own console
	// handles from CreateProcess. FreeConsole causes all pending
	// ReadConsole/WriteConsole calls in THIS process to fail
	// immediately, which unblocks the stdin reader, the renderer,
	// and any goroutine backed up behind them. This is far more
	// reliable than closing individual handles (which doesn't cancel
	// pending reads on Windows) or closing Go channels (which can't
	// be processed when the event loop is backed up behind a lock).
	procFreeConsole.Call()

	if PreUpgradeCleanup != nil {
		PreUpgradeCleanup()
	}

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
