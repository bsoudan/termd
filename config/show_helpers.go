package config

import (
	"os"
	"strings"
)

// FlagSet is the minimal interface needed by ScalarSource. *cli.Command
// satisfies it; tests can satisfy it with a simple stub.
type FlagSet interface {
	IsSet(name string) bool
}

// ScalarSource resolves the source of a scalar value: a CLI flag, an
// env var, the config file, or the default. Flag takes precedence
// over file, file over default.
func ScalarSource(cmd FlagSet, flagName string, aliases []string, envVar, cfgKey string, keyLocs map[string]Source) Source {
	if cmd.IsSet(flagName) {
		return ResolveSetFlag(flagName, aliases, envVar)
	}
	return FileOrDefault(cfgKey, keyLocs)
}

// FileOrDefault returns the file location for cfgKey if present in
// keyLocs, otherwise SourceDefault.
func FileOrDefault(cfgKey string, keyLocs map[string]Source) Source {
	if loc, ok := keyLocs[cfgKey]; ok {
		return loc
	}
	return Source{Kind: SourceDefault}
}

// ResolveSetFlag distinguishes a CLI-supplied flag from an env-supplied
// one by inspecting os.Args. urfave/cli's IsSet() is true for either,
// so we look at argv to disambiguate.
func ResolveSetFlag(flagName string, aliases []string, envVar string) Source {
	if ArgvHasFlag(flagName, aliases) {
		return Source{Kind: SourceFlag, Origin: "--" + flagName}
	}
	if envVar != "" {
		if _, ok := os.LookupEnv(envVar); ok {
			return Source{Kind: SourceEnv, Origin: envVar}
		}
	}
	return Source{Kind: SourceFlag, Origin: "--" + flagName}
}

// ArgvHasFlag reports whether the named flag (or one of its aliases)
// appears in os.Args, supporting both `--flag` and `--flag=value` forms.
func ArgvHasFlag(name string, aliases []string) bool {
	needles := []string{"--" + name, "-" + name}
	for _, a := range aliases {
		needles = append(needles, "--"+a, "-"+a)
	}
	for _, arg := range os.Args[1:] {
		for _, n := range needles {
			if arg == n || strings.HasPrefix(arg, n+"=") {
				return true
			}
		}
	}
	return false
}
