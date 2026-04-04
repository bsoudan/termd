package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
		{"alt+f1", []byte("\x1b[1;3P")},
		{"alt+f5", []byte("\x1b[15;3~")},
		{"alt+f9", []byte("\x1b[20;3~")},
		{"ctrl+f1", []byte("\x1b[1;5P")},
		{"ctrl+f5", []byte("\x1b[15;5~")},
		{"ctrl+,", []byte("\x1b[44;5u")},
		{"ctrl+.", []byte("\x1b[46;5u")},
		{"d", nil},
		{"ctrl+b", nil},   // ctrl+letter is a prefix key, not always-binding
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

func TestFuzzyScore(t *testing.T) {
	tests := []struct {
		query     string
		candidate string
		wantMatch bool
	}{
		{"det", "detach", true},
		{"dt", "detach", true},
		{"ot", "open-tab", true},
		{"xyz", "detach", false},
		{"", "anything", true},
		{"tab", "open-tab", true},
		{"st", "show-status", true},
	}
	for _, tt := range tests {
		_, ok := fuzzyScore(tt.query, tt.candidate)
		if ok != tt.wantMatch {
			t.Errorf("fuzzyScore(%q, %q) matched=%v, want %v", tt.query, tt.candidate, ok, tt.wantMatch)
		}
	}
}

func TestCommandPaletteEntries(t *testing.T) {
	r := NewRegistry("native", "", nil)
	entries := r.PaletteEntries()
	if len(entries) == 0 {
		t.Fatal("palette entries should not be empty")
	}
	// Should include run-command itself.
	found := false
	for _, e := range entries {
		if e.Command.Name == "run-command" {
			found = true
			break
		}
	}
	if !found {
		t.Error("palette entries should include run-command")
	}
	// Should include switch-tab variants with args.
	foundSwitchTab := false
	for _, e := range entries {
		if e.Command.Name == "switch-tab" && e.Args != "" {
			foundSwitchTab = true
			break
		}
	}
	if !foundSwitchTab {
		t.Error("palette entries should include switch-tab with args")
	}
}

func TestCommandPaletteView(t *testing.T) {
	r := NewRegistry("native", "", nil)
	// Test at multiple terminal widths to stress the layout.
	for _, tw := range []int{80, 60, 40} {
		p := NewCommandPaletteLayer(r)
		layers := p.View(tw, 24, false)
		if len(layers) == 0 {
			t.Fatalf("palette returned no layers at width=%d", tw)
		}
	}
}

func TestCommandPaletteLineWidths(t *testing.T) {
	r := NewRegistry("native", "", nil)
	p := NewCommandPaletteLayer(r)

	// Call buildContent directly to test line widths pre-border.
	// View uses overlayW minus the horizontal frame (border + padding).
	overlayW := 80 * 2 / 3
	contentW := overlayW - paletteStyle.GetHorizontalFrameSize()
	content := p.buildContent(contentW, 24)
	lines := strings.Split(content, "\n")

	t.Logf("contentW=%d, %d content lines", contentW, len(lines))
	for i, line := range lines {
		w := displayWidth(line)
		t.Logf("  line %d: width=%d %q", i, w, stripAnsi(line))
		if w > contentW {
			t.Errorf("line %d display width %d > contentW %d", i, w, contentW)
		}
	}

	// Should have a separator line of microdots.
	foundSep := false
	for _, line := range lines {
		stripped := stripAnsi(line)
		if len(stripped) > 0 && allDots(stripped) {
			foundSep = true
			break
		}
	}
	if !foundSep {
		t.Error("no microdot separator line found between input and suggestions")
	}

	// Should have at least one suggestion line with a microdot column separator.
	foundSuggestion := false
	for _, line := range lines {
		stripped := stripAnsi(line)
		if strings.Contains(stripped, " · ") && !allDots(stripped) {
			foundSuggestion = true
			break
		}
	}
	if !foundSuggestion {
		t.Error("no suggestion line with ' · ' column separator found")
	}

	// Test at narrow width too.
	p2 := NewCommandPaletteLayer(r)
	narrowCW := 40 - paletteStyle.GetHorizontalFrameSize()
	content2 := p2.buildContent(narrowCW, 24)
	for i, line := range strings.Split(content2, "\n") {
		w := displayWidth(line)
		if w > narrowCW {
			t.Errorf("narrow: line %d display width %d > %d", i, w, narrowCW)
		}
	}

	// Verify lipgloss agrees on widths.
	p3 := NewCommandPaletteLayer(r)
	content3 := p3.buildContent(contentW, 24)
	for i, line := range strings.Split(content3, "\n") {
		ourW := displayWidth(line)
		lgW := lipgloss.Width(line)
		ansiW := ansi.StringWidth(line)
		if ourW != lgW || ourW != ansiW {
			t.Errorf("line %d width mismatch: ours=%d lipgloss=%d ansi=%d: %q",
				i, ourW, lgW, ansiW, stripAnsi(line))
		}
		if lgW > contentW {
			t.Errorf("line %d lipgloss width %d > contentW %d: %q",
				i, lgW, contentW, stripAnsi(line))
		}
	}

	// Verify the border doesn't add extra lines from wrapping.
	bordered := paletteStyle.Width(overlayW).Render(content3)
	borderedLines := strings.Split(bordered, "\n")
	contentLines := strings.Split(content3, "\n")
	// Border adds top + bottom = 2 lines.
	expectedLines := len(contentLines) + 2
	if len(borderedLines) != expectedLines {
		t.Errorf("bordered has %d lines, expected %d (content %d + 2 border); wrapping detected",
			len(borderedLines), expectedLines, len(contentLines))
		for i, line := range borderedLines {
			t.Logf("  bordered line %d (ansi.Width=%d): %q", i, ansi.StringWidth(line), stripAnsi(line))
		}
	}
}

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

func displayWidth(s string) int {
	return len([]rune(stripAnsi(s)))
}

func allDots(s string) bool {
	for _, r := range s {
		if r != '·' {
			return false
		}
	}
	return len(s) > 0
}

func TestIsAlwaysKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"alt+.", true},
		{"alt+f1", true},
		{"ctrl+f1", true},
		{"ctrl+,", true},
		{"ctrl+.", true},
		{"d", false},
		{"ctrl+b", false}, // ctrl+letter is prefix, not always
		{"?", false},
	}
	for _, tt := range tests {
		got := isAlwaysKey(tt.key)
		if got != tt.want {
			t.Errorf("isAlwaysKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

// lookupChord is a test helper that looks up a space-separated chord
// key sequence in the registry trie.
func lookupChord(r *Registry, key string) (*resolvedBinding, bool) {
	b, _ := r.MatchChord(strings.Fields(key))
	return b, b != nil
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
		{"S o", "open-session"},
		{"S c", "close-session"},
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
		b, ok := lookupChord(r, tt.key)
		if !ok {
			t.Errorf("native: chord %q not found", tt.key)
			continue
		}
		if b.command.Name != tt.wantCmd {
			t.Errorf("native: chord %q = %q, want %q", tt.key, b.command.Name, tt.wantCmd)
		}
	}

	if b, _ := lookupChord(r, "1"); b.args != "1" {
		t.Errorf("native: switch-tab 1 args = %q, want %q", b.args, "1")
	}

	// Check always bindings: alt+./,, alt+f1-f9, ctrl+./,, ctrl+f1-f9
	alwaysByName := make(map[string]int)
	for _, ab := range r.always {
		alwaysByName[ab.command.Name]++
	}
	for _, name := range []string{"next-tab", "prev-tab", "next-session", "prev-session"} {
		if alwaysByName[name] == 0 {
			t.Errorf("native: always-binding for %q not found", name)
		}
	}
	// alt+. and alt+, should be present
	foundAltDot, foundAltComma := false, false
	for _, ab := range r.always {
		if bytes.Equal(ab.raw, []byte{0x1b, '.'}) {
			foundAltDot = true
		}
		if bytes.Equal(ab.raw, []byte{0x1b, ','}) {
			foundAltComma = true
		}
	}
	if !foundAltDot {
		t.Error("native: alt+. always-binding not found")
	}
	if !foundAltComma {
		t.Error("native: alt+, always-binding not found")
	}
}

func TestRegistryTmux(t *testing.T) {
	r := NewRegistry("tmux", "", nil)

	if r.PrefixKey != 0x02 {
		t.Fatalf("tmux prefix = 0x%02x, want 0x02", r.PrefixKey)
	}
	if b, ok := lookupChord(r, "n"); !ok || b.command.Name != "next-tab" {
		t.Errorf("tmux: chord 'n' should be next-tab, got %v", b)
	}
	if b, ok := lookupChord(r, "p"); !ok || b.command.Name != "prev-tab" {
		t.Errorf("tmux: chord 'p' should be prev-tab, got %v", b)
	}
	if b, ok := lookupChord(r, "&"); !ok || b.command.Name != "close-tab" {
		t.Errorf("tmux: chord '&' should be close-tab, got %v", b)
	}
	if len(r.always) != 0 {
		t.Errorf("tmux: %d always bindings, want 0", len(r.always))
	}
	if b, ok := lookupChord(r, ")"); !ok || b.command.Name != "next-session" {
		t.Errorf("tmux: chord ')' should be next-session, got %v", b)
	}
	if b, ok := lookupChord(r, "("); !ok || b.command.Name != "prev-session" {
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
	if b, ok := lookupChord(r, "k"); !ok || b.command.Name != "close-tab" {
		t.Errorf("screen: chord 'k' should be close-tab, got %v", b)
	}
	if b, ok := lookupChord(r, "ctrl+a"); !ok || b.command.Name != "send-prefix" {
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

	b, ok := lookupChord(r, "x")
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
	if b, ok := lookupChord(r, "x"); ok && b.command.Name == "close-tab" {
		t.Error("unbind: chord 'x' should not be bound to close-tab")
	}
}

func TestRegistryMultipleBindings(t *testing.T) {
	overrides := map[string][]string{
		"detach": {"d", "alt+d"}, // both chord and always for same command
	}
	r := NewRegistry("native", "", overrides)

	if b, ok := lookupChord(r, "d"); !ok || b.command.Name != "detach" {
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
	if b, ok := lookupChord(r, "ctrl+a"); !ok || b.command.Name != "send-prefix" {
		t.Errorf("prefix override: send-prefix chord should be 'ctrl+a', got %v", b)
	}
	if _, ok := lookupChord(r, "ctrl+b"); ok {
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

func TestMatchChordMultiKey(t *testing.T) {
	r := NewRegistry("native", "", nil)

	// "S" is a prefix for "S o" and "S c" but not a complete match.
	b, isPrefix := r.MatchChord([]string{"S"})
	if b != nil {
		t.Error("MatchChord(['S']) should not be a complete match")
	}
	if !isPrefix {
		t.Error("MatchChord(['S']) should be a valid prefix")
	}

	// "S o" is a complete match for open-session.
	b, isPrefix = r.MatchChord([]string{"S", "o"})
	if b == nil {
		t.Fatal("MatchChord(['S','o']) should match open-session")
	}
	if b.command.Name != "open-session" {
		t.Errorf("MatchChord(['S','o']) = %q, want open-session", b.command.Name)
	}
	if isPrefix {
		t.Error("MatchChord(['S','o']) should not be a prefix")
	}

	// "S c" is a complete match for close-session.
	b, isPrefix = r.MatchChord([]string{"S", "c"})
	if b == nil {
		t.Fatal("MatchChord(['S','c']) should match close-session")
	}
	if b.command.Name != "close-session" {
		t.Errorf("MatchChord(['S','c']) = %q, want close-session", b.command.Name)
	}

	// "S z" has no match and is not a prefix.
	b, isPrefix = r.MatchChord([]string{"S", "z"})
	if b != nil || isPrefix {
		t.Error("MatchChord(['S','z']) should have no match and no prefix")
	}

	// "d" is a single-key match (detach), not a prefix.
	b, isPrefix = r.MatchChord([]string{"d"})
	if b == nil || b.command.Name != "detach" {
		t.Error("MatchChord(['d']) should match detach")
	}
	if isPrefix {
		t.Error("MatchChord(['d']) should not be a prefix")
	}

	// Unknown key has no match.
	b, isPrefix = r.MatchChord([]string{"Z"})
	if b != nil || isPrefix {
		t.Error("MatchChord(['Z']) should have no match")
	}
}

func TestRawSeqToChordKey(t *testing.T) {
	tests := []struct {
		input []byte
		want  string
	}{
		{[]byte{'d'}, "d"},
		{[]byte{'S'}, "S"},
		{[]byte{'?'}, "?"},
		{[]byte{0x02}, "ctrl+b"},
		{[]byte{0x01}, "ctrl+a"},
		{[]byte{0x1b, '[', 'A'}, ""},   // escape sequence → no match
		{[]byte{0x00}, ""},              // null → no match
		{[]byte{0x7f}, ""},              // DEL → no match
	}
	for _, tt := range tests {
		got := rawSeqToChordKey(tt.input)
		if got != tt.want {
			t.Errorf("rawSeqToChordKey(%v) = %q, want %q", tt.input, got, tt.want)
		}
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

	// Verify the table renders visible content.
	h.View(80, 40, false) // trigger SetHeight with enough room
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

	// Verify all categories are in the display entries (not just visible viewport).
	entries := r.DisplayEntries()
	catsSeen := make(map[string]int)
	for i, e := range entries {
		if e.isHeader {
			catsSeen[e.keyDisplay] = i
		}
	}
	for _, cat := range []string{"main", "session", "tab"} {
		if _, ok := catsSeen[cat]; !ok {
			t.Errorf("display entries should contain %q category header", cat)
		}
	}
	// Category order: main < session < tab.
	if catsSeen["main"] >= catsSeen["session"] || catsSeen["session"] >= catsSeen["tab"] {
		t.Errorf("category order wrong: main@%d session@%d tab@%d",
			catsSeen["main"], catsSeen["session"], catsSeen["tab"])
	}
}
