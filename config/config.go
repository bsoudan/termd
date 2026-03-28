// Package config reads TOML configuration files for termd tools.
//
// The server and termctl share ~/.config/termd/server.toml.
// The frontend uses ~/.config/termd-tui/config.toml, falling back to
// the server config's first listen address if no frontend config exists.
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RegionConfig describes a region to spawn for new sessions.
type RegionConfig struct {
	Cmd  string   `toml:"cmd"`
	Args []string `toml:"args"`
}

// SessionsConfig holds session-related server settings.
type SessionsConfig struct {
	DefaultName    string         `toml:"default-name"`
	DefaultRegions []RegionConfig `toml:"default-regions"`
}

// ServerConfig represents termd/server.toml.
type ServerConfig struct {
	Listen   []string       `toml:"listen"`
	Debug    bool           `toml:"debug"`
	SSH      SSHConfig      `toml:"ssh"`
	Termctl  TermctlConfig  `toml:"termctl"`
	Sessions SessionsConfig `toml:"sessions"`
}

// SSHConfig holds SSH-specific server settings.
type SSHConfig struct {
	HostKey        string `toml:"host-key"`
	AuthorizedKeys string `toml:"authorized-keys"`
	NoAuth         bool   `toml:"no-auth"`
}

// TermctlConfig holds termctl defaults from the server config.
type TermctlConfig struct {
	Connect string `toml:"connect"`
	Debug   bool   `toml:"debug"`
}

// FrontendConfig represents termd-tui/config.toml.
type FrontendConfig struct {
	Connect string `toml:"connect"`
	Command string `toml:"command"`
	Debug   bool   `toml:"debug"`
}

// LoadServerConfig reads the server configuration file.
// If explicit is non-empty, that file is used (error if missing).
// Otherwise, checks XDG paths. Returns zero config if no file found.
func LoadServerConfig(explicit string) (ServerConfig, error) {
	var cfg ServerConfig
	path := explicit
	if path == "" {
		path = findConfig("termd", "server.toml")
	}
	if path == "" {
		return cfg, nil
	}
	_, err := toml.DecodeFile(path, &cfg)
	return cfg, err
}

// LoadFrontendConfig reads the frontend configuration file.
// If explicit is non-empty, that file is used (error if missing).
// Otherwise, checks XDG paths. If no frontend config exists, falls
// back to the first listen address from the server config.
// Returns zero config if nothing found.
func LoadFrontendConfig(explicit string) (FrontendConfig, error) {
	var cfg FrontendConfig
	path := explicit
	if path == "" {
		path = findConfig("termd-tui", "config.toml")
	}
	if path != "" {
		_, err := toml.DecodeFile(path, &cfg)
		return cfg, err
	}

	// Fallback: read server config for the connect address
	serverCfg, err := LoadServerConfig("")
	if err != nil {
		return cfg, nil // ignore server config errors for fallback
	}
	if len(serverCfg.Listen) > 0 {
		cfg.Connect = serverCfg.Listen[0]
	}
	return cfg, nil
}

// findConfig returns the path to a config file if it exists, checking
// XDG_CONFIG_HOME first, then ~/.config.
func findConfig(appDir, filename string) string {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg != "" {
		// XDG_CONFIG_HOME is set — only look there.
		p := filepath.Join(xdg, appDir, filename)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	// Fall back to ~/.config
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".config", appDir, filename)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
