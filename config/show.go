package config

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Field is one row in a --show-config listing: a configuration value
// alongside the source it was read from.
type Field struct {
	Name   string
	Value  any
	Source Source
}

// FileStatus describes a TOML config file considered during loading.
type FileStatus struct {
	Label  string // human label, e.g. "server config"
	Path   string // resolved path; empty means none was found
	Loaded bool   // true if the file existed and was read successfully
	Note   string // optional extra detail (e.g. error message)
}

// PrintConfig writes a uniform --show-config listing to w.
//
// The output has three sections: a title, a list of considered config
// files with their status, and a table of effective fields with the
// origin of each value.
func PrintConfig(w io.Writer, title string, files []FileStatus, fields []Field) {
	fmt.Fprintf(w, "%s\n\n", title)

	fmt.Fprintln(w, "Config files:")
	if len(files) == 0 {
		fmt.Fprintln(w, "  (none)")
	} else {
		labelW := 0
		for _, f := range files {
			if len(f.Label) > labelW {
				labelW = len(f.Label)
			}
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
			line := fmt.Sprintf("  %-*s  %s", labelW, f.Label, status)
			if f.Note != "" {
				line += "  -- " + f.Note
			}
			fmt.Fprintln(w, line)
		}
	}
	fmt.Fprintln(w)

	if len(fields) == 0 {
		return
	}

	fmt.Fprintln(w, "Settings:")

	// Compute column widths.
	nameW, valueW := 0, 0
	rendered := make([]struct{ name, value, source string }, len(fields))
	for i, f := range fields {
		v := formatValue(f.Value)
		s := f.Source.String()
		rendered[i].name = f.Name
		rendered[i].value = v
		rendered[i].source = s
		if len(f.Name) > nameW {
			nameW = len(f.Name)
		}
		if len(v) > valueW {
			valueW = len(v)
		}
	}
	// Cap value column to keep things readable.
	if valueW > 60 {
		valueW = 60
	}

	for _, r := range rendered {
		v := r.value
		if len(v) > valueW {
			fmt.Fprintf(w, "  %-*s = %s\n", nameW, r.name, v)
			fmt.Fprintf(w, "  %-*s   # %s\n", nameW, "", r.source)
			continue
		}
		fmt.Fprintf(w, "  %-*s = %-*s   # %s\n", nameW, r.name, valueW, v, r.source)
	}
}

// formatValue returns a TOML-ish rendering of a config value for
// display purposes.
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		if x == "" {
			return `""`
		}
		return fmt.Sprintf("%q", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []string:
		if len(x) == 0 {
			return "[]"
		}
		quoted := make([]string, len(x))
		for i, s := range x {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	case map[string]string:
		if len(x) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s = %q", k, x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprintf("%v", v)
	}
}
