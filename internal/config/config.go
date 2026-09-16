// Package config reads caddie configuration files.
package config

import (
	"bufio"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
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

// YAMLQuote renders s as a double-quoted YAML scalar, escaping backslashes,
// double quotes, and the control characters this codebase actually emits.
// Every value caddie writes into hand-rolled YAML (repo names/URLs, profile
// names/descriptions, skill patterns) must go through this — otherwise a
// value containing a literal `"` breaks out of its quotes and lets
// attacker-controlled input inject arbitrary extra YAML keys that caddie's
// own line-oriented parser (StripQuotes, ReadScalar, ReadList) will read
// back as real fields.
func YAMLQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	// Ranging over the string would replace every invalid byte with U+FFFD,
	// so a value that isn't valid UTF-8 — argv makes no such promise — would
	// round-trip to a different string, and a repo name would stop matching
	// its own checkout directory. Walk bytes, and only decode where a rune is
	// actually needed.
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				fmt.Fprintf(&b, `\x%02x`, s[i])
				continue
			}
			b.WriteString(s[i : i+size])
			i += size - 1
			continue
		}
		switch r := rune(s[i]); r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			// Any other C0 control (and DEL) goes out as \xNN. cmdInit's
			// readLine trims only "\n", so CRLF-piped stdin — a scripted
			// setup, or Windows/WSL — left a bare CR on the value; written
			// raw inside the quoted scalar it survived the round trip and
			// mangled every line that printed the name. The rest are escaped
			// for the same reason: this function exists so that nothing a
			// value contains can change how the file parses or renders.
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
				continue
			}
			b.WriteRune(r)
		}

	}
	b.WriteByte('"')
	return b.String()
}

// StripQuotes removes a single layer of matched surrounding ' or " quotes.
// For double-quoted values it also unescapes the backslash sequences that
// YAMLQuote produces, so quoting round-trips.
func StripQuotes(v string) string {
	if len(v) >= 2 {
		c := v[0]
		if (c == '"' || c == '\'') && v[len(v)-1] == c {
			inner := v[1 : len(v)-1]
			if c == '"' {
				return unescapeDoubleQuoted(inner)
			}
			return inner
		}
	}
	return v
}

// unescapeDoubleQuoted reverses YAMLQuote's escaping. Unrecognized escape
// sequences (e.g. a literal `\f` in a Windows-style path someone typed by
// hand) are passed through verbatim rather than mangled.
func unescapeDoubleQuoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'x':
				// \xNN — the escape YAMLQuote emits for other controls.
				if i+2 < len(s) {
					if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
						b.WriteByte(byte(n))
						i += 2
						continue
					}
				}
				b.WriteByte('\\')
				b.WriteByte(s[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
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
