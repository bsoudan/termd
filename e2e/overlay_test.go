package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"nxtermd/internal/nxtest"
)

func TestHelpOverlay(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Open help overlay: ctrl+b ?
	nxt.Write([]byte{0x02, '?'})

	// Wait for the help overlay to render with keybinding content.
	nxt.WaitFor("detach", 5*time.Second)

	lines := nxt.ScreenLines()
	// First category (main) and its commands should be visible.
	foundMain := false
	foundDetach := false
	foundSession := false
	for _, line := range lines {
		if strings.Contains(line, "main") {
			foundMain = true
		}
		if strings.Contains(line, "detach") {
			foundDetach = true
		}
		if strings.Contains(line, "session") {
			foundSession = true
		}
	}
	if !foundMain {
		t.Error("help overlay should show 'main' category")
	}
	if !foundDetach {
		t.Error("help overlay should show 'detach' command")
	}
	if !foundSession {
		t.Error("help overlay should show 'session' category")
	}

	// Close with q.
	nxt.Write([]byte("q"))
	nxt.WaitFor("nxterm$", 5*time.Second)
}

func TestLogViewerOverlay(t *testing.T) {
	t.Parallel()
	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	cmd := exec.Command("nxterm", "--socket", socketPath, "--debug", )
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start frontend: %v", err)
	}
	nxt := nxtest.New(t, nxtest.NewPtyIO(ptmx, 80, 24))
	frontendCleanup := func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() }
	defer frontendCleanup()

	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(500 * time.Millisecond)
	nxt.Write([]byte{0x02, 'l'})

	lines := nxt.WaitForScreen(func(lines []string) bool {
		topRow, _ := nxtest.FindOnScreen(lines, "\u256d")
		bottomRow, _ := nxtest.FindOnScreen(lines, "\u2570")
		helpRow, _ := nxtest.FindOnScreen(lines, "q/esc: close")
		return topRow >= 0 && bottomRow >= 0 && helpRow >= 0
	}, "overlay with borders and help text", 5*time.Second)

	topRow, topCol := nxtest.FindOnScreen(lines, "\u256d")
	bottomRow, bottomCol := nxtest.FindOnScreen(lines, "\u2570")
	helpRow, _ := nxtest.FindOnScreen(lines, "q/esc: close")

	t.Logf("overlay top border: row %d col %d", topRow, topCol)
	t.Logf("overlay bottom border: row %d col %d", bottomRow, bottomCol)
	t.Logf("help text: row %d", helpRow)

	// Borders should be vertically aligned (same column)
	if topCol != bottomCol {
		t.Fatalf("border columns don't align: top col=%d, bottom col=%d", topCol, bottomCol)
	}
	// Bottom should be below top
	if bottomRow <= topRow {
		t.Fatalf("bottom border (row %d) not below top border (row %d)", bottomRow, topRow)
	}
	// Both within screen bounds
	if topRow > 23 || bottomRow > 23 {
		t.Fatalf("overlay outside screen: top=%d, bottom=%d", topRow, bottomRow)
	}
	// Help text should be below or at the bottom border
	if helpRow < bottomRow {
		t.Fatalf("help text (row %d) above bottom border (row %d)", helpRow, bottomRow)
	}

	overlayHeight := bottomRow - topRow + 1
	t.Logf("overlay: %d rows", overlayHeight)

	// Close and verify overlay disappears
	nxt.Write([]byte("q"))
	nxt.WaitForScreen(func(lines []string) bool {
		row, _ := nxtest.FindOnScreen(lines, "\u256d")
		return row < 0
	}, "overlay gone", 10*time.Second)

	nxt.WaitFor("nxterm$",10*time.Second)
	nxt.Write([]byte("echo logview_closed\r"))
	nxt.WaitFor("logview_closed", 10*time.Second)
}

func TestCommandPalette(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Open command palette: ctrl+b :
	nxt.Write([]byte{0x02, ':'})

	// Should show the palette with commands listed.
	nxt.WaitFor("detach", 5*time.Second)

	// Type to filter.
	nxt.Write([]byte("det"))
	nxt.WaitFor("detach", 5*time.Second)

	// Pressing enter on "detach" should detach.
	nxt.Write([]byte("\r"))
}

func TestCommandPaletteEsc(t *testing.T) {
	t.Parallel()
	nxt := startFrontendShared(t)
	defer nxt.Kill()

	nxt.WaitFor("nxterm$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Open command palette.
	nxt.Write([]byte{0x02, ':'})
	nxt.WaitFor("detach", 5*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Ctrl+G should close the palette (unambiguous single byte,
	// unlike ESC which requires timeout-based disambiguation).
	nxt.Write([]byte{0x07})

	// Should return to normal prompt.
	nxt.WaitFor("nxterm$", 5*time.Second)

	// Verify palette is gone — type something and it should go to the shell.
	nxt.Write([]byte("echo PALETTE_CLOSED\r"))
	nxt.WaitFor("PALETTE_CLOSED", 5*time.Second)
}
