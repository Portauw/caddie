// Package repos parses the `repos:` section of sources.yaml.
package repos

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Portauw/caddie/internal/config"
)

// Timeouts cap git operations so a hung remote can't wedge `caddie activate`.
const (
	GitReadTimeout  = 5 * time.Second
	GitFetchTimeout = 30 * time.Second
	GitCloneTimeout = 90 * time.Second
)

// gitAllowedProtocols restricts git subprocesses to these transports. Repo
// URLs come from sources.yaml, which users often populate by copy-pasting a
// `caddie repo add <name> <url>` one-liner from a README or chat message.
// Without this, a URL using git's "ext::" (or "fd::") transport helper runs
// an arbitrary shell command the moment the repo is cloned or updated —
// before any file from the "repo" is ever inspected. "file" stays allowed:
// it only reads a local git repo (a legitimate on-disk skill source, and
// what the contract tests use in place of a network fixture), it can't
// execute anything.
const gitAllowedProtocols = "GIT_ALLOW_PROTOCOL=http:https:ssh:git:file"

// GitCommand returns a `git <args...>` subprocess with the protocol
// allowlist applied. Callers that pass a repo URL as a positional argument
// should put "--" immediately before it so a URL starting with "-" can't be
// parsed as a flag.
func GitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), gitAllowedProtocols)
	return cmd
}

// Entry is one registered git repo. Prefix may be "", "false", "true", or a
// literal prefix string — callers must interpret it.
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
// under `repos:`. Missing `skills_path` defaults to "skills".
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
				cur.Name = config.StripQuotes(strings.TrimSpace(rest[len("name:"):]))
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
			v := config.StripQuotes(strings.TrimSpace(kv[1]))
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

// Exists reports whether a repo with the given name is registered.
func Exists(name string) (bool, error) {
	entries, err := Parse()
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// Append adds a new repo entry to sources.yaml, creating the file and the
// `repos:` section if needed.
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
	_, err = fmt.Fprintf(f, "  - name: %s\n    url: %s\n    skills_path: %s\n",
		config.YAMLQuote(e.Name), config.YAMLQuote(e.URL), config.YAMLQuote(e.SkillsPath))
	return err
}

// Remove deletes the block for `name` under `repos:` from sources.yaml.
// Match is exact — a name "lenny" will not match a registered "lenny-extra".
func Remove(name string) error {
	path := File()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	skip := false
	inRepos := false
	for _, line := range lines {
		if strings.HasPrefix(line, "repos:") {
			inRepos = true
			out = append(out, line)
			continue
		}
		if inRepos && isRepoNameLine(line, name) {
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

// isRepoNameLine reports whether line is a `  - name: <name>` block header
// for an exactly-matching name.
func isRepoNameLine(line, name string) bool {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, " \t"), "  - name:")
	if !ok {
		return false
	}
	return config.StripQuotes(strings.TrimSpace(rest)) == name
}

// GitOutput runs `git -C dir <args...>` with a 5s timeout and returns trimmed
// stdout, or "" on error.
func GitOutput(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), GitReadTimeout)
	defer cancel()
	cmd := GitCommand(ctx, append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// UsesPrefix reports whether the entry has a non-trivial prefix that should
// be applied to its skill names. The sentinels "" and "false" mean "no
// prefix"; "true" means "use the repo name as the prefix".
func (e Entry) UsesPrefix() bool {
	return e.Prefix != "" && e.Prefix != "false"
}

// LinkPointsTo reports whether target (a symlink target string) refers into
// the checkout for repo `name`. Used to scrub stale store links when a repo
// is removed or its prefix changes.
func LinkPointsTo(target, name string) bool {
	needle := string(os.PathSeparator) + "repos" + string(os.PathSeparator) + name + string(os.PathSeparator)
	return strings.Contains(target, needle)
}

// EffectivePrefix returns the prefix string that should appear in skill IDs:
// the literal Prefix value when it's a real string, or the repo Name when
// Prefix is "" / "false" / "true".
func (e Entry) EffectivePrefix() string {
	if !e.UsesPrefix() || e.Prefix == "true" {
		return e.Name
	}
	return e.Prefix
}

