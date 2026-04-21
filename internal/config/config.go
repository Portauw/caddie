// Package config mirrors the subset of ai-env configuration reads needed by
// the commands ported so far. It reproduces the bash yaml_get semantics
// byte-for-byte — do not switch to a real YAML library until every YAML-reading
// command has been ported, or the contract will drift.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Dir returns the ai-env config directory: $AI_ENV_DIR or ~/.config/ai-env.
func Dir() string {
	if d := os.Getenv("AI_ENV_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-env")
}

// EnvDir returns the directory that holds per-environment YAML files.
func EnvDir() string { return filepath.Join(Dir(), "environments") }

// EnvFile returns the path to a specific environment's YAML file.
func EnvFile(name string) string { return filepath.Join(EnvDir(), name+".yaml") }

// EnvExists reports whether an environment YAML file exists.
func EnvExists(name string) bool {
	_, err := os.Stat(EnvFile(name))
	return err == nil
}

// ReadScalar reproduces bash yaml_get for a top-level key:
//  1. Find the first line starting with "key:"
//  2. Strip "key:" and any following spaces/tabs
//  3. If wrapped in matching single or double quotes, strip them
//  4. Trim trailing whitespace
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
		if len(v) >= 2 {
			c := v[0]
			if (c == '"' || c == '\'') && v[len(v)-1] == c {
				v = v[1 : len(v)-1]
			}
		}
		return strings.TrimRight(v, " \t")
	}
	return ""
}

// ListEnvs returns the sorted names of all environments (basename without .yaml).
func ListEnvs() []string {
	entries, err := os.ReadDir(EnvDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	// os.ReadDir already returns sorted entries.
	return names
}

// ReadList reproduces bash yaml_list: items under "key:" indented with
// "  - " (dash + space), stripped of surrounding quotes and trailing space.
// Section ends at the first line that starts without indentation.
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
		if len(item) >= 2 {
			c := item[0]
			if (c == '"' || c == '\'') && item[len(item)-1] == c {
				item = item[1 : len(item)-1]
			}
		}
		out = append(out, item)
	}
	return out
}

// FindProjectConfig walks from dir up to "/" looking for .ai-env.yaml.
// Returns the path if found, "" otherwise.
func FindProjectConfig(dir string) string {
	dir = expandHome(dir)
	for dir != "/" && dir != "" {
		candidate := filepath.Join(dir, ".ai-env.yaml")
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

// ResolveFromCwd mirrors bash resolve_env_from_cwd for `which`.
// Returns (name, warnings, ok). warnings are lines to emit to stdout
// (matching bash behavior) before the result — e.g. when .ai-env.yaml
// references a missing environment.
func ResolveFromCwd(cwd string) (name string, warnings []string, ok bool) {
	// Strategy 1: nearest .ai-env.yaml
	if cfg := FindProjectConfig(cwd); cfg != "" {
		if ref := ReadScalar(cfg, "environment"); ref != "" {
			if EnvExists(ref) {
				return ref, warnings, true
			}
			warnings = append(warnings, ".ai-env.yaml references unknown environment: "+ref)
		}
	}

	// Strategy 2: longest directory: match among environment YAMLs
	entries, err := os.ReadDir(EnvDir())
	if err != nil {
		return "", warnings, false
	}
	var best string
	var bestLen int
	check := strings.TrimRight(cwd, "/")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		envDir := ReadScalar(filepath.Join(EnvDir(), e.Name()), "directory")
		if envDir == "" {
			continue
		}
		envDir = strings.TrimRight(expandHome(envDir), "/")
		if check == envDir || strings.HasPrefix(check, envDir+"/") {
			if len(envDir) > bestLen {
				bestLen = len(envDir)
				best = strings.TrimSuffix(e.Name(), ".yaml")
			}
		}
	}
	if best != "" {
		return best, warnings, true
	}
	return "", warnings, false
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return home + strings.TrimPrefix(p, "~")
	}
	return p
}
