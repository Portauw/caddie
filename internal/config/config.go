// Package config reads caddie configuration files.
package config

import (
	"bufio"
	"cmp"
	"os"
	"path/filepath"
	"strings"
)

// Home returns the user's home directory, preferring $HOME (so tests and
// callers can override) and falling back to os.UserHomeDir.
func Home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// ExpandTilde replaces a leading "~" with the user's home directory. Only a
// leading tilde is expanded — `~user` is not supported.
func ExpandTilde(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	return Home() + strings.TrimPrefix(p, "~")
}

// Dir returns the caddie config directory: $CADDIE_DIR or ~/.config/caddie.
func Dir() string {
	return cmp.Or(os.Getenv("CADDIE_DIR"), filepath.Join(Home(), ".config", "caddie"))
}

// StripQuotes removes a single layer of matched surrounding ' or " quotes.
func StripQuotes(v string) string {
	if len(v) >= 2 {
		c := v[0]
		if (c == '"' || c == '\'') && v[len(v)-1] == c {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// ReadScalar returns the value of a top-level "key:" line:
//  1. Strip "key:" and any following spaces/tabs
//  2. If wrapped in matching single or double quotes, strip them
//  3. Trim trailing whitespace
func ReadScalar(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	prefix := key + ":"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimLeft(line[len(prefix):], " \t")
		return strings.TrimRight(StripQuotes(v), " \t")
	}
	return ""
}

// ReadList returns items under "key:" indented with "  - " (dash + space),
// stripped of surrounding quotes and trailing space. Section ends at the
// first line that starts without indentation.
func ReadList(path, key string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	prefix := key + ":"
	var out []string
	inSection := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// Section terminator: any line that doesn't start with space or #.
		if len(line) > 0 && line[0] != ' ' && line[0] != '#' {
			if inSection && !strings.HasPrefix(line, prefix) {
				break
			}
			if strings.HasPrefix(line, prefix) {
				inSection = true
				continue
			}
		}
		if !inSection {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		item := strings.TrimRight(strings.TrimPrefix(trimmed, "- "), " \t")
		out = append(out, StripQuotes(item))
	}
	return out
}

// FindProfile walks from dir up to "/" looking for the nearest .caddie.yaml
// profile. Returns its path, or "" if there is none.
func FindProfile(dir string) string {
	return findProfileUntil(dir, "/")
}

// findProfileUntil is FindProfile with a configurable stop directory so tests
// can confine the walk to a temp tree instead of the real filesystem root.
func findProfileUntil(dir, stop string) string {
	dir = ExpandTilde(dir)
	for dir != stop && dir != "" {
		candidate := filepath.Join(dir, ".caddie.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
