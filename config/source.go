package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// SourceKind identifies where a configuration value originated.
type SourceKind string

const (
	SourceDefault  SourceKind = "default"
	SourceFile     SourceKind = "file"
	SourceFlag     SourceKind = "flag"
	SourceEnv      SourceKind = "env"
	SourceArg      SourceKind = "arg" // positional command-line argument
	SourceInferred SourceKind = "inferred"
)

// Source describes the origin of a configuration value. The zero value
// represents an unset value with no source.
type Source struct {
	Kind   SourceKind
	File   string // populated for SourceFile and SourceInferred (when inferred from a file)
	Line   int    // 1-based line number; populated alongside File
	Origin string // contextual detail: "--debug", "NXTERMD_DEBUG", "fallback from server.toml listen[0]", etc.
}

// String returns a short, human-readable description of the source.
func (s Source) String() string {
	switch s.Kind {
	case SourceFile:
		if s.Line > 0 {
			return fmt.Sprintf("%s:%d", s.File, s.Line)
		}
		return s.File
	case SourceFlag:
		if s.Origin != "" {
			return "flag " + s.Origin
		}
		return "flag"
	case SourceEnv:
		if s.Origin != "" {
			return "env " + s.Origin
		}
		return "env"
	case SourceArg:
		if s.Origin != "" {
			return "argv " + s.Origin
		}
		return "argv"
	case SourceInferred:
		if s.File != "" && s.Line > 0 {
			return fmt.Sprintf("inferred (%s:%d)", s.File, s.Line)
		}
		if s.Origin != "" {
			return "inferred (" + s.Origin + ")"
		}
		return "inferred"
	case SourceDefault:
		return "default"
	}
	return string(s.Kind)
}

// KeyLocations parses a TOML file and returns a map of dotted-key paths
// to their source location. It is intentionally lenient: it tracks
// [section] and [[array.of.tables]] headers and `key = value` lines,
// but does not validate TOML syntax.
//
// Successive [[array.of.tables]] entries with the same name are tagged
// with an index suffix, e.g. `programs[0].name`, `programs[1].name`.
//
// Multi-line values (multiline strings, multi-line arrays, inline
// tables that span lines) are recognised at their first line only —
// the line number reported is where the key appears.
func KeyLocations(path string) (map[string]Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sources := make(map[string]Source)
	arrayCounters := make(map[string]int)
	var sectionPrefix string // e.g. "ssh." or "programs[0]." — already includes trailing dot if non-empty

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		text := stripLineComment(raw)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		// Section header.
		if strings.HasPrefix(text, "[[") && strings.HasSuffix(text, "]]") {
			key := strings.TrimSpace(text[2 : len(text)-2])
			idx := arrayCounters[key]
			arrayCounters[key] = idx + 1
			sectionPrefix = fmt.Sprintf("%s[%d].", key, idx)
			// Record the array entry header itself so callers can locate
			// the table even if no scalar keys live inside it yet.
			sources[fmt.Sprintf("%s[%d]", key, idx)] = Source{Kind: SourceFile, File: path, Line: lineNum}
			continue
		}
		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
			key := strings.TrimSpace(text[1 : len(text)-1])
			sectionPrefix = key + "."
			sources[key] = Source{Kind: SourceFile, File: path, Line: lineNum}
			continue
		}

		// key = value
		eq := strings.Index(text, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(text[:eq])
		key = strings.Trim(key, `"'`)
		if key == "" {
			continue
		}
		full := sectionPrefix + key
		if _, exists := sources[full]; !exists {
			sources[full] = Source{Kind: SourceFile, File: path, Line: lineNum}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sources, nil
}

// stripLineComment removes a trailing `# comment` from a TOML line,
// preserving `#` characters that appear inside string literals. It is
// purposely simple and only handles the cases that occur in our config
// files.
func stripLineComment(s string) string {
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			if inDouble && i+1 < len(s) {
				i++ // skip escaped char
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '#':
			if !inSingle && !inDouble {
				return s[:i]
			}
		}
	}
	return s
}
