// Package sources implements the `source` subcommand family: list, add,
// remove. It manipulates $CONFIG_DIR/sources.yaml, which is a shared file
// with the `repos:` section (not yet ported). Writes here must stay byte-
// compatible with the bash implementation until `repo` is also on Go.
package sources

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Portauw/ai-env/internal/config"
)

// Entry is a parsed source record from sources.yaml.
type Entry struct {
	Name        string
	Marketplace string
	Plugin      string
}

func file() string { return filepath.Join(config.Dir(), "sources.yaml") }

// Parse walks the sources.yaml file and returns every `  - name: ...` block
// under `sources:`. Matches the bash cmd_source_list python parser exactly:
// a new item starts at `  - `, continuation lines are indented 4+ spaces,
// the section ends at the first fully non-indented non-comment line.
func Parse() ([]Entry, error) {
	path := file()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	var cur *Entry
	inSources := false

	flush := func() {
		if cur != nil {
			entries = append(entries, *cur)
			cur = nil
		}
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t")
		if strings.HasPrefix(line, "sources:") {
			inSources = true
			continue
		}
		if !inSources {
			continue
		}
		if strings.HasPrefix(line, "  - ") {
			flush()
			cur = &Entry{}
			rest := strings.TrimSpace(line[4:])
			if strings.HasPrefix(rest, "name:") {
				cur.Name = stripQuotes(strings.TrimSpace(rest[len("name:"):]))
			}
			continue
		}
		if strings.HasPrefix(line, "    ") {
			if cur == nil {
				continue
			}
			kv := strings.SplitN(strings.TrimSpace(line), ":", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.TrimSpace(kv[0])
			v := stripQuotes(strings.TrimSpace(kv[1]))
			switch k {
			case "name":
				cur.Name = v
			case "marketplace":
				cur.Marketplace = v
			case "plugin":
				cur.Plugin = v
			}
			continue
		}
		// Section terminator: non-indented line that isn't a comment or blank.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			break
		}
	}
	flush()
	return entries, nil
}

func stripQuotes(v string) string {
	if len(v) >= 2 {
		c := v[0]
		if (c == '"' || c == '\'') && v[len(v)-1] == c {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// ExistsStrict mirrors cmd_source_add's duplicate check:
//
//	grep -q "name: ${name}" sources.yaml
//
// This does NOT match quoted values like `name: "foo"` — a bash bug we
// preserve for contract compatibility.
func ExistsStrict(name string) (bool, error) {
	return grepExists("name: " + name)
}

// ExistsLoose mirrors cmd_source_remove's existence check:
//
//	grep -q "name: .*${name}" sources.yaml
//
// Matches any line where `name:` is followed by the target substring —
// including quoted values.
func ExistsLoose(name string) (bool, error) {
	f, err := os.Open(file())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, "name:")
		if idx < 0 {
			continue
		}
		if strings.Contains(line[idx+len("name:"):], name) {
			return true, nil
		}
	}
	return false, nil
}

func grepExists(needle string) (bool, error) {
	f, err := os.Open(file())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.Contains(sc.Text(), needle) {
			return true, nil
		}
	}
	return false, nil
}

// Append adds a new source entry to sources.yaml, creating the file (with the
// "sources:" header) if missing. Output format is hand-crafted to match the
// bash HEREDOC byte-for-byte.
func Append(e Entry) error {
	path := file()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("sources:\n"), 0o644); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "  - name: \"%s\"\n    marketplace: \"%s\"\n    plugin: \"%s\"\n", e.Name, e.Marketplace, e.Plugin)
	return err
}

// Remove deletes the block for `name` from sources.yaml. Mirrors the bash
// cmd_source_remove python algorithm line-for-line, including its substring
// match on the name (present for bug-parity).
func Remove(name string) error {
	path := file()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	skip := false
	for _, line := range lines {
		if strings.Contains(line, "  - name:") && strings.Contains(line, name) {
			skip = true
			continue
		}
		if skip {
			if strings.HasPrefix(line, "  - ") ||
				(line != "" && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "  - ")) {
				skip = false
			}
		}
		if skip && strings.HasPrefix(line, "    ") {
			continue
		}
		out = append(out, line)
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}

// PluginCacheDir returns $HOME/.claude/plugins/cache.
func PluginCacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "plugins", "cache")
}

// AvailableMarketplaces returns the basenames of directories under the
// plugin cache, sorted. Used in the "cache not found" error message.
func AvailableMarketplaces() string {
	entries, err := os.ReadDir(PluginCacheDir())
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}
