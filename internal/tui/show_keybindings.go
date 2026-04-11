package tui

import (
	"fmt"

	"nxtermd/internal/config"
)

// showKeybindings prints all resolved keybindings, with a source for
// each binding indicating whether it came from the built-in style
// preset or from an override in keybindings.toml.
func showKeybindings() error {
	kbCfg, kbErr := config.LoadKeybindConfig()
	kbPath := config.ResolveKeybindConfigPath()

	var keyLocs map[string]config.Source
	if kbPath != "" && kbErr == nil {
		if locs, err := config.KeyLocations(kbPath); err == nil {
			keyLocs = locs
		}
	}

	registry := NewRegistry(kbCfg.Style, kbCfg.Prefix, kbCfg.Overrides())

	fmt.Printf("nxterm %s keybindings\n\n", version)

	// Header info
	files := []config.FileStatus{{
		Label:  "keybindings",
		Path:   kbPath,
		Loaded: kbPath != "" && kbErr == nil,
	}}
	if kbErr != nil {
		files[0].Note = kbErr.Error()
	}
	for _, f := range files {
		status := "not found"
		if f.Path != "" {
			if f.Loaded {
				status = f.Path
			} else {
				status = f.Path + " (not loaded)"
			}
		}
		fmt.Printf("Config file: %s\n", status)
		if f.Note != "" {
			fmt.Printf("             -- %s\n", f.Note)
		}
	}

	styleSource := "default"
	if kbCfg.Style != "" {
		if loc, ok := keyLocs["style"]; ok {
			styleSource = loc.String()
		} else {
			styleSource = "from " + kbPath
		}
	}
	prefixSource := "default (preset " + registry.Style + ")"
	if kbCfg.Prefix != "" {
		if loc, ok := keyLocs["prefix"]; ok {
			prefixSource = loc.String()
		} else {
			prefixSource = "from " + kbPath
		}
	}

	fmt.Printf("Style:       %s   # %s\n", registry.Style, styleSource)
	fmt.Printf("Prefix:      %s   # %s\n", registry.PrefixStr, prefixSource)
	fmt.Println()

	bindings := registry.Bindings()
	if len(bindings) == 0 {
		fmt.Println("(no bindings)")
		return nil
	}

	// Compute column widths.
	keyW, invW, descW := 0, 0, 0
	for _, b := range bindings {
		inv := b.CommandName
		if b.Args != "" {
			inv += " " + b.Args
		}
		if l := len(b.KeyDisplay); l > keyW {
			keyW = l
		}
		if l := len(inv); l > invW {
			invW = l
		}
		if l := len(b.Description); l > descW {
			descW = l
		}
	}

	currentCat := ""
	for _, b := range bindings {
		if b.Category != currentCat {
			if currentCat != "" {
				fmt.Println()
			}
			fmt.Printf("[%s]\n", b.Category)
			currentCat = b.Category
		}
		inv := b.CommandName
		if b.Args != "" {
			inv += " " + b.Args
		}
		src := bindingSource(b, kbPath, keyLocs, registry.Style)
		fmt.Printf("  %-*s   %-*s   %-*s   # %s\n", keyW, b.KeyDisplay, invW, inv, descW, b.Description, src)
	}
	return nil
}

// bindingSource determines whether a binding came from the user's
// keybindings.toml file or from the active style preset. The kb file
// stores overrides under [tab]/[session]/[main] sections keyed by the
// command invocation, so we look up "<category>.<invocation>".
func bindingSource(b BindingInfo, kbPath string, keyLocs map[string]config.Source, style string) string {
	invocation := b.CommandName
	if b.Args != "" {
		invocation = b.CommandName + " " + b.Args
	}
	key := b.Category + "." + invocation
	if loc, ok := keyLocs[key]; ok {
		return loc.String()
	}
	if kbPath != "" {
		// File exists but this binding is not in it; report preset.
		// (Falls through.)
	}
	return "default (preset " + style + ")"
}

