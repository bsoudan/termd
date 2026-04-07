package e2e

import (
	"testing"
	"time"
)

func TestPrefixKeyDetach(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	fe := startFrontendFull(t, socketPath)
	defer fe.Kill()

	fe.WaitFor(t, "nxterm$", 10*time.Second)

	fe.Write([]byte{0x02, 'd'})

	// Process should exit cleanly with code 0, no panic
	if err := fe.Wait(5 * time.Second); err != nil {
		t.Fatalf("frontend exited with error: %v", err)
	}
}

func TestPrefixKeyLiteralCtrlB(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$",10*time.Second)

	pio.Write([]byte("cat -v\r"))
	pio.WaitFor(t, "cat -v", 10*time.Second)

	pio.Write([]byte{0x02, 0x02})
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines, "^B")
		return row >= 0
	}, "'^B' on screen", 10*time.Second)

	// "^B" should be at col 0 (cat -v output)
	row, col := findOnScreen(lines, "^B")
	t.Logf("'^B' at row %d, col %d", row, col)
	if col != 0 {
		t.Fatalf("expected '^B' at col 0, found at col %d", col)
	}

	pio.Write([]byte("\x03"))
	pio.WaitFor(t, "nxterm$",10*time.Second)
}

func TestPrefixKeyStatusIndicator(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)
	pio.WaitForSilence(500 * time.Millisecond)

	pio.Write([]byte{0x02})
	lines := pio.WaitForScreen(t, func(lines []string) bool {
		row, col := findOnScreen(lines, "? ")
		return row == 0 && col > 50
	}, "'?' right-justified on row 0", 3*time.Second)

	row, col := findOnScreen(lines, "? ")
	t.Logf("'?' at row %d, col %d", row, col)
	if row != 0 {
		t.Fatalf("expected prefix indicator on row 0, found on row %d", row)
	}

	// Dismiss (press an unbound key) and verify it clears
	pio.Write([]byte("z"))
	pio.Write([]byte("echo prefix_cleared\r"))
	pio.WaitForScreen(t, func(lines []string) bool {
		row, _ := findOnScreen(lines[1:], "prefix_cleared")
		return row >= 0
	}, "'prefix_cleared' on screen", 10*time.Second)
}

func TestKeybindNativeNextPrevTab(t *testing.T) {
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	pio, frontendCleanup := startFrontend(t, socketPath)
	defer frontendCleanup()

	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Mark tab 1
	pio.Write([]byte("echo TAB1_NATIVE\r"))
	pio.WaitFor(t, "TAB1_NATIVE", 10*time.Second)

	// Spawn second tab (ctrl+b c). Tab 1 becomes inactive → "1:bash"
	// appears in the tab bar; that's our signal the spawn took effect.
	pio.Write([]byte("\x02c"))
	pio.WaitFor(t, "1:bash", 10*time.Second)
	pio.WaitFor(t, "nxterm$", 10*time.Second)

	// Mark tab 2
	pio.Write([]byte("echo TAB2_NATIVE\r"))
	pio.WaitFor(t, "TAB2_NATIVE", 10*time.Second)

	// Alt+, (prev-tab) → should go back to tab 1
	pio.Write([]byte("\x1b,"))
	pio.WaitFor(t, "TAB1_NATIVE", 10*time.Second)

	// Alt+. (next-tab) → should go back to tab 2
	pio.Write([]byte("\x1b."))
	pio.WaitFor(t, "TAB2_NATIVE", 10*time.Second)
}

func TestKeybindTmuxStyle(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	writeTestKeybindConfig(t, env, `style = "tmux"`)

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "nxterm$", 10*time.Second)

	// Mark tab 1
	fe.Write([]byte("echo TAB1_TMUX\r"))
	fe.WaitFor(t, "TAB1_TMUX", 10*time.Second)

	// Spawn second tab (ctrl+b c — same as tmux). Tab 1 becomes
	// inactive → "1:bash" appears in the tab bar.
	fe.Write([]byte("\x02c"))
	fe.WaitFor(t, "1:bash", 10*time.Second)
	fe.WaitFor(t, "nxterm$", 10*time.Second)

	// Mark tab 2
	fe.Write([]byte("echo TAB2_TMUX\r"))
	fe.WaitFor(t, "TAB2_TMUX", 10*time.Second)

	// ctrl+b p (prev-tab in tmux) → should go to tab 1
	fe.Write([]byte("\x02p"))
	fe.WaitFor(t, "TAB1_TMUX", 10*time.Second)

	// ctrl+b n (next-tab in tmux) → should go to tab 2
	fe.Write([]byte("\x02n"))
	fe.WaitFor(t, "TAB2_TMUX", 10*time.Second)
}

func TestKeybindScreenPrefix(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	writeTestKeybindConfig(t, env, `style = "screen"`)

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "nxterm$", 10*time.Second)

	// ctrl+a d (detach in screen style; ctrl+a = 0x01)
	fe.Write([]byte("\x01d"))

	// Frontend should exit with detach
	if err := fe.Wait(10 * time.Second); err != nil {
		t.Fatalf("frontend did not exit after screen-style detach: %v", err)
	}
}

func TestKeybindCustomOverride(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	// Rebind ctrl+b x from close-tab to detach
	writeTestKeybindConfig(t, env, "style = \"native\"\n\n[main]\ndetach = [\"d\", \"x\"]\n")

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "nxterm$", 10*time.Second)

	// ctrl+b x should now detach (instead of closing the tab)
	fe.Write([]byte("\x02x"))

	if err := fe.Wait(10 * time.Second); err != nil {
		t.Fatalf("frontend did not exit after override detach: %v", err)
	}
}

func TestKeybindOpenSessionTmux(t *testing.T) {
	socketPath, env, serverCleanup := startServerReturnEnv(t)
	defer serverCleanup()

	// Use tmux style which has $ for open-session
	writeTestKeybindConfig(t, env, `style = "tmux"`)

	fe := startFrontendWithEnv(t, socketPath, env)
	defer fe.Kill()

	fe.WaitFor(t, "nxterm$", 10*time.Second)

	// ctrl+b $ (open-session in tmux) should open the connect overlay.
	fe.Write([]byte{0x02, '$'})
	fe.WaitFor(t, "type a server address", 5*time.Second)

	// Cancel and verify we're back.
	fe.Write([]byte{0x1b})
	fe.WaitFor(t, "nxterm$", 5*time.Second)
}
