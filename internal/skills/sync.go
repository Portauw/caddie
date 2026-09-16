// Scan sync logic: clean broken symlinks, sync repo skills into the
// canonical store.
package skills

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Portauw/caddie/internal/repos"
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
	Warning    string
}

// LogFn is an optional verbose-logger. nil means silent.
type LogFn func(format string, a ...any)

func (l LogFn) write(format string, a ...any) {
	if l != nil {
		l(format, a...)
	}
}

// CleanBrokenStoreLinks removes symlinks in the store whose target doesn't
// exist.
func CleanBrokenStoreLinks(log LogFn) int {
	removed := 0
	EachSymlink(Store(), func(name, full string) {
		if _, err := os.Stat(full); err != nil {
			if os.Remove(full) == nil {
				log.write("  \033[2mremoved broken: %s\033[0m\n", name)
				removed++
			}
		}
	})
	return removed
}

// SyncRepos iterates every registered repo, checks for remote updates (best
// effort — offline failures are silent), and syncs skills into the store.
// When skipFetch is true, the per-repo `git fetch --dry-run` probe is omitted
// — set this when caller already pulled the repos.
func SyncRepos(log LogFn, skipFetch bool) (total int, updates []RepoUpdate, shadowed []Shadow, perRepo []PerRepoSync) {
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
		repoDir, err := repos.CheckoutDir(e.Name)
		if err != nil {
			perRepo = append(perRepo, PerRepoSync{Name: e.Name, Warning: "unusable repo name"})
			continue
		}
		gitDir := filepath.Join(repoDir, ".git")
		if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
			perRepo = append(perRepo, PerRepoSync{Name: e.Name, Warning: "not cloned"})
			continue
		}

		if !skipFetch {
			if u, ok := checkRemoteAhead(e.Name, repoDir); ok {
				updates = append(updates, u)
			}
		}

		if e.SkillsPath != "" && e.SkillsPath != "." {
			repoSkillDir := filepath.Join(repoDir, e.SkillsPath)
			if info, err := os.Stat(repoSkillDir); err != nil || !info.IsDir() {
				perRepo = append(perRepo, PerRepoSync{Name: e.Name, SkillsPath: e.SkillsPath, Warning: "skills_path not found"})
				continue
			}
		}
		walked, err := WalkRepoSkills(repoDir, e.SkillsPath)
		if err != nil {
			// Previously a bare `continue`: any failure here, including a
			// skills_path pointing outside the checkout, left the repo
			// reporting nothing at all with no warning anywhere.
			warning := "walk failed: " + err.Error()
			if errors.Is(err, ErrSkillsPathOutsideRepo) {
				warning = "skills_path outside repo"
			}
			perRepo = append(perRepo, PerRepoSync{Name: e.Name, SkillsPath: e.SkillsPath, Warning: warning})
			continue
		}
		count := syncRepoSkills(e, walked, store, log, &shadowed)
		perRepo = append(perRepo, PerRepoSync{Name: e.Name, SkillsPath: e.SkillsPath, Count: count})
		total += count
	}
	return total, updates, shadowed, perRepo
}

// checkRemoteAhead probes the remote with `git fetch --dry-run`; on a hit it
// runs a real fetch and compares hashes against origin/<default-branch>. The
// caller can skip this entirely when it just pulled the repo.
func checkRemoteAhead(name, repoDir string) (RepoUpdate, bool) {
	if !hasRemoteUpdates(repoDir) {
		return RepoUpdate{}, false
	}
	localHash := repos.GitOutput(repoDir, "rev-parse", "HEAD")
	runGit(repoDir, repos.GitFetchTimeout, "fetch", "--quiet")
	defaultBranch := "main"
	if sym := repos.GitOutput(repoDir, "symbolic-ref", "refs/remotes/origin/HEAD"); sym != "" {
		defaultBranch = strings.TrimPrefix(sym, "refs/remotes/origin/")
	}
	remoteHash := repos.GitOutput(repoDir, "rev-parse", "origin/"+defaultBranch)
	if remoteHash == "" || localHash == remoteHash {
		return RepoUpdate{}, false
	}
	behind := cmp.Or(repos.GitOutput(repoDir, "rev-list", "HEAD..origin/"+defaultBranch, "--count"), "?")
	return RepoUpdate{Name: name, Behind: behind}, true
}

func hasRemoteUpdates(repoDir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), repos.GitFetchTimeout)
	defer cancel()
	out, err := repos.GitCommand(ctx, "-C", repoDir, "fetch", "--dry-run").CombinedOutput()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

func runGit(dir string, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return repos.GitCommand(ctx, append([]string{"-C", dir}, args...)...).Run()
}

func prefixSkillName(e repos.Entry, name string) string {
	if !e.UsesPrefix() {
		return name
	}
	prefix := e.EffectivePrefix()
	if strings.HasPrefix(name, prefix+"-") {
		return name
	}
	return prefix + "-" + name
}

func syncRepoSkills(e repos.Entry, walked []RepoSkill, store string, log LogFn, shadowed *[]Shadow) int {
	count := 0
	for _, rs := range walked {
		skillName := prefixSkillName(e, rs.Name)

		// Clean up stale unprefixed symlink when a prefix was applied.
		if skillName != rs.Name {
			cleanStaleUnprefixedLink(store, e.Name, rs.Name, skillName, log)
		}

		target := filepath.Join(store, skillName)
		li, err := os.Lstat(target)
		if err == nil {
			isSymlink := li.Mode()&os.ModeSymlink != 0
			if !isSymlink && li.IsDir() {
				log.write("  \033[2mskip: %s (local override)\033[0m\n", skillName)
				*shadowed = append(*shadowed, Shadow{SkillName: skillName, RepoName: e.Name})
				continue
			}
			if isSymlink {
				current, _ := os.Readlink(target)
				if current != rs.AbsPath {
					_ = os.Remove(target)
					if err := os.Symlink(rs.AbsPath, target); err == nil {
						log.write("  \033[2mupdate: %s -> %s\033[0m\n", skillName, rs.AbsPath)
					}
				}
				count++
				continue
			}
		}
		if err := os.Symlink(rs.AbsPath, target); err == nil {
			log.write("  \033[0;32m+\033[0m %s -> %s\n", skillName, rs.AbsPath)
			count++
		}
	}
	return count
}

func cleanStaleUnprefixedLink(store, repoName, originalName, newName string, log LogFn) {
	stale := filepath.Join(store, originalName)
	li, err := os.Lstat(stale)
	if err != nil || li.Mode()&os.ModeSymlink == 0 {
		return
	}
	target, err := os.Readlink(stale)
	if err != nil || !repos.LinkPointsTo(target, repoName) {
		return
	}
	if os.Remove(stale) == nil {
		log.write("  \033[2mclean: removed unprefixed %s (now %s)\033[0m\n", originalName, newName)
	}
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
