package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFrontendFallbackToServerConfig(t *testing.T) {
	dir := t.TempDir()

	// Create a server config with listen addresses but no frontend config
	serverDir := filepath.Join(dir, "nxtermd")
	os.MkdirAll(serverDir, 0755)
	os.WriteFile(filepath.Join(serverDir, "server.toml"), []byte(`
listen = ["tcp:127.0.0.1:9090", "unix:/tmp/nxtermd.sock"]
`), 0644)

	// Point XDG_CONFIG_HOME to our temp dir
	old := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Setenv("XDG_CONFIG_HOME", old)

	cfg, err := LoadFrontendConfig("")
	if err != nil {
		t.Fatal(err)
	}

	// Should get the first listen address from server.toml
	if cfg.Connect != "tcp:127.0.0.1:9090" {
		t.Errorf("expected connect=%q, got %q", "tcp:127.0.0.1:9090", cfg.Connect)
	}
}

func TestFrontendOwnConfigTakesPrecedence(t *testing.T) {
	dir := t.TempDir()

	// Create both server and frontend configs
	serverDir := filepath.Join(dir, "nxtermd")
	os.MkdirAll(serverDir, 0755)
	os.WriteFile(filepath.Join(serverDir, "server.toml"), []byte(`
listen = ["tcp:127.0.0.1:9090"]
`), 0644)

	frontendDir := filepath.Join(dir, "nxterm")
	os.MkdirAll(frontendDir, 0755)
	os.WriteFile(filepath.Join(frontendDir, "config.toml"), []byte(`
connect = "unix:/tmp/my-nxtermd.sock"
`), 0644)

	old := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Setenv("XDG_CONFIG_HOME", old)

	cfg, err := LoadFrontendConfig("")
	if err != nil {
		t.Fatal(err)
	}

	// Frontend config wins over server fallback
	if cfg.Connect != "unix:/tmp/my-nxtermd.sock" {
		t.Errorf("expected connect=%q, got %q", "unix:/tmp/my-nxtermd.sock", cfg.Connect)
	}
}

func TestNoConfigReturnsZero(t *testing.T) {
	dir := t.TempDir()

	old := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Setenv("XDG_CONFIG_HOME", old)

	cfg, err := LoadFrontendConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connect != "" {
		t.Errorf("expected empty connect, got %q", cfg.Connect)
	}
}
