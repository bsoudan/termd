package ui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Command is a named user action. Category is for user-facing grouping
// in the help overlay and TOML config. Layer determines which layer
// handles the command ("session" → SessionLayer, "main" → MainLayer).
type Command struct {
	Name        string
	Category    string // "tab", "session", "main"
	Layer       string // "session" or "main"
	Description string
}

// BindingType distinguishes raw-byte interception from prefix+key chords.
type BindingType int

const (
	BindChord  BindingType = iota // prefix key then key (processed as tea.KeyPressMsg)
	BindAlways                    // intercepted from raw bytes (e.g., alt+key)
)

// resolvedBinding pairs a command with its arguments and display key.
type resolvedBinding struct {
	command *Command
	args    string
	key     string
}

// alwaysBinding is a raw byte pattern that triggers a command.
type alwaysBinding struct {
	raw     []byte
	command *Command
	args    string
	key     string
}

// chordNode is a trie node for matching multi-key chord sequences.
// Single-key chords like "d" are a trie of depth 1; multi-key chords
// like "S o" (press S then o) are deeper.
type chordNode struct {
	binding  *resolvedBinding
	children map[string]*chordNode
}

func (n *chordNode) insert(keys []string, b resolvedBinding) {
	cur := n
	for _, k := range keys {
		if cur.children == nil {
			cur.children = make(map[string]*chordNode)
		}
		child, ok := cur.children[k]
		if !ok {
			child = &chordNode{}
			cur.children[k] = child
		}
		cur = child
	}
	cur.binding = &b
}

func (n *chordNode) match(keys []string) (binding *resolvedBinding, isPrefix bool) {
	cur := n
	for _, k := range keys {
		if cur.children == nil {
			return nil, false
		}
		child, ok := cur.children[k]
		if !ok {
			return nil, false
		}
		cur = child
	}
	return cur.binding, len(cur.children) > 0
}

// walk calls fn for each terminal binding in the trie.
func (n *chordNode) walk(path []string, fn func(keyStr string, b *resolvedBinding)) {
	if n.binding != nil {
		fn(strings.Join(path, " "), n.binding)
	}
	for k, child := range n.children {
		child.walk(append(path, k), fn)
	}
}

// Registry holds all commands and resolved bindings.
type Registry struct {
	commands     []*Command
	byName       map[string]*Command
	chordRoot    chordNode
	always       []alwaysBinding
	displayOrder []displayEntry
	bindings     []BindingInfo

	PrefixKey byte
	PrefixStr string
	Style     string // resolved style preset name (e.g., "native")
}

// BindingInfo describes a resolved keybinding for introspection
// (e.g. --show-keybindings). Listed in display order, grouped by
// Category. Headers are not included.
type BindingInfo struct {
	Category    string // "main", "session", "tab"
	Key         string // raw key spec, e.g. "c", "alt+x", "S o"
	KeyDisplay  string // pretty-printed: "ctrl+b, c" or "alt+x"
	CommandName string
	Args        string
	Description string
	Always      bool // true for raw-byte bindings (alt+x etc.)
}

// Bindings returns all resolved bindings in display order.
func (r *Registry) Bindings() []BindingInfo {
	return r.bindings
}

type displayEntry struct {
	keyDisplay  string
	description string
	cmdFn       func() tea.Cmd // nil for display-only entries (always-bindings, headers)
	chordKey    string         // chord key for shortcut matching in help, "" for always
	isHeader    bool
}

func cmdMsg(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}

// cmdForBinding returns a tea.Cmd that dispatches a SessionCmd or MainCmd
// based on the command's Layer field.
func cmdForBinding(cmd *Command, args string) tea.Cmd {
	switch cmd.Layer {
	case "session":
		return cmdMsg(SessionCmd{Name: cmd.Name, Args: args})
	case "main":
		return cmdMsg(MainCmd{Name: cmd.Name, Args: args})
	default:
		return nil
	}
}

// MatchChord checks the chord trie for the given key sequence.
// Returns the matched binding (nil if no exact match) and whether
// the sequence is a valid prefix of a longer chord.
func (r *Registry) MatchChord(keys []string) (*resolvedBinding, bool) {
	return r.chordRoot.match(keys)
}

// Dispatch looks up a single chord key and returns the command's tea.Cmd, or nil.
func (r *Registry) Dispatch(key string) tea.Cmd {
	b, _ := r.MatchChord([]string{key})
	if b != nil {
		return cmdForBinding(b.command, b.args)
	}
	return nil
}

// DisplayEntries returns the help display items including category headers.
func (r *Registry) DisplayEntries() []displayEntry {
	return r.displayOrder
}

// PaletteEntry is one row in the command palette.
type PaletteEntry struct {
	Command    *Command
	Args       string
	KeyDisplay string // primary keybinding display, "" if unbound
}

// PaletteEntries returns one entry per command invocation with its
// first (primary) keybinding. Ordered by display order, deduplicated.
func (r *Registry) PaletteEntries() []PaletteEntry {
	seen := make(map[string]bool)
	var entries []PaletteEntry
	// Walk display order for stable ordering; take first binding per invocation.
	for _, de := range r.displayOrder {
		if de.isHeader {
			continue
		}
		// Reconstruct the invocation from the display entry.
		// Display entries don't carry command/args directly, so
		// match via the binding data in chords and always.
		// Skip duplicates (same command with different bindings).
	}

	// Use chords and always in preset order instead (bindings list is stable).
	seen = make(map[string]bool)
	entries = nil
	// Walk all bindings (chords first, then always) to get first key per invocation.
	firstKey := make(map[string]string)
	r.chordRoot.walk(nil, func(keyStr string, b *resolvedBinding) {
		inv := commandInvocation(b.command.Name, b.args)
		if _, ok := firstKey[inv]; !ok {
			firstKey[inv] = r.PrefixStr + ", " + strings.ReplaceAll(keyStr, " ", ", ")
		}
	})
	for _, ab := range r.always {
		inv := commandInvocation(ab.command.Name, ab.args)
		if _, ok := firstKey[inv]; !ok {
			firstKey[inv] = ab.key
		}
	}
	// Build entries from all commands, including arg variants from bindings.
	for _, cmd := range r.commands {
		inv := cmd.Name
		if firstKey[inv] != "" || !hasArgVariants(firstKey, cmd.Name) {
			entries = append(entries, PaletteEntry{
				Command:    cmd,
				KeyDisplay: firstKey[inv],
			})
			seen[inv] = true
		}
	}
	// Add arg variants (switch-tab 1, switch-tab 2, etc.).
	for inv, key := range firstKey {
		if seen[inv] {
			continue
		}
		name, args := parseCommandInvocation(inv)
		cmd := r.byName[name]
		if cmd == nil {
			continue
		}
		entries = append(entries, PaletteEntry{
			Command:    cmd,
			Args:       args,
			KeyDisplay: key,
		})
	}
	return entries
}

func hasArgVariants(keys map[string]string, name string) bool {
	prefix := name + " "
	for inv := range keys {
		if strings.HasPrefix(inv, prefix) {
			return true
		}
	}
	return false
}

// --- Command definitions ---

// categories defines the display order of categories in the help overlay
// and the TOML config sections.
var categories = []string{"main", "session", "tab"}

func allCommands() []*Command {
	return []*Command{
		// Main — user category "main"
		{Name: "run-command", Category: "main", Layer: "main", Description: "command palette"},
		{Name: "detach", Category: "main", Layer: "main", Description: "detach"},
		{Name: "send-prefix", Category: "main", Layer: "main", Description: "send literal prefix key"},
		{Name: "show-help", Category: "main", Layer: "main", Description: "show keybindings"},
		{Name: "show-log", Category: "main", Layer: "main", Description: "open log viewer"},
		{Name: "show-status", Category: "main", Layer: "main", Description: "show status"},
		{Name: "show-release-notes", Category: "main", Layer: "main", Description: "show release notes"},
		{Name: "enter-scrollback", Category: "main", Layer: "main", Description: "enter scrollback mode"},
		{Name: "refresh-screen", Category: "main", Layer: "main", Description: "refresh screen"},
		{Name: "upgrade", Category: "main", Layer: "main", Description: "upgrade server/client"},
		// Session — user category "session"
		{Name: "open-session", Category: "session", Layer: "main", Description: "create new session"},
		{Name: "close-session", Category: "session", Layer: "main", Description: "kill current session"},
		{Name: "next-session", Category: "session", Layer: "main", Description: "next session"},
		{Name: "prev-session", Category: "session", Layer: "main", Description: "previous session"},
		{Name: "switch-session", Category: "session", Layer: "main", Description: "switch session"},
		// Tab — user category "tab", handled by SessionLayer
		{Name: "open-tab", Category: "tab", Layer: "session", Description: "open new tab"},
		{Name: "close-tab", Category: "tab", Layer: "session", Description: "close active tab"},
		{Name: "next-tab", Category: "tab", Layer: "session", Description: "next tab"},
		{Name: "prev-tab", Category: "tab", Layer: "session", Description: "previous tab"},
		{Name: "switch-tab", Category: "tab", Layer: "session", Description: "switch to tab N"},
	}
}

// --- Style presets ---

type binding struct {
	key         string // chord: key after prefix; always: "alt+X"
	commandName string
	args        string
}

type stylePreset struct {
	prefixStr string
	bindings  []binding
}

func nativePreset() stylePreset {
	return stylePreset{
		prefixStr: "ctrl+b",
		bindings: []binding{
			{"c", "open-tab", ""},
			{"x", "close-tab", ""},
			{"alt+.", "next-tab", ""},
			{"alt+,", "prev-tab", ""},
			{"1", "switch-tab", "1"},
			{"2", "switch-tab", "2"},
			{"3", "switch-tab", "3"},
			{"4", "switch-tab", "4"},
			{"5", "switch-tab", "5"},
			{"6", "switch-tab", "6"},
			{"7", "switch-tab", "7"},
			{"8", "switch-tab", "8"},
			{"9", "switch-tab", "9"},
			{"ctrl+f1", "switch-tab", "1"},
			{"ctrl+f2", "switch-tab", "2"},
			{"ctrl+f3", "switch-tab", "3"},
			{"ctrl+f4", "switch-tab", "4"},
			{"ctrl+f5", "switch-tab", "5"},
			{"ctrl+f6", "switch-tab", "6"},
			{"ctrl+f7", "switch-tab", "7"},
			{"ctrl+f8", "switch-tab", "8"},
			{"ctrl+f9", "switch-tab", "9"},
			{"S o", "open-session", ""},
			{"S c", "close-session", ""},
			{"ctrl+.", "next-session", ""},
			{"ctrl+,", "prev-session", ""},
			{"w", "switch-session", ""},
			{"ctrl+f1", "switch-session", "1"},
			{"ctrl+f2", "switch-session", "2"},
			{"ctrl+f3", "switch-session", "3"},
			{"ctrl+f4", "switch-session", "4"},
			{"ctrl+f5", "switch-session", "5"},
			{"ctrl+f6", "switch-session", "6"},
			{"ctrl+f7", "switch-session", "7"},
			{"ctrl+f8", "switch-session", "8"},
			{"ctrl+f9", "switch-session", "9"},
			{"d", "detach", ""},
			{":", "run-command", ""},
			{"ctrl+b", "send-prefix", ""},
			{"l", "show-log", ""},
			{"?", "show-help", ""},
			{"s", "show-status", ""},
			{"n", "show-release-notes", ""},
			{"[", "enter-scrollback", ""},
			{"r", "refresh-screen", ""},
			{"u", "upgrade", ""},
		},
	}
}

func tmuxPreset() stylePreset {
	return stylePreset{
		prefixStr: "ctrl+b",
		bindings: []binding{
			{"c", "open-tab", ""},
			{"&", "close-tab", ""},
			{"n", "next-tab", ""},
			{"p", "prev-tab", ""},
			{"0", "switch-tab", "0"},
			{"1", "switch-tab", "1"},
			{"2", "switch-tab", "2"},
			{"3", "switch-tab", "3"},
			{"4", "switch-tab", "4"},
			{"5", "switch-tab", "5"},
			{"6", "switch-tab", "6"},
			{"7", "switch-tab", "7"},
			{"8", "switch-tab", "8"},
			{"9", "switch-tab", "9"},
			{"$", "open-session", ""},
			{"s", "switch-session", ""},
			{")", "next-session", ""},
			{"(", "prev-session", ""},
			{"d", "detach", ""},
			{"ctrl+b", "send-prefix", ""},
			{"?", "show-help", ""},
			{"[", "enter-scrollback", ""},
			{"l", "show-log", ""},
			{"r", "refresh-screen", ""},
		},
	}
}

func screenPreset() stylePreset {
	return stylePreset{
		prefixStr: "ctrl+a",
		bindings: []binding{
			{"c", "open-tab", ""},
			{"k", "close-tab", ""},
			{"n", "next-tab", ""},
			{"p", "prev-tab", ""},
			{"0", "switch-tab", "0"},
			{"1", "switch-tab", "1"},
			{"2", "switch-tab", "2"},
			{"3", "switch-tab", "3"},
			{"4", "switch-tab", "4"},
			{"5", "switch-tab", "5"},
			{"6", "switch-tab", "6"},
			{"7", "switch-tab", "7"},
			{"8", "switch-tab", "8"},
			{"9", "switch-tab", "9"},
			{"S", "open-session", ""},
			{"\"", "switch-session", ""},
			{"d", "detach", ""},
			{"ctrl+a", "send-prefix", ""},
			{"?", "show-help", ""},
			{"[", "enter-scrollback", ""},
			{"l", "show-log", ""},
		},
	}
}

func zellijPreset() stylePreset {
	return stylePreset{
		prefixStr: "ctrl+b",
		bindings: []binding{
			{"alt+n", "open-tab", ""},
			{"alt+x", "close-tab", ""},
			{"alt+,", "prev-tab", ""},
			{"alt+.", "next-tab", ""},
			{"alt+1", "switch-tab", "1"},
			{"alt+2", "switch-tab", "2"},
			{"alt+3", "switch-tab", "3"},
			{"alt+4", "switch-tab", "4"},
			{"alt+5", "switch-tab", "5"},
			{"alt+6", "switch-tab", "6"},
			{"alt+7", "switch-tab", "7"},
			{"alt+8", "switch-tab", "8"},
			{"alt+9", "switch-tab", "9"},
			{"S", "open-session", ""},
			{"X", "close-session", ""},
			{"w", "switch-session", ""},
			{"alt+d", "detach", ""},
			{"alt+h", "show-help", ""},
			{"alt+e", "enter-scrollback", ""},
			{"ctrl+b", "send-prefix", ""},
			{"s", "show-status", ""},
			{"l", "show-log", ""},
			{"r", "refresh-screen", ""},
		},
	}
}

func getPreset(style string) stylePreset {
	switch style {
	case "tmux":
		return tmuxPreset()
	case "screen":
		return screenPreset()
	case "zellij":
		return zellijPreset()
	default:
		return nativePreset()
	}
}

// --- Key parsing ---

// Function key sequences (xterm format).
// F1-F4 end with P/Q/R/S; F5-F12 use ~-terminated CSI with codes.
var fnKeyCodes = map[string]struct {
	code string // CSI parameter number
	end  byte   // terminator: P/Q/R/S for F1-4, ~ for F5-12
}{
	"f1": {"1", 'P'}, "f2": {"1", 'Q'}, "f3": {"1", 'R'}, "f4": {"1", 'S'},
	"f5": {"15", '~'}, "f6": {"17", '~'}, "f7": {"18", '~'}, "f8": {"19", '~'},
	"f9": {"20", '~'}, "f10": {"21", '~'}, "f11": {"23", '~'}, "f12": {"24", '~'},
}

// Modifier codes for xterm-style CSI sequences.
const (
	modAlt  = "3" // xterm modifier for Alt
	modCtrl = "5" // xterm modifier for Ctrl
)

// keyToRawBytes converts a key spec to raw terminal bytes for
// always-active binding detection. Supports:
//
//	alt+x      → ESC x
//	alt+f1     → CSI 1;3P (xterm Alt+F1)
//	ctrl+f1    → CSI 1;5P (xterm Ctrl+F1)
//	ctrl+,     → CSI 44;5u (kitty keyboard protocol)
//	ctrl+.     → CSI 46;5u (kitty keyboard protocol)
func keyToRawBytes(key string) []byte {
	if strings.HasPrefix(key, "alt+") {
		rest := key[len("alt+"):]
		// alt+fN → xterm function key with alt modifier
		if fk, ok := fnKeyCodes[rest]; ok {
			return []byte(fmt.Sprintf("\x1b[%s;%s%c", fk.code, modAlt, fk.end))
		}
		// alt+x → ESC x (single character)
		if len(rest) == 1 {
			return []byte{0x1b, rest[0]}
		}
		return nil
	}
	if strings.HasPrefix(key, "ctrl+") {
		rest := key[len("ctrl+"):]
		// ctrl+fN → xterm function key with ctrl modifier
		if fk, ok := fnKeyCodes[rest]; ok {
			return []byte(fmt.Sprintf("\x1b[%s;%s%c", fk.code, modCtrl, fk.end))
		}
		// ctrl+punctuation → kitty keyboard protocol: CSI codepoint;5u
		if len(rest) == 1 {
			c := rest[0]
			if c >= '!' && c <= '~' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
				return []byte(fmt.Sprintf("\x1b[%d;5u", c))
			}
		}
		return nil
	}
	return nil
}

func prefixKeyToByte(key string) byte {
	if !strings.HasPrefix(key, "ctrl+") {
		return 0x02
	}
	ch := key[len("ctrl+"):]
	if len(ch) != 1 {
		return 0x02
	}
	c := ch[0]
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 1
	}
	if c >= 'A' && c <= 'Z' {
		return c - 'A' + 1
	}
	return 0x02
}

// isAlwaysKey returns true if the key spec is an always-active binding
// (intercepted from raw bytes, no prefix key needed).
func isAlwaysKey(key string) bool {
	if strings.HasPrefix(key, "alt+") {
		return true
	}
	// ctrl+fN and ctrl+punctuation are always-bindings
	if strings.HasPrefix(key, "ctrl+") {
		rest := key[len("ctrl+"):]
		if _, ok := fnKeyCodes[rest]; ok {
			return true
		}
		if len(rest) == 1 {
			c := rest[0]
			return c >= '!' && c <= '~' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z')
		}
	}
	return false
}

func parseCommandInvocation(s string) (name, args string) {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func commandInvocation(name, args string) string {
	if args == "" {
		return name
	}
	return name + " " + args
}

// --- Registry builder ---

// NewRegistry builds a Registry from a style preset and optional overrides.
func NewRegistry(style, prefix string, overrides map[string][]string) *Registry {
	resolvedStyle := style
	if resolvedStyle == "" {
		resolvedStyle = "native"
	}
	preset := getPreset(style)

	prefixStr := preset.prefixStr
	if prefix != "" {
		prefixStr = prefix
	}
	prefixByte := prefixKeyToByte(prefixStr)

	cmds := allCommands()
	byName := make(map[string]*Command, len(cmds))
	for _, c := range cmds {
		byName[c.Name] = c
	}

	bindings := make([]binding, len(preset.bindings))
	copy(bindings, preset.bindings)

	for i := range bindings {
		if bindings[i].commandName == "send-prefix" {
			bindings[i].key = prefixStr
		}
	}

	if len(overrides) > 0 {
		overriddenCmds := make(map[string]bool, len(overrides))
		for invocation := range overrides {
			overriddenCmds[invocation] = true
		}
		filtered := bindings[:0]
		for _, b := range bindings {
			inv := commandInvocation(b.commandName, b.args)
			if !overriddenCmds[inv] {
				filtered = append(filtered, b)
			}
		}
		bindings = filtered
		for invocation, keys := range overrides {
			cmdName, args := parseCommandInvocation(invocation)
			if _, ok := byName[cmdName]; !ok {
				slog.Warn("keybind: unknown command in override", "command", cmdName)
				continue
			}
			for _, key := range keys {
				bindings = append(bindings, binding{key: key, commandName: cmdName, args: args})
			}
		}
	}

	var chordRoot chordNode
	var always []alwaysBinding

	type catBinding struct {
		b   binding
		cmd *Command
	}
	catBindings := make(map[string][]catBinding)
	for _, b := range bindings {
		cmd := byName[b.commandName]
		if cmd == nil {
			continue
		}
		if isAlwaysKey(b.key) {
			raw := keyToRawBytes(b.key)
			if raw == nil {
				slog.Warn("keybind: cannot parse always-key", "key", b.key)
				continue
			}
			always = append(always, alwaysBinding{raw: raw, command: cmd, args: b.args, key: b.key})
		} else {
			keys := strings.Fields(b.key)
			chordRoot.insert(keys, resolvedBinding{command: cmd, args: b.args, key: b.key})
		}
		catBindings[cmd.Category] = append(catBindings[cmd.Category], catBinding{b: b, cmd: cmd})
	}

	var display []displayEntry
	var bindingInfos []BindingInfo
	for _, cat := range categories {
		entries := catBindings[cat]
		if len(entries) == 0 {
			continue
		}
		display = append(display, displayEntry{keyDisplay: cat, isHeader: true})
		for _, cb := range entries {
			desc := cb.cmd.Description
			if cb.b.args != "" {
				desc += " " + cb.b.args
			}
			keyDisp := cb.b.key
			if !isAlwaysKey(cb.b.key) {
				keyDisp = prefixStr + ", " + strings.ReplaceAll(cb.b.key, " ", ", ")
			}
			de := displayEntry{
				keyDisplay:  keyDisp,
				description: desc,
			}
			if !isAlwaysKey(cb.b.key) {
				de.cmdFn = func() tea.Cmd { return cmdForBinding(cb.cmd, cb.b.args) }
				de.chordKey = cb.b.key
			}
			display = append(display, de)
			bindingInfos = append(bindingInfos, BindingInfo{
				Category:    cat,
				Key:         cb.b.key,
				KeyDisplay:  keyDisp,
				CommandName: cb.cmd.Name,
				Args:        cb.b.args,
				Description: cb.cmd.Description,
				Always:      isAlwaysKey(cb.b.key),
			})
		}
	}

	return &Registry{
		commands:     cmds,
		byName:       byName,
		chordRoot:    chordRoot,
		always:       always,
		displayOrder: display,
		bindings:     bindingInfos,
		PrefixKey:    prefixByte,
		PrefixStr:    prefixStr,
		Style:        resolvedStyle,
	}
}
