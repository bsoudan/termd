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
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-help", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-help")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Open help overlay: ctrl+b ?
	nxterm.Write([]byte{0x02, '?'}).Sync("open help overlay")

	lines := nxterm.ScreenLines()
	foundMain, foundDetach, foundSession := false, false, false
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

	nxterm.Write([]byte("q")).Sync("close help overlay")
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

	nxt.WaitFor("nxterm$", 30*time.Second)
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
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-cmdp", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-cmdp")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Open command palette: ctrl+b :
	nxterm.Write([]byte{0x02, ':'}).Sync("open command palette")
	nxterm.WaitFor("detach", 5*time.Second)

	// Type to filter.
	nxterm.Write([]byte("det")).Sync("filter 'det'")
	nxterm.WaitFor("detach", 5*time.Second)

	// Pressing enter on "detach" should detach — don't sync after
	// because detaching exits the TUI and the ack never returns.
	nxterm.Write([]byte("\r"))
}

func TestCommandPaletteEsc(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	driver := nxtest.DialDriver(t, socketPath)
	region := driver.SpawnNativeRegion("nxtest-cmde", "r1", 80, 24)

	nxterm := startFrontendForSession(t, socketPath, "nxtest-cmde")
	defer nxterm.Kill()
	region.Sync(nxterm, "TUI boot + subscribe")

	// Open command palette.
	nxterm.Write([]byte{0x02, ':'}).Sync("open palette")
	nxterm.WaitFor("detach", 5*time.Second)

	// Ctrl+G closes the palette (unambiguous single byte).
	nxterm.Write([]byte{0x07}).Sync("close palette via ctrl+g")

	// Palette is gone — typed content should reach the region.
	nxterm.Write([]byte("PALETTE_CLOSED")).Sync("echo after close")
	// Driver sees the input since no palette is intercepting.
	select {
	case data := <-region.Input():
		if !strings.Contains(string(data), "PALETTE_CLOSED") {
			t.Fatalf("expected PALETTE_CLOSED in region input, got %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for input to reach region after palette close")
	}
}
