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
//
// A SKILL.md at the walk root itself (the "one repo = one skill" layout, with
// SKILL.md at the repo top level) counts as a single skill named after the
// repo directory.
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

	// Resolve repoDir once so containment checks below compare against the
	// real path — repoDir itself may be reached through a symlink (e.g.
	// macOS's /tmp -> /private/tmp).
	repoDirReal, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		repoDirReal = repoDir
	}
	if rootReal, err := filepath.EvalSymlinks(root); err != nil || !isWithin(repoDirReal, rootReal) {
		// skills_path escaped repoDir (e.g. "../../.."). Whoever registered
		// the repo chose that path, but there's no legitimate reason for a
		// skill root to live outside the repo's own checkout.
		return nil, nil
	}

	// visitedDirs dedupes symlinked directories by their resolved real path.
	// Without this, two directories whose symlinks point at each other (or
	// at a shared third directory reached multiple ways) make the walk
	// recurse exponentially with depth — depth is bounded by the OS path
	// length limit, but the call count along the way is not, so this is a
	// real hang, not just an inefficiency.
	visitedDirs := map[string]bool{}

	var out []RepoSkill
	var walk func(dir string, parts []string)
	walk = func(dir string, parts []string) {
		// os.Stat (not Lstat) so a symlinked SKILL.md still counts — but the
		// resolved target must stay inside repoDir. Otherwise a real skill
		// directory (itself safely inside repoDir) could hold a SKILL.md
		// that's a symlink to an arbitrary local file, which `caddie export`
		// would then preserve and copy/upload verbatim.
		skillMD := filepath.Join(dir, "SKILL.md")
		if fi, err := os.Stat(skillMD); err == nil && fi.Mode().IsRegular() {
			if real, err := filepath.EvalSymlinks(skillMD); err != nil || !isWithin(repoDirReal, real) {
				return
			}
			name := strings.Join(parts, "-")
			if name == "" {
				// SKILL.md at the walk root: the repo itself is a single
				// skill, named after the repo directory.
				name = filepath.Base(repoDir)
			}
			out = append(out, RepoSkill{
				Name:    name,
				AbsPath: dir,
			})
			return
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
			// Follow symlinks-to-dirs but not regular files — and only when
			// the real target stays inside repoDir. A malicious repo could
			// otherwise ship a symlink pointing outside its own checkout
			// (into a sibling registered repo, or a sensitive path like
			// ~/.ssh) to get an unrelated directory adopted as one of its
			// "skills". Since caddie symlinks matched skills straight into
			// every project using that profile, and `caddie export` will
			// copy/upload whatever a symlink resolves to, that directory
			// would otherwise escape repoDir entirely.
			if t&os.ModeSymlink != 0 {
				fi, err := os.Stat(full)
				if err != nil || !fi.IsDir() {
					continue
				}
				real, err := filepath.EvalSymlinks(full)
				if err != nil || !isWithin(repoDirReal, real) {
					continue
				}
				if visitedDirs[real] {
					continue
				}
				visitedDirs[real] = true
				walk(full, append(parts, name))
			}
		}
	}
	walk(root, nil)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isWithin reports whether target is root itself or a descendant of root.
// Both paths must already be resolved (no symlinks) for this to be meaningful.
func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
