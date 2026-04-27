// SKILL.md walker for repo skill discovery. Recurses under <repoDir>/<skillsPath>
// and emits one RepoSkill per directory containing a SKILL.md file. Stops
// descending once a SKILL.md is found (so a skill that nests another skill
// won't double-count). Hidden directories (starting with ".") are skipped.
package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RepoSkill is one skill discovered in a repo, ready to be linked into the
// canonical store. Name is the path relative to skillsPath joined with "-"
// (e.g. "cloud/foo" -> "cloud-foo"); AbsPath is the absolute directory holding
// the SKILL.md file.
type RepoSkill struct {
	Name    string
	AbsPath string
}

// WalkRepoSkills walks <repoDir>/<skillsPath> looking for directories that
// contain a regular SKILL.md file. skillsPath == "" or "." means "the repo
// root". Returns a slice sorted by Name. Errors reading a single directory
// are swallowed (siblings keep walking); a missing skillsPath returns
// (nil, nil) so callers can render a friendly warning.
func WalkRepoSkills(repoDir, skillsPath string) ([]RepoSkill, error) {
	root := repoDir
	if skillsPath != "" && skillsPath != "." {
		root = filepath.Join(repoDir, skillsPath)
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	var out []RepoSkill
	var walk func(dir string, parts []string)
	walk = func(dir string, parts []string) {
		// If this dir has a SKILL.md, emit and stop descending.
		if len(parts) > 0 {
			skillFile := filepath.Join(dir, "SKILL.md")
			if fi, err := os.Stat(skillFile); err == nil && fi.Mode().IsRegular() {
				out = append(out, RepoSkill{
					Name:    strings.Join(parts, "-"),
					AbsPath: dir,
				})
				return
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			// Resolve into a directory (follow symlinks-to-dirs the same way
			// os.Stat would; keep it simple — only descend into dirs).
			full := filepath.Join(dir, name)
			fi, err := os.Stat(full)
			if err != nil || !fi.IsDir() {
				continue
			}
			walk(full, append(parts, name))
		}
	}
	walk(root, nil)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
