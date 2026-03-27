package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"
)

const serviceName = "termd.service"

func serviceUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create systemd user dir: %w", err)
	}
	return filepath.Join(dir, serviceName), nil
}

func generateUnit(execPath string, cmd *cli.Command) string {
	var args []string
	args = append(args, execPath)

	for _, spec := range cmd.StringSlice("listen") {
		args = append(args, "--listen", spec)
	}
	if sock := cmd.String("socket"); sock != "" {
		args = append(args, "--socket", sock)
	}
	if cmd.String("ssh-host-key") != "" {
		args = append(args, "--ssh-host-key", cmd.String("ssh-host-key"))
	}
	if cmd.String("ssh-auth-keys") != "" {
		args = append(args, "--ssh-auth-keys", cmd.String("ssh-auth-keys"))
	}
	if cmd.Bool("ssh-no-auth") {
		args = append(args, "--ssh-no-auth")
	}
	if cmd.Bool("debug") {
		args = append(args, "--debug")
	}

	execLine := strings.Join(args, " ")

	return fmt.Sprintf(`[Unit]
Description=termd %s

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
Environment=TERMD_VERSION=%s

[Install]
WantedBy=default.target
`, version, execLine, version)
}

// readUnitVersion reads the TERMD_VERSION from an installed unit file.
func readUnitVersion(unitPath string) string {
	data, err := os.ReadFile(unitPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Environment=TERMD_VERSION=") {
			return strings.TrimPrefix(line, "Environment=TERMD_VERSION=")
		}
	}
	return ""
}

func systemctl(args ...string) (string, error) {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func cmdStart(_ context.Context, cmd *cli.Command) error {
	unitPath, err := serviceUnitPath()
	if err != nil {
		return err
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find termd binary path: %w", err)
	}
	execPath, err = filepath.Abs(execPath)
	if err != nil {
		return fmt.Errorf("cannot resolve binary path: %w", err)
	}

	// Check if already installed
	installedVersion := readUnitVersion(unitPath)
	if installedVersion != "" {
		if installedVersion == version {
			// Check if actually running
			if _, err := systemctl("is-active", "--quiet", serviceName); err == nil {
				fmt.Printf("termd %s is already running\n", version)
				return nil
			}
			// Installed but not running — start it
		} else {
			return fmt.Errorf("termd %s is installed but current binary is %s; run 'termd stop' first", installedVersion, version)
		}
	}

	// Write unit file
	unit := generateUnit(execPath, cmd)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	// Reload and start
	if _, err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if out, err := systemctl("start", serviceName); err != nil {
		return fmt.Errorf("start: %s", out)
	}

	fmt.Printf("termd %s started\n", version)
	return nil
}

func cmdStop(_ context.Context, cmd *cli.Command) error {
	unitPath, err := serviceUnitPath()
	if err != nil {
		return err
	}

	// Stop if running
	systemctl("stop", serviceName)

	// Remove unit file
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	// Reload
	systemctl("daemon-reload")

	fmt.Println("termd stopped and service removed")
	return nil
}

func cmdStatus(_ context.Context, cmd *cli.Command) error {
	unitPath, err := serviceUnitPath()
	if err != nil {
		return err
	}

	installedVersion := readUnitVersion(unitPath)
	if installedVersion == "" {
		fmt.Println("termd service is not installed")
		return nil
	}

	out, _ := systemctl("status", serviceName)
	fmt.Println(out)

	if installedVersion != version {
		fmt.Printf("\nwarning: installed service is %s but current binary is %s\n", installedVersion, version)
	}

	return nil
}
