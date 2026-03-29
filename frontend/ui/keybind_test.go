package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestKeyToRawBytes(t *testing.T) {
	tests := []struct {
		key  string
		want []byte
	}{
		{"alt+,", []byte{0x1b, ','}},
		{"alt+.", []byte{0x1b, '.'}},
		{"alt+n", []byte{0x1b, 'n'}},
		{"alt+1", []byte{0x1b, '1'}},
		{"alt+d", []byte{0x1b, 'd'}},
		{"d", nil},
		{"ctrl+b", nil},
		{"alt+", nil},
		{"alt+ab", nil},
	}
	for _, tt := range tests {
		got := keyToRawBytes(tt.key)
		if !bytes.Equal(got, tt.want) {
			t.Errorf("keyToRawBytes(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestPrefixKeyToByte(t *testing.T) {
	tests := []struct {
		key  string
		want byte
	}{
		{"ctrl+a", 0x01},
		{"ctrl+b", 0x02},
		{"ctrl+c", 0x03},
		{"ctrl+z", 0x1a},
		{"ctrl+A", 0x01},
		{"ctrl+Z", 0x1a},
		{"invalid", 0x02},
		{"ctrl+", 0x02},
	}
	for _, tt := range tests {
		got := prefixKeyToByte(tt.key)
		if got != tt.want {
			t.Errorf("prefixKeyToByte(%q) = 0x%02x, want 0x%02x", tt.key, got, tt.want)
		}
	}
}

func TestRegistryNative(t *testing.T) {
	r := NewRegistry("native", "", nil)

	if r.PrefixKey != 0x02 {
		t.Fatalf("native prefix = 0x%02x, want 0x02", r.PrefixKey)
	}
	if r.PrefixStr != "ctrl+b" {
		t.Fatalf("native prefix str = %q, want %q", r.PrefixStr, "ctrl+b")
	}

	chordTests := []struct {
		key     string
		wantCmd string
	}{
		{"d", "detach"},
		{"c", "open-tab"},
		{"x", "close-tab"},
		{"?", "show-help"},
		{"S", "open-session"},
		{"X", "close-session"},
		{"w", "switch-session"},
		{"[", "enter-scrollback"},
		{"r", "refresh-screen"},
		{"l", "show-log"},
		{"s", "show-status"},
		{"n", "show-release-notes"},
		{"1", "switch-tab"},
		{"9", "switch-tab"},
		{"ctrl+b", "send-prefix"},
	}
	for _, tt := range chordTests {
		b, ok := r.chords[tt.key]
		if !ok {
			t.Errorf("native: chord %q not found", tt.key)
			continue
		}
		if b.command.Name != tt.wantCmd {
			t.Errorf("native: chord %q = %q, want %q", tt.key, b.command.Name, tt.wantCmd)
		}
	}

	if b := r.chords["1"]; b.args != "1" {
		t.Errorf("native: switch-tab 1 args = %q, want %q", b.args, "1")
	}

	if len(r.always) != 2 {
		t.Fatalf("native: %d always bindings, want 2", len(r.always))
	}
	foundPrev, foundNext := false, false
	for _, ab := range r.always {
		switch ab.command.Name {
		case "prev-tab":
			foundPrev = true
			if !bytes.Equal(ab.raw, []byte{0x1b, ','}) {
				t.Errorf("prev-tab raw = %v, want {0x1b, ','}", ab.raw)
			}
		case "next-tab":
			foundNext = true
			if !bytes.Equal(ab.raw, []byte{0x1b, '.'}) {
				t.Errorf("next-tab raw = %v, want {0x1b, '.'}", ab.raw)
			}
		}
	}
	if !foundPrev {
		t.Error("native: prev-tab always-binding not found")
	}
	if !foundNext {
		t.Error("native: next-tab always-binding not found")
	}
}

func TestRegistryTmux(t *testing.T) {
	r := NewRegistry("tmux", "", nil)

	if r.PrefixKey != 0x02 {
		t.Fatalf("tmux prefix = 0x%02x, want 0x02", r.PrefixKey)
	}
	if b, ok := r.chords["n"]; !ok || b.command.Name != "next-tab" {
		t.Errorf("tmux: chord 'n' should be next-tab, got %v", b)
	}
	if b, ok := r.chords["p"]; !ok || b.command.Name != "prev-tab" {
		t.Errorf("tmux: chord 'p' should be prev-tab, got %v", b)
	}
	if b, ok := r.chords["&"]; !ok || b.command.Name != "close-tab" {
		t.Errorf("tmux: chord '&' should be close-tab, got %v", b)
	}
	if len(r.always) != 0 {
		t.Errorf("tmux: %d always bindings, want 0", len(r.always))
	}
	if b, ok := r.chords[")"]; !ok || b.command.Name != "next-session" {
		t.Errorf("tmux: chord ')' should be next-session, got %v", b)
	}
	if b, ok := r.chords["("]; !ok || b.command.Name != "prev-session" {
		t.Errorf("tmux: chord '(' should be prev-session, got %v", b)
	}
}

func TestRegistryScreen(t *testing.T) {
	r := NewRegistry("screen", "", nil)

	if r.PrefixKey != 0x01 {
		t.Fatalf("screen prefix = 0x%02x, want 0x01", r.PrefixKey)
	}
	if r.PrefixStr != "ctrl+a" {
		t.Fatalf("screen prefix str = %q, want %q", r.PrefixStr, "ctrl+a")
	}
	if b, ok := r.chords["k"]; !ok || b.command.Name != "close-tab" {
		t.Errorf("screen: chord 'k' should be close-tab, got %v", b)
	}
	if b, ok := r.chords["ctrl+a"]; !ok || b.command.Name != "send-prefix" {
		t.Errorf("screen: chord 'ctrl+a' should be send-prefix, got %v", b)
	}
}

func TestRegistryZellij(t *testing.T) {
	r := NewRegistry("zellij", "", nil)

	if len(r.always) < 5 {
		t.Errorf("zellij: only %d always bindings, expected >= 5", len(r.always))
	}
	alwaysCmds := make(map[string]bool)
	for _, ab := range r.always {
		alwaysCmds[ab.command.Name] = true
	}
	for _, name := range []string{"open-tab", "close-tab", "prev-tab", "next-tab", "detach"} {
		if !alwaysCmds[name] {
			t.Errorf("zellij: %q should be an always-binding", name)
		}
	}
}

func TestRegistryOverride(t *testing.T) {
	overrides := map[string][]string{
		"close-tab": {"x", "alt+x"}, // rebind close-tab to both chord x and alt+x
	}
	r := NewRegistry("native", "", overrides)

	b, ok := r.chords["x"]
	if !ok {
		t.Fatal("override: chord 'x' not found")
	}
	if b.command.Name != "close-tab" {
		t.Errorf("override: chord 'x' = %q, want %q", b.command.Name, "close-tab")
	}
	// Should also have an always binding for alt+x
	found := false
	for _, ab := range r.always {
		if ab.command.Name == "close-tab" && bytes.Equal(ab.raw, []byte{0x1b, 'x'}) {
			found = true
		}
	}
	if !found {
		t.Error("override: alt+x always-binding for close-tab not found")
	}
}

func TestRegistryUnbind(t *testing.T) {
	overrides := map[string][]string{
		"close-tab": {}, // unbind close-tab entirely
	}
	r := NewRegistry("native", "", overrides)

	// Native normally has "x" bound to close-tab — should be gone.
	if b, ok := r.chords["x"]; ok && b.command.Name == "close-tab" {
		t.Error("unbind: chord 'x' should not be bound to close-tab")
	}
}

func TestRegistryMultipleBindings(t *testing.T) {
	overrides := map[string][]string{
		"detach": {"d", "alt+d"}, // both chord and always for same command
	}
	r := NewRegistry("native", "", overrides)

	if b, ok := r.chords["d"]; !ok || b.command.Name != "detach" {
		t.Error("multi: chord 'd' should be detach")
	}
	found := false
	for _, ab := range r.always {
		if ab.command.Name == "detach" && bytes.Equal(ab.raw, []byte{0x1b, 'd'}) {
			found = true
		}
	}
	if !found {
		t.Error("multi: alt+d always-binding for detach not found")
	}
}

func TestRegistryPrefixOverride(t *testing.T) {
	r := NewRegistry("native", "ctrl+a", nil)

	if r.PrefixKey != 0x01 {
		t.Fatalf("prefix override = 0x%02x, want 0x01", r.PrefixKey)
	}
	if b, ok := r.chords["ctrl+a"]; !ok || b.command.Name != "send-prefix" {
		t.Errorf("prefix override: send-prefix chord should be 'ctrl+a', got %v", b)
	}
	if _, ok := r.chords["ctrl+b"]; ok {
		t.Error("prefix override: ctrl+b should not be bound after changing prefix to ctrl+a")
	}
}

func TestCommandArgs(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"detach", "detach", ""},
		{"switch-tab 3", "switch-tab", "3"},
		{"switch-session 1", "switch-session", "1"},
		{"show-help", "show-help", ""},
	}
	for _, tt := range tests {
		name, args := parseCommandInvocation(tt.input)
		if name != tt.wantName || args != tt.wantArgs {
			t.Errorf("parseCommandInvocation(%q) = (%q, %q), want (%q, %q)",
				tt.input, name, args, tt.wantName, tt.wantArgs)
		}
	}
}

func TestRegistryDispatch(t *testing.T) {
	r := NewRegistry("native", "", nil)

	if cmd := r.Dispatch("d"); cmd == nil {
		t.Error("Dispatch('d') should return non-nil for detach")
	}
	if cmd := r.Dispatch("Z"); cmd != nil {
		t.Error("Dispatch('Z') should return nil for unbound key")
	}
}

func TestRegistryDisplayCategories(t *testing.T) {
	r := NewRegistry("native", "", nil)

	entries := r.DisplayEntries()
	if len(entries) == 0 {
		t.Fatal("no display entries")
	}

	// Should have category headers.
	headers := 0
	for _, e := range entries {
		if e.isHeader {
			headers++
		}
	}
	if headers < 3 {
		t.Errorf("expected >= 3 category headers, got %d", headers)
	}

	// First entry should be a header.
	if !entries[0].isHeader {
		t.Errorf("first display entry should be a header, got %q", entries[0].keyDisplay)
	}
}

func TestHelpLayerView(t *testing.T) {
	r := NewRegistry("native", "", nil)
	h := NewHelpLayer(r)

	// Verify the table renders content with keybindings.
	h.View(80, 24, false) // trigger SetHeight
	content := h.table.View()
	if strings.TrimSpace(content) == "" {
		t.Fatal("help table rendered empty content")
	}
	if !strings.Contains(content, "detach") {
		t.Error("help table should contain 'detach'")
	}
	if !strings.Contains(content, "main") {
		t.Error("help table should contain 'main' category header")
	}
	if !strings.Contains(content, "session") {
		t.Error("help table should contain 'session' category header")
	}
	if !strings.Contains(content, "tab") {
		t.Error("help table should contain 'tab' category header")
	}
	// Category order: main first, then session, then tab.
	mainIdx := strings.Index(content, "main")
	sessionIdx := strings.Index(content, "session")
	tabIdx := strings.Index(content, "── tab")
	if mainIdx >= sessionIdx || sessionIdx >= tabIdx {
		t.Errorf("category order wrong: main@%d session@%d tab@%d", mainIdx, sessionIdx, tabIdx)
	}
}
