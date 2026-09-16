// SKILL.md walker for repo skill discovery.
package skills

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// escapeScans counts how many times a skill directory is actually scanned for
// escaping symlinks, i.e. cache misses. Tests assert on it so the "scan once
// per real directory, not once per alias" property is checked directly rather
// than through a wall-clock budget. Not used outside tests; walks are
// sequential, so a plain counter is enough.
var escapeScans int

// ErrSkillsPathOutsideRepo is returned when the configured skills_path
// resolves outside the repo's own checkout, so nothing under it is walked.
var ErrSkillsPathOutsideRepo = errors.New("skills_path resolves outside the repo checkout")

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
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil || !isWithin(repoDirReal, rootReal) {
		// skills_path escaped repoDir (e.g. "../../.."). Whoever registered
		// the repo chose that path, but there's no legitimate reason for a
		// skill root to live outside the repo's own checkout. Returning a
		// distinguishable error rather than (nil, nil): a rejected path used
		// to be indistinguishable from "this repo has no skills", so a repo
		// that silently stopped contributing anything gave the user no
		// message to search for.
		return nil, ErrSkillsPathOutsideRepo
	}

	// visited holds the resolved real path of every directory the walk has
	// already descended into. It is global to the walk, not scoped to the
	// current chain: a chain-scoped set only catches a directory that
	// reappears as its own ancestor, and a shared target reached along many
	// distinct root-to-dir paths is never its own ancestor — so a cycle-free
	// DAG (each directory holding two symlinks to the next) still explodes
	// 2^depth. Deduping globally makes the walk linear in the number of real
	// directories, whatever the symlink topology.
	//
	// Crucially this gates *descent* only. A directory that is itself a skill
	// is matched before the check, so two symlinks aimed directly at the same
	// skill still yield one skill each — the legitimate aliasing case, and
	// the one earlier attempts regressed.
	//
	// Indirect aliases are not preserved: with the SKILL.md at shared/thing/,
	// "alpha -> shared" and "beta -> shared" yield alpha-thing only, because
	// shared is descended once. origin/main loses this too, so it is not a
	// regression, but it is a real limit rather than a general guarantee.
	visited := map[string]bool{}

	// escapes memoizes hasEscapingSymlink per resolved real directory. The
	// SKILL.md match deliberately runs before the `visited` gate so aliases
	// each yield a skill, which means without this a repo with N aliases of
	// one M-file skill directory costs N full recursive scans of it: 2000
	// aliases over 2000 files measured at 4.1s, 5000 over 5000 at 26.7s, from
	// a few MB of empty files. `caddie scan` runs from `activate`, which runs
	// from the claude() shell function, so that is a stall a repo can impose
	// on every shell. All aliases share one real directory, so caching by it
	// collapses this back to linear.
	escapes := map[string]bool{}
	escapesOutside := func(dir, real string) bool {
		bad, seen := escapes[real]
		if !seen {
			escapeScans++
			bad = hasEscapingSymlink(dir, repoDirReal, map[string]bool{})
			escapes[real] = bad
		}
		return bad
	}

	var out []RepoSkill
	var walk func(dir, real string, parts []string)
	walk = func(dir, real string, parts []string) {
		// os.Stat (not Lstat) so a symlinked SKILL.md still counts — but the
		// resolved target must stay inside repoDir. Otherwise a real skill
		// directory (itself safely inside repoDir) could hold a SKILL.md
		// that's a symlink to an arbitrary local file, which `caddie export`
		// would then preserve and copy/upload verbatim.
		skillMD := filepath.Join(dir, "SKILL.md")
		if fi, err := os.Stat(skillMD); err == nil && fi.Mode().IsRegular() {
			// SKILL.md itself is fine, but the whole directory — every file
			// in it — gets adopted: AbsPath is symlinked wholesale into the
			// store and from there into every project on a "*"-style
			// profile. A sibling file that's a symlink escaping repoDir
			// (e.g. "reference.md" -> "../../../id_rsa") would otherwise be
			// read directly by whatever loads the skill's files, no export
			// step required.
			if r, err := filepath.EvalSymlinks(skillMD); err == nil && isWithin(repoDirReal, r) &&
				!escapesOutside(dir, real) {
				name := strings.Join(parts, "-")
				if name == "" {
					// SKILL.md at the walk root: the repo itself is a single
					// skill, named after the repo directory.
					name = filepath.Base(repoDir)
				}
				out = append(out, RepoSkill{Name: name, AbsPath: dir})
			}
			// Descent stops here either way: the children of a directory that
			// just tried to escape are no more trustworthy than it is.
			return
		}
		if visited[real] {
			return
		}
		visited[real] = true
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
			switch {
			case t.IsDir():
				// A real directory reached from an already-validated parent is
				// inside repoDir by construction, so a resolve failure (an
				// unreadable path component, a concurrent rename during a
				// `git pull`) is no reason to drop the subtree — fall back to
				// the lexical path as its identity.
				childReal, err := filepath.EvalSymlinks(full)
				if err != nil {
					childReal = full
				}
				walk(full, childReal, append(parts, name))
			case t&os.ModeSymlink != 0:
				// Follow symlinks-to-dirs but not regular files — and only
				// when the real target stays inside repoDir. A malicious repo
				// could otherwise ship a symlink pointing outside its own
				// checkout (into a sibling registered repo, or a sensitive
				// path like ~/.ssh) to get an unrelated directory adopted as
				// one of its "skills". Since caddie symlinks matched skills
				// straight into every project using that profile, and
				// `caddie export` will copy/upload whatever a symlink
				// resolves to, that directory would otherwise escape repoDir
				// entirely.
				fi, err := os.Stat(full)
				if err != nil || !fi.IsDir() {
					continue
				}
				childReal, err := filepath.EvalSymlinks(full)
				if err != nil || !isWithin(repoDirReal, childReal) {
					continue
				}
				walk(full, childReal, append(parts, name))
			}
		}
	}
	walk(root, rootReal, nil)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ContainsEscapingSymlink reports whether dir, or anything under it, contains
// a symlink resolving outside root. Exported for callers that ship a skill
// directory somewhere else (see internal/export): skills placed in the store
// by hand never go through WalkRepoSkills, so nothing else has checked them.
func ContainsEscapingSymlink(dir, root string) bool {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootReal = root
	}
	return hasEscapingSymlink(dir, rootReal, map[string]bool{})
}

// hasEscapingSymlink reports whether dir, or anything under it recursively,
// contains a symlink (to a file or a directory) whose resolved target falls
// outside repoDirReal. visited dedupes symlinked directories by resolved
// real path so a cycle within the skill directory can't hang this check the
// same way it could the outer walk.
//
// Unlike the discovery walk above, this does NOT skip hidden entries. The
// whole directory is adopted — linked into the store, and copied by export,
// dotfiles included — so a hidden escaping symlink leaks exactly as well as a
// visible one.
func hasEscapingSymlink(dir, repoDirReal string, visited map[string]bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Unreadable is treated as unsafe — the caller skips the skill.
		return true
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			fi, statErr := os.Stat(full)
			if statErr != nil {
				// Dangling links are tolerated only while they point inside
				// root. Legitimate repos carry them (an unfetched LFS
				// pointer, a sparse-checkout gap, a generated file) and those
				// are in-repo relative paths. Tolerating *any* dangling link
				// would let a repo ship "key.md -> ../../../../.ssh/id_rsa"
				// and be adopted on a machine where the target doesn't exist
				// yet — the skill is already linked into every project by the
				// time the target appears, and scan results are cached.
				if os.IsNotExist(statErr) && !danglingEscapes(full, repoDirReal) {
					continue
				}
				return true
			}
			real, err := filepath.EvalSymlinks(full)
			if err != nil || !isWithin(repoDirReal, real) {
				return true
			}
			if fi.IsDir() {
				if visited[real] {
					continue
				}
				visited[real] = true
				if hasEscapingSymlink(full, repoDirReal, visited) {
					return true
				}
			}
			continue
		}
		if e.IsDir() && hasEscapingSymlink(full, repoDirReal, visited) {
			return true
		}
	}
	return false
}

// danglingEscapes reports whether a symlink whose target does not exist points
// outside root. The target can't be resolved (nothing is there), so it is
// resolved lexically against the link's own real parent directory.
//
// Two subtleties, both of which were holes before:
//
//   - Join and Clean are purely lexical. Resolving only the link's own parent
//     left "skills/s/key.md -> ../../out/x" accepted whenever "out" was itself
//     a directory symlink out of the repo. So the longest prefix of the target
//     that actually exists is resolved, and the missing tail re-appended.
//   - The first missing component can itself be a dangling symlink, which is
//     how "key.md -> ../../shared/k" with "shared/k -> ../../id_rsa" escaped.
//     So the chain is followed, with a hop cap.
//
// The intermediate in both shapes only has to sit outside the skill directory
// and inside the repo — the one region neither the per-skill containment scan
// nor the discovery walk looks at.
func danglingEscapes(full, rootReal string) bool {
	for hops := 0; hops < 40; hops++ {
		target, err := os.Readlink(full)
		if err != nil {
			return true
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			parent, err := filepath.EvalSymlinks(filepath.Dir(full))
			if err != nil {
				return true
			}
			resolved = filepath.Join(parent, target)
		}

		existing := filepath.Clean(resolved)
		var tail []string
		for {
			real, err := filepath.EvalSymlinks(existing)
			if err == nil {
				existing = real
				break
			}
			parent := filepath.Dir(existing)
			if parent == existing {
				// Nothing on the path resolves, not even the root.
				return true
			}
			tail = append([]string{filepath.Base(existing)}, tail...)
			existing = parent
		}
		if len(tail) == 0 {
			return !isWithin(rootReal, existing)
		}
		// The first component that doesn't resolve decides it: if it's a
		// dangling symlink, follow it; anything below it inherits wherever it
		// lands, so containment of that one link is what matters.
		next := filepath.Join(existing, tail[0])
		if !isWithin(rootReal, next) {
			return true
		}
		li, err := os.Lstat(next)
		if err != nil || li.Mode()&os.ModeSymlink == 0 {
			// A plain missing name inside root — the LFS-pointer case.
			return false
		}
		full = next
	}
	// A chain this long is not a real repo layout.
	return true
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
