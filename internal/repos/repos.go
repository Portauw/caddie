// Package repos parses the `repos:` section of sources.yaml. Mirrors the
// bash parse_repos python parser line-for-line — do not swap in a real YAML
// parser until the rest of the repo command family is ported and covered.
package repos

import (
	"bufio"
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

