package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyLocations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	content := `# top-level comment
listen = ["tcp:127.0.0.1:9090", "unix:/tmp/nxtermd.sock"]
debug  = true
pprof  = "localhost:6060"  # inline comment

[ssh]
host-key       = "/etc/ssh/host_key"
authorized-keys = "/home/u/.ssh/authorized_keys"
no-auth        = false

[sessions]
default-name = "main"

[[programs]]
name = "shell"
cmd  = "bash"

[[programs]]
name = "editor"
cmd  = "nvim"
args = ["-u", "init.lua"]

[discovery]
enabled = true
name    = "dev box"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := KeyLocations(path)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]int{
		"listen":             2,
		"debug":              3,
		"pprof":              4,
		"ssh":                6,
		"ssh.host-key":       7,
		"ssh.authorized-keys": 8,
		"ssh.no-auth":        9,
		"sessions":           11,
		"sessions.default-name": 12,
		"programs[0]":        14,
		"programs[0].name":   15,
		"programs[0].cmd":    16,
		"programs[1]":        18,
		"programs[1].name":   19,
		"programs[1].cmd":    20,
		"programs[1].args":   21,
		"discovery":          23,
		"discovery.enabled":  24,
		"discovery.name":     25,
	}

	for k, line := range want {
		src, ok := got[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if src.Line != line {
			t.Errorf("key %q: line=%d, want %d", k, src.Line, line)
		}
		if src.File != path {
			t.Errorf("key %q: file=%q, want %q", k, src.File, path)
		}
		if src.Kind != SourceFile {
			t.Errorf("key %q: kind=%q, want %q", k, src.Kind, SourceFile)
		}
	}
}

func TestKeyLocationsHashInString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	content := `name = "has # in it"
other = "plain"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := KeyLocations(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["name"].Line != 1 {
		t.Errorf("name: line=%d, want 1", got["name"].Line)
	}
	if got["other"].Line != 2 {
		t.Errorf("other: line=%d, want 2", got["other"].Line)
	}
}

func TestKeyLocationsQuotedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kb.toml")
	content := `[main]
"ctrl+a" = "next-tab"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := KeyLocations(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["main.ctrl+a"].Line != 2 {
		t.Errorf("main.ctrl+a: line=%d, want 2", got["main.ctrl+a"].Line)
	}
}
