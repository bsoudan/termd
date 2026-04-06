package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	"nxtermd/config"
)

// showFrontendConfig prints the effective nxterm configuration along
// with the source of each value.
func showFrontendConfig(cmd *cli.Command) error {
	explicitPath := cmd.String("config")
	resolvedPath := config.ResolveFrontendConfigPath(explicitPath)

	frontendCfg, frontendErr := config.LoadFrontendConfig(explicitPath)

	files := []config.FileStatus{{
		Label:  "frontend config",
		Path:   resolvedPath,
		Loaded: resolvedPath != "" && frontendErr == nil,
	}}
	if frontendErr != nil {
		files[0].Note = frontendErr.Error()
	}

	var frontendKeys map[string]config.Source
	if resolvedPath != "" && frontendErr == nil {
		if locs, err := config.KeyLocations(resolvedPath); err == nil {
			frontendKeys = locs
		}
	}

	// Keybindings file (informational only).
	keybindPath := config.ResolveKeybindConfigPath()
	files = append(files, config.FileStatus{
		Label:  "keybindings",
		Path:   keybindPath,
		Loaded: keybindPath != "",
	})

	// When the frontend file is missing, LoadFrontendConfig falls back
	// to the first listen address in server.toml. Resolve that path so
	// we can attribute the connect value correctly.
	serverPath := ""
	var serverKeys map[string]config.Source
	if resolvedPath == "" {
		serverPath = config.ResolveServerConfigPath("")
		if serverPath != "" {
			files = append(files, config.FileStatus{
				Label:  "server config (fallback)",
				Path:   serverPath,
				Loaded: true,
			})
			if locs, err := config.KeyLocations(serverPath); err == nil {
				serverKeys = locs
			}
		}
	}

	// Effective socket value: --socket > config.connect > defaultSocket.
	socketVal := cmd.String("socket")
	if !cmd.IsSet("socket") && frontendCfg.Connect != "" {
		socketVal = frontendCfg.Connect
	}

	connectSource := func() config.Source {
		if cmd.IsSet("socket") {
			return config.ResolveSetFlag("socket", []string{"s"}, "NXTERMD_SOCKET")
		}
		if frontendCfg.Connect != "" {
			if resolvedPath != "" { // came from frontend file
				if loc, ok := frontendKeys["connect"]; ok {
					return loc
				}
				return config.Source{Kind: config.SourceFile, File: resolvedPath}
			}
			// Came from server.toml fallback (LoadFrontendConfig pulled
			// listen[0] into Connect when no frontend file existed).
			if loc, ok := serverKeys["listen"]; ok {
				return config.Source{
					Kind:   config.SourceInferred,
					File:   loc.File,
					Line:   loc.Line,
					Origin: "server.toml listen[0]",
				}
			}
			return config.Source{Kind: config.SourceInferred, Origin: "server.toml listen[0]"}
		}
		return config.Source{Kind: config.SourceDefault}
	}()

	debugVal := cmd.Bool("debug") || frontendCfg.Debug

	fields := []config.Field{
		{Name: "socket", Value: socketVal, Source: connectSource},
		{Name: "session", Value: cmd.String("session"), Source: config.ScalarSource(cmd, "session", []string{"S"}, "NXTERMD_SESSION", "", frontendKeys)},
		{Name: "browse", Value: cmd.Bool("browse"), Source: flagOrDefault(cmd, "browse", []string{"b"}, "")},
		{Name: "debug", Value: debugVal, Source: config.ScalarSource(cmd, "debug", []string{"d"}, "NXTERMD_DEBUG", "debug", frontendKeys)},
		{Name: "log-stderr", Value: cmd.Bool("log-stderr"), Source: flagOrDefault(cmd, "log-stderr", nil, "")},
	}

	title := fmt.Sprintf("nxterm %s configuration", version)
	config.PrintConfig(os.Stdout, title, files, fields)
	return nil
}

// flagOrDefault is a helper for fields that have no config-file
// equivalent: if the flag is set, report its source; otherwise default.
func flagOrDefault(cmd *cli.Command, flagName string, aliases []string, envVar string) config.Source {
	if cmd.IsSet(flagName) {
		return config.ResolveSetFlag(flagName, aliases, envVar)
	}
	return config.Source{Kind: config.SourceDefault}
}
