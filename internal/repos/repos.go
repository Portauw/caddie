// Package repos parses the `repos:` section of sources.yaml. Mirrors the
// bash parse_repos python parser line-for-line — do not swap in a real YAML
// parser until the rest of the repo command family is ported and covered.
package repos

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Portauw/ai-env/internal/config"
	"github.com/Portauw/ai-env/internal/sources"
)

// Entry mirrors the tuple emitted by bash parse_repos:
//
//	name|url|skills_path|prefix
//
// skills_path defaults to "skills" if unset. prefix may be "", "false",
// "true", or a literal prefix string — callers must interpret it.
type Entry struct {
	Name       string
	URL        string
	SkillsPath string
	Prefix     string
}

// File returns the sources.yaml path.
func File() string { return filepath.Join(config.Dir(), "sources.yaml") }

// Dir returns the repos checkout directory ($CONFIG_DIR/repos).
func Dir() string { return filepath.Join(config.Dir(), "repos") }

// Parse reads sources.yaml and returns one Entry per `  - name: ...` block
// under `repos:`. Missing `skills_path` defaults to "skills" (matching
// parse_repos' `current.get('skills_path','skills')`).
func Parse() ([]Entry, error) {
	path := File()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Entry
	var cur *Entry
	inRepos := false

	flush := func() {
		if cur != nil && cur.Name != "" {
			if cur.SkillsPath == "" {
				cur.SkillsPath = "skills"
			}
			out = append(out, *cur)
		}
		cur = nil
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t")
		if strings.HasPrefix(line, "repos:") {
			inRepos = true
			continue
		}
		if !inRepos {
			continue
		}
		if strings.HasPrefix(line, "  - ") {
			flush()
			cur = &Entry{}
			rest := strings.TrimSpace(line[4:])
			if strings.HasPrefix(rest, "name:") {
				cur.Name = sources.StripQuotes(strings.TrimSpace(rest[len("name:"):]))
			}
			continue
		}
		if strings.HasPrefix(line, "    ") && strings.Contains(line, ":") {
			if cur == nil {
				continue
			}
			kv := strings.SplitN(strings.TrimSpace(line), ":", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.TrimSpace(kv[0])
			v := sources.StripQuotes(strings.TrimSpace(kv[1]))
			switch k {
			case "name":
				cur.Name = v
			case "url":
				cur.URL = v
			case "skills_path":
				cur.SkillsPath = v
			case "prefix":
				cur.Prefix = v
			}
			continue
		}
		// Section terminator: non-indented non-comment non-empty line.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(line) > 0 && line[0] != ' ' {
			break
		}
	}
	flush()
	return out, nil
}

// HasReposSection reports whether sources.yaml contains a `^repos:` line.
// Mirrors bash `grep -q "^repos:" "$SOURCES_FILE"`.
func HasReposSection() (bool, error) {
	f, err := os.Open(File())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "repos:") {
			return true, nil
		}
	}
	return false, nil
}

// Exists mirrors the bash cmd_repo_add / cmd_repo_remove python duplicate
// check: scans for any line under `repos:` containing `name:` and the given
// substring. Preserves the bash substring-match quirk for parity.
func Exists(name string) (bool, error) {
	f, err := os.Open(File())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inRepos := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "repos:") {
			inRepos = true
			continue
		}
		if inRepos && strings.Contains(line, "name:") && strings.Contains(line, name) {
			return true, nil
		}
	}
	return false, nil
}

// Append adds a new repo entry to sources.yaml. Creates the file with
// `sources:` header if missing, and appends a `repos:` header section if
// absent. Byte-compatible with the bash HEREDOC.
func Append(e Entry) error {
	path := File()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("sources:\n"), 0o644); err != nil {
			return err
		}
	}
	hasRepos, err := HasReposSection()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if !hasRepos {
		if _, err := f.WriteString("\nrepos:\n"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(f, "  - name: \"%s\"\n    url: \"%s\"\n    skills_path: \"%s\"\n", e.Name, e.URL, e.SkillsPath)
	return err
}

// Remove deletes the block for `name` under `repos:` from sources.yaml.
// Mirrors the bash cmd_repo_remove python algorithm line-for-line.
func Remove(name string) error {
	path := File()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	skip := false
	inRepos := false
	for _, line := range lines {
		if strings.HasPrefix(line, "repos:") {
			inRepos = true
			out = append(out, line)
			continue
		}
		if inRepos && strings.Contains(line, "  - name:") && strings.Contains(line, name) {
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

// GitOutput runs `git -C dir <args...>` and returns trimmed stdout, or ""
// on error. Mirrors bash `$(git -C "$dir" ... 2>/dev/null)`.
func GitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RepoName returns the effective prefix used in the inventory repo_map:
// if prefix is empty/"false"/"true", falls back to the repo name; otherwise
// uses the literal prefix value.
func (e Entry) RepoName() string {
	if e.Prefix == "" || e.Prefix == "false" || e.Prefix == "true" {
		return e.Name
	}
	return e.Prefix
}

