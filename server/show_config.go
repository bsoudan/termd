package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	"nxtermd/config"
)

// showServerConfig prints the effective nxtermd configuration along
// with the source of each value. It is invoked when --show-config is
// passed at the command line.
func showServerConfig(cmd *cli.Command) error {
	explicitPath := cmd.String("config")
	resolvedPath := config.ResolveServerConfigPath(explicitPath)

	cfg, loadErr := config.LoadServerConfig(explicitPath)

	files := []config.FileStatus{{
		Label:  "server config",
		Path:   resolvedPath,
		Loaded: loadErr == nil && resolvedPath != "",
	}}
	if loadErr != nil {
		files[0].Note = loadErr.Error()
	}

	var keyLocs map[string]config.Source
	if resolvedPath != "" && loadErr == nil {
		locs, err := config.KeyLocations(resolvedPath)
		if err == nil {
			keyLocs = locs
		}
	}

	specs, _ := listenSpecs(cmd, cfg)

	fields := []config.Field{
		{Name: "listen", Value: specs, Source: listenSource(cmd, keyLocs)},
		{Name: "debug", Value: cmd.Bool("debug") || cfg.Debug, Source: config.ScalarSource(cmd, "debug", []string{"d"}, "NXTERMD_DEBUG", "debug", keyLocs)},
		{Name: "pprof", Value: stringWithFallback(cmd.String("pprof"), cfg.Pprof), Source: config.ScalarSource(cmd, "pprof", nil, "", "pprof", keyLocs)},
		{Name: "ssh.host-key", Value: stringWithFallback(cmd.String("ssh-host-key"), cfg.SSH.HostKey), Source: config.ScalarSource(cmd, "ssh-host-key", nil, "", "ssh.host-key", keyLocs)},
		{Name: "ssh.authorized-keys", Value: stringWithFallback(cmd.String("ssh-auth-keys"), cfg.SSH.AuthorizedKeys), Source: config.ScalarSource(cmd, "ssh-auth-keys", nil, "", "ssh.authorized-keys", keyLocs)},
		{Name: "ssh.no-auth", Value: cmd.Bool("ssh-no-auth") || cfg.SSH.NoAuth, Source: config.ScalarSource(cmd, "ssh-no-auth", nil, "", "ssh.no-auth", keyLocs)},
		{Name: "sessions.default-name", Value: cfg.Sessions.DefaultName, Source: config.FileOrDefault("sessions.default-name", keyLocs)},
		{Name: "sessions.default-programs", Value: cfg.Sessions.DefaultPrograms, Source: config.FileOrDefault("sessions.default-programs", keyLocs)},
		{Name: "discovery.enabled", Value: cfg.Discovery.IsEnabled(), Source: config.FileOrDefault("discovery.enabled", keyLocs)},
		{Name: "discovery.name", Value: cfg.Discovery.Name, Source: config.FileOrDefault("discovery.name", keyLocs)},
		{Name: "upgrade.binaries-dir", Value: cfg.Upgrade.BinariesDir, Source: config.FileOrDefault("upgrade.binaries-dir", keyLocs)},
		{Name: "termctl.connect", Value: cfg.Termctl.Connect, Source: config.FileOrDefault("termctl.connect", keyLocs)},
		{Name: "termctl.debug", Value: cfg.Termctl.Debug, Source: config.FileOrDefault("termctl.debug", keyLocs)},
	}

	for i, p := range cfg.Programs {
		prefix := fmt.Sprintf("programs[%d]", i)
		fields = append(fields,
			config.Field{Name: prefix + ".name", Value: p.Name, Source: config.FileOrDefault(prefix+".name", keyLocs)},
			config.Field{Name: prefix + ".cmd", Value: p.Cmd, Source: config.FileOrDefault(prefix+".cmd", keyLocs)},
			config.Field{Name: prefix + ".args", Value: p.Args, Source: config.FileOrDefault(prefix+".args", keyLocs)},
		)
		if len(p.Env) > 0 {
			fields = append(fields, config.Field{
				Name:   prefix + ".env",
				Value:  p.Env,
				Source: config.FileOrDefault(prefix+".env", keyLocs),
			})
		}
	}

	title := fmt.Sprintf("nxtermd %s configuration", version)
	config.PrintConfig(os.Stdout, title, files, fields)
	return nil
}

func stringWithFallback(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// listenSource determines where the effective listen list came from:
// command-line positional args, the config file, or the built-in default.
func listenSource(cmd *cli.Command, keyLocs map[string]config.Source) config.Source {
	if cmd.NArg() > 0 {
		return config.Source{Kind: config.SourceArg, Origin: "positional"}
	}
	if loc, ok := keyLocs["listen"]; ok {
		return loc
	}
	return config.Source{Kind: config.SourceDefault}
}
