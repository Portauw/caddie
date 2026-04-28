// SKILL.md walker for repo skill discovery.
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
// are swallowed; a missing skillsPath returns (nil, nil).
//
// Descent stops at the first SKILL.md found in a subtree, so a skill that
// nests another skill won't double-count. Hidden directories are skipped.
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
		if len(parts) > 0 {
			// os.Stat (not Lstat) so a symlinked SKILL.md still counts.
			if fi, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && fi.Mode().IsRegular() {
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
			full := filepath.Join(dir, name)
			t := e.Type()
			if t.IsDir() {
				walk(full, append(parts, name))
				continue
			}
			// Follow symlinks-to-dirs but not regular files.
			if t&os.ModeSymlink != 0 {
				if fi, err := os.Stat(full); err == nil && fi.IsDir() {
					walk(full, append(parts, name))
				}
			}
		}
	}
	walk(root, nil)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
