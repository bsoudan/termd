package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	"nxtermd/config"
)

// showTermctlConfig prints the effective nxtermctl configuration along
// with the source of each value. nxtermctl shares server.toml; only
// the [termctl] section is interpreted.
func showTermctlConfig(cmd *cli.Command) error {
	explicitPath := cmd.String("config")
	resolvedPath := config.ResolveServerConfigPath(explicitPath)

	cfg, loadErr := config.LoadServerConfig(explicitPath)

	files := []config.FileStatus{{
		Label:  "server config",
		Path:   resolvedPath,
		Loaded: resolvedPath != "" && loadErr == nil,
	}}
	if loadErr != nil {
		files[0].Note = loadErr.Error()
	}

	var keyLocs map[string]config.Source
	if resolvedPath != "" && loadErr == nil {
		if locs, err := config.KeyLocations(resolvedPath); err == nil {
			keyLocs = locs
		}
	}

	// Effective socket: --socket > [termctl] connect > built-in default.
	// We can't read the merged value from cmd.String("socket") because
	// nxtermctl's Before hook performs the merge after our --show-config
	// check; recompute it here to match.
	socketVal := cmd.String("socket")
	if !cmd.IsSet("socket") && cfg.Termctl.Connect != "" {
		socketVal = cfg.Termctl.Connect
	}
	socketSource := func() config.Source {
		if config.ArgvHasFlag("socket", []string{"s"}) {
			return config.Source{Kind: config.SourceFlag, Origin: "--socket"}
		}
		if _, ok := os.LookupEnv("NXTERMD_SOCKET"); ok {
			return config.Source{Kind: config.SourceEnv, Origin: "NXTERMD_SOCKET"}
		}
		if cfg.Termctl.Connect != "" {
			if loc, ok := keyLocs["termctl.connect"]; ok {
				return loc
			}
			return config.Source{Kind: config.SourceFile, File: resolvedPath}
		}
		return config.Source{Kind: config.SourceDefault}
	}()

	debugVal := cmd.Bool("debug") || cfg.Termctl.Debug
	debugSource := func() config.Source {
		if cmd.IsSet("debug") {
			return config.ResolveSetFlag("debug", []string{"d"}, "NXTERMD_DEBUG")
		}
		return config.FileOrDefault("termctl.debug", keyLocs)
	}()

	fields := []config.Field{
		{Name: "socket", Value: socketVal, Source: socketSource},
		{Name: "debug", Value: debugVal, Source: debugSource},
	}

	title := fmt.Sprintf("nxtermctl %s configuration", version)
	config.PrintConfig(os.Stdout, title, files, fields)
	return nil
}
