// Scan sync logic: sweep agent dirs, clean broken symlinks, sync repo skills
// into the canonical store. Extracted from cmd_scan so the main.go renderer
// can focus on output formatting while the fs side-effects live here.
package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Portauw/ai-env/internal/repos"
)

// RepoUpdate is a single entry for the "Repo updates available" summary.
type RepoUpdate struct {
	Name   string
	Behind string // commit count, "?" when rev-list fails
}

// Shadow records a repo skill that collided with a pre-existing real dir in
// the store — the repo version is skipped, user is warned.
type Shadow struct {
	SkillName string
	RepoName  string
}

// PerRepoSync describes one repo's scan outcome. Warning is set when the repo
// was skipped entirely; Count is the number of (new or updated) skills synced.
type PerRepoSync struct {
	Name       string
	SkillsPath string
	Count      int
	Warning    string // non-empty when the repo was skipped
}

// SweepAgentDir moves non-symlink dirs under $HOME/.agents/skills/<name>/ into
// the store when no entry exists there yet. Mirrors bash 0c.
func SweepAgentDir(home string, verbose bool, verbosef func(format string, a ...any)) int {
	agentDir := filepath.Join(home, ".agents", "skills")
	info, err := os.Stat(agentDir)
	if err != nil || !info.IsDir() {
		return 0
	}
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		return 0
	}
	store := Store()
	swept := 0
	for _, e := range entries {
		full := filepath.Join(agentDir, e.Name())
		li, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if li.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !li.IsDir() {
			continue
		}
		target := filepath.Join(store, e.Name())
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := os.Rename(full, target); err != nil {
			continue
		}
		if verbose && verbosef != nil {
			verbosef("  \033[0;32m+\033[0m swept: %s -> store\n", e.Name())
		}
		swept++
	}
	return swept
}

// CleanBrokenStoreLinks removes symlinks in the store whose target doesn't
// exist. Mirrors bash 0d.
func CleanBrokenStoreLinks(verbose bool, verbosef func(format string, a ...any)) int {
	store := Store()
	entries, err := os.ReadDir(store)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		full := filepath.Join(store, e.Name())
		li, err := os.Lstat(full)
		if err != nil || li.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if _, err := os.Stat(full); err != nil {
			if os.Remove(full) == nil {
				if verbose && verbosef != nil {
					verbosef("  \033[2mremoved broken: %s\033[0m\n", e.Name())
				}
				removed++
			}
		}
	}
	return removed
}

// SyncRepos iterates every registered repo, checks for remote updates (best
// effort — offline failures are silent), and syncs skills into the store.
// Mirrors bash 1b.
func SyncRepos(verbose bool, verbosef func(format string, a ...any)) (total int, updates []RepoUpdate, shadowed []Shadow, perRepo []PerRepoSync) {
	hasRepos, err := repos.HasReposSection()
	if err != nil || !hasRepos {
		return 0, nil, nil, nil
	}
	entries, err := repos.Parse()
	if err != nil {
		return 0, nil, nil, nil
	}
	store := Store()
	for _, e := range entries {
		repoDir := filepath.Join(repos.Dir(), e.Name)
		gitDir := filepath.Join(repoDir, ".git")
		if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
			perRepo = append(perRepo, PerRepoSync{Name: e.Name, Warning: "not cloned"})
			continue
		}

		// Check for remote updates — fetch --dry-run. Any stdout/stderr implies
		// a pending change. Errors (offline) are silently ignored.
		cmd := exec.Command("git", "-C", repoDir, "fetch", "--dry-run")
		out, err := cmd.CombinedOutput()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			localHash := repos.GitOutput(repoDir, "rev-parse", "HEAD")
			_ = exec.Command("git", "-C", repoDir, "fetch", "--quiet").Run()
			defaultBranch := "main"
			if sym := repos.GitOutput(repoDir, "symbolic-ref", "refs/remotes/origin/HEAD"); sym != "" {
				defaultBranch = strings.TrimPrefix(sym, "refs/remotes/origin/")
			}
			remoteHash := repos.GitOutput(repoDir, "rev-parse", "origin/"+defaultBranch)
			if remoteHash != "" && localHash != remoteHash {
				behind := repos.GitOutput(repoDir, "rev-list", "HEAD..origin/"+defaultBranch, "--count")
				if behind == "" {
					behind = "?"
				}
				updates = append(updates, RepoUpdate{Name: e.Name, Behind: behind})
			}
		}

		repoSkillDir := filepath.Join(repoDir, e.SkillsPath)
		if info, err := os.Stat(repoSkillDir); err != nil || !info.IsDir() {
			perRepo = append(perRepo, PerRepoSync{Name: e.Name, SkillsPath: e.SkillsPath, Warning: "skills_path not found"})
			continue
		}
		skillEntries, err := os.ReadDir(repoSkillDir)
		if err != nil {
			continue
		}
		count := 0
		for _, se := range skillEntries {
			skillPath := filepath.Join(repoSkillDir, se.Name())
			info, err := os.Stat(skillPath)
			if err != nil || !info.IsDir() {
				continue
			}
			originalName := se.Name()
			skillName := originalName
			switch {
			case e.Prefix == "" || e.Prefix == "false":
				// no-op
			case e.Prefix == "true":
				if !strings.HasPrefix(skillName, e.Name+"-") {
					skillName = e.Name + "-" + skillName
				}
			default:
				if !strings.HasPrefix(skillName, e.Prefix+"-") {
					skillName = e.Prefix + "-" + skillName
				}
			}

			// Clean up stale unprefixed symlink when a prefix was applied.
			if skillName != originalName {
				stale := filepath.Join(store, originalName)
				if li, err := os.Lstat(stale); err == nil && li.Mode()&os.ModeSymlink != 0 {
					if target, err := os.Readlink(stale); err == nil {
						needle := string(os.PathSeparator) + "repos" + string(os.PathSeparator) + e.Name + string(os.PathSeparator)
						if strings.Contains(target, needle) {
							_ = os.Remove(stale)
							if verbose && verbosef != nil {
								verbosef("  \033[2mclean: removed unprefixed %s (now %s)\033[0m\n", originalName, skillName)
							}
						}
					}
				}
			}

			target := filepath.Join(store, skillName)
			li, err := os.Lstat(target)
			if err == nil {
				isSymlink := li.Mode()&os.ModeSymlink != 0
				if !isSymlink && li.IsDir() {
					// Real dir shadows the repo version.
					if verbose && verbosef != nil {
						verbosef("  \033[2mskip: %s (local override)\033[0m\n", skillName)
					}
					shadowed = append(shadowed, Shadow{SkillName: skillName, RepoName: e.Name})
					continue
				}
				if isSymlink {
					current, _ := os.Readlink(target)
					if current != skillPath {
						_ = os.Remove(target)
						if err := os.Symlink(skillPath, target); err == nil {
							if verbose && verbosef != nil {
								verbosef("  \033[2mupdate: %s -> %s\033[0m\n", skillName, skillPath)
							}
						}
					}
					count++
					continue
				}
			}
			// No existing target — create symlink.
			if err := os.Symlink(skillPath, target); err == nil {
				if verbose && verbosef != nil {
					verbosef("  \033[0;32m+\033[0m %s -> %s\n", skillName, skillPath)
				}
				count++
			}
		}
		perRepo = append(perRepo, PerRepoSync{Name: e.Name, SkillsPath: e.SkillsPath, Count: count})
		total += count
	}
	return total, updates, shadowed, perRepo
}

// CountStore returns (localCount, total) where localCount is the number of
// non-symlink dirs and total is every dir (including symlinks-to-dirs).
func CountStore() (local, total int) {
	store := Store()
	entries, err := os.ReadDir(store)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		full := filepath.Join(store, e.Name())
		li, err := os.Lstat(full)
		if err != nil {
			continue
		}
		isSymlink := li.Mode()&os.ModeSymlink != 0
		// Stat follows symlinks — matches bash's `-d "$item"`.
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		if !isSymlink {
			local++
		}
		total++
	}
	return local, total
}

