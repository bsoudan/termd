package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProgramListDefault(t *testing.T) {
	t.Parallel()
	// Start server with no programs config — should auto-create "default".
	socketPath, cleanup := startServerCustom(t, "")
	defer cleanup()

	out := runNxtermctl(t, socketPath, "program", "list")
	if !strings.Contains(out, "default") {
		t.Fatalf("program list missing 'default':\n%s", out)
	}
}

func TestProgramAddAndRemove(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	shell, _ := exec.LookPath("bash")
	if shell == "" {
		shell = "bash"
	}

	// Add a program
	out := runNxtermctl(t, socketPath, "program", "add", "--", "myshell", shell, "--norc")
	if !strings.Contains(out, "added") {
		t.Fatalf("expected 'added', got: %s", out)
	}

	// List should include it
	out = runNxtermctl(t, socketPath, "program", "list")
	if !strings.Contains(out, "myshell") {
		t.Fatalf("program list missing 'myshell' after add:\n%s", out)
	}

	// Remove it
	out = runNxtermctl(t, socketPath, "program", "remove", "myshell")
	if !strings.Contains(out, "removed") {
		t.Fatalf("expected 'removed', got: %s", out)
	}

	// List should not include it
	out = runNxtermctl(t, socketPath, "program", "list")
	if strings.Contains(out, "myshell") {
		t.Fatalf("program list still contains 'myshell' after remove:\n%s", out)
	}
}

func TestProgramSpawnByName(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	// Spawn using the configured "shell" program name
	id := spawnRegion(t, socketPath, "shell")

	out := runNxtermctl(t, socketPath, "region", "list")
	if !strings.Contains(out, id) {
		t.Fatalf("region list missing spawned region:\n%s", out)
	}
	if !strings.Contains(out, "bash") {
		t.Fatalf("region list missing 'bash' name:\n%s", out)
	}
}

func TestProgramSpawnUnknown(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	// Try to spawn a non-existent program — nxtermctl should fail
	args := []string{"--socket", socketPath, "region", "spawn", "nonexistent"}
	cmd := exec.Command("nxtermctl", args...)
	cmd.Env = testEnv(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected spawn of unknown program to fail, got: %s", out)
	}
	if !strings.Contains(string(out), "unknown program") {
		t.Fatalf("expected 'unknown program' error, got: %s", out)
	}
}

func TestProgramDefaultSession(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	// Connect frontend — the server should auto-spawn the "shell" program
	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("$", 10*time.Second)

	// Verify the region was spawned with the shell program's cmd
	out := runNxtermctl(t, socketPath, "region", "list")
	if !strings.Contains(out, "bash") {
		t.Fatalf("expected shell program to spawn bash, got:\n%s", out)
	}
}

func TestProgramPickerMultiple(t *testing.T) {
	t.Parallel()
	shell, _ := exec.LookPath("bash")
	if shell == "" {
		shell = "bash"
	}
	cfgContent := fmt.Sprintf(
		"[[programs]]\nname = \"shell\"\ncmd = %q\nargs = [\"--norc\"]\n\n[[programs]]\nname = \"shell2\"\ncmd = %q\nargs = [\"--norc\"]\n\n[sessions]\ndefault-programs = [\"shell\"]\n",
		shell, shell,
	)
	socketPath, cleanup := startServerCustom(t, cfgContent)
	defer cleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	// Wait for initial tab and let the screen settle
	nxt.WaitFor("$", 10*time.Second)
	nxt.WaitForSilence(200 * time.Millisecond)

	// Press ctrl+b c to request new tab — should show picker
	nxt.Write([]byte{0x02, 'c'})

	// Picker should show program names
	nxt.WaitFor("shell2", 5*time.Second)

	// Press enter to select the first program
	nxt.Write([]byte("\r"))

	// Should get a second tab. Tab 2 becomes active and tab 1
	// becomes inactive — tab 1's "1:shell" label appears.
	nxt.WaitForScreen(func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:")
	}, "tab 1 to go inactive after second tab spawn", 10*time.Second)
}

func TestProgramPickerSingleAutoSpawn(t *testing.T) {
	t.Parallel()
	socketPath, cleanup := startServer(t)
	defer cleanup()

	nxt := startFrontend(t, socketPath)
	defer nxt.Kill()

	nxt.WaitFor("$", 10*time.Second)

	// Press ctrl+b c — with only 1 program, should spawn immediately (no picker)
	nxt.Write([]byte{0x02, 'c'})

	// Should get a second tab without seeing a picker dialog. Tab 2
	// becomes active so tab 1 goes inactive — its "1:shell" label
	// appears in the tab bar.
	nxt.WaitForScreen(func(lines []string) bool {
		if len(lines) == 0 {
			return false
		}
		return strings.Contains(lines[0], "1:")
	}, "tab 1 to go inactive after second tab spawn", 10*time.Second)
}

func TestProgramEnvVars(t *testing.T) {
	t.Parallel()
	shell, _ := exec.LookPath("bash")
	if shell == "" {
		shell = "bash"
	}
	cfgContent := fmt.Sprintf(
		"[[programs]]\nname = \"envtest\"\ncmd = %q\nargs = [\"--norc\"]\n\n[programs.env]\nMY_TEST_VAR = \"hello_from_program\"\n\n[sessions]\ndefault-programs = [\"envtest\"]\n",
		shell,
	)
	socketPath, cleanup := startServerCustom(t, cfgContent)
	defer cleanup()

	id := spawnRegion(t, socketPath, "envtest")
	regionSendAndWait(t, socketPath, id, `echo $MY_TEST_VAR\r`, "hello_from_program")
}
