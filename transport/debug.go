package transport

import (
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
)

// InstallStackDump starts a goroutine that writes all goroutine stacks
// to /tmp/<name>.stack on SIGUSR1. The process stays alive.
func InstallStackDump(name string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			path := filepath.Join(os.TempDir(), name+".stack")
			if err := os.WriteFile(path, buf[:n], 0644); err != nil {
				slog.Debug("stack dump failed", "error", err)
				continue
			}
			slog.Debug("stack dump written", "path", path)
		}
	}()
}
