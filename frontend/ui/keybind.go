package ui

import (
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

// Registry holds all commands and resolved bindings.
type Registry struct {
	commands     []*Command
	byName       map[string]*Command
	chords       map[string]resolvedBinding
	always       []alwaysBinding
	displayOrder []displayEntry

	PrefixKey byte
	PrefixStr string
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

// Dispatch looks up a chord key and returns the command's tea.Cmd, or nil.
func (r *Registry) Dispatch(key string) tea.Cmd {
	if b, ok := r.chords[key]; ok {
		return cmdForBinding(b.command, b.args)
	}
	return nil
}

// DisplayEntries returns the help display items including category headers.
func (r *Registry) DisplayEntries() []displayEntry {
	return r.displayOrder
}

// --- Command definitions ---

// categories defines the display order of categories in the help overlay
// and the TOML config sections.
var categories = []string{"main", "session", "tab"}

func allCommands() []*Command {
	return []*Command{
		// Main — user category "main", mixed handlers
		{Name: "detach", Category: "main", Layer: "main", Description: "detach"},
		{Name: "send-prefix", Category: "main", Layer: "session", Description: "send literal prefix key"},
		{Name: "show-help", Category: "main", Layer: "session", Description: "show keybindings"},
		{Name: "show-log", Category: "main", Layer: "session", Description: "open log viewer"},
		{Name: "show-status", Category: "main", Layer: "session", Description: "show status"},
		{Name: "show-release-notes", Category: "main", Layer: "session", Description: "show release notes"},
		{Name: "enter-scrollback", Category: "main", Layer: "session", Description: "enter scrollback mode"},
		{Name: "refresh-screen", Category: "main", Layer: "session", Description: "refresh screen"},
		// Session — user category "session", handled by MainLayer
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
			{"S", "open-session", ""},
			{"X", "close-session", ""},
			{"w", "switch-session", ""},
			{"d", "detach", ""},
			{"ctrl+b", "send-prefix", ""},
			{"l", "show-log", ""},
			{"?", "show-help", ""},
			{"s", "show-status", ""},
			{"n", "show-release-notes", ""},
			{"[", "enter-scrollback", ""},
			{"r", "refresh-screen", ""},
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

func keyToRawBytes(key string) []byte {
	if !strings.HasPrefix(key, "alt+") {
		return nil
	}
	ch := key[len("alt+"):]
	if len(ch) != 1 {
		return nil
	}
	return []byte{0x1b, ch[0]}
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

func isAlwaysKey(key string) bool {
	return strings.HasPrefix(key, "alt+")
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

	chords := make(map[string]resolvedBinding)
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
			chords[b.key] = resolvedBinding{command: cmd, args: b.args, key: b.key}
		}
		catBindings[cmd.Category] = append(catBindings[cmd.Category], catBinding{b: b, cmd: cmd})
	}

	var display []displayEntry
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
				keyDisp = prefixStr + " " + cb.b.key
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
		}
	}

	return &Registry{
		commands:     cmds,
		byName:       byName,
		chords:       chords,
		always:       always,
		displayOrder: display,
		PrefixKey:    prefixByte,
		PrefixStr:    prefixStr,
	}
}
