package skills

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func mkSkill(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(rs []RepoSkill) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func TestWalkRepoSkills(t *testing.T) {
	t.Run("flat", func(t *testing.T) {
		root := t.TempDir()
		mkSkill(t, filepath.Join(root, "skills", "alpha"))
		mkSkill(t, filepath.Join(root, "skills", "beta"))
		got, err := WalkRepoSkills(root, "skills")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"alpha", "beta"}
		if !reflect.DeepEqual(names(got), want) {
			t.Errorf("got %v want %v", names(got), want)
		}
	})

	t.Run("single_category", func(t *testing.T) {
		root := t.TempDir()
		mkSkill(t, filepath.Join(root, "skills", "cloud", "foo"))
		got, _ := WalkRepoSkills(root, "skills")
		if !reflect.DeepEqual(names(got), []string{"cloud-foo"}) {
			t.Errorf("got %v", names(got))
		}
		if !filepath.IsAbs(got[0].AbsPath) {
			t.Errorf("AbsPath not absolute: %s", got[0].AbsPath)
		}
	})

	t.Run("nested_categories", func(t *testing.T) {
		root := t.TempDir()
		mkSkill(t, filepath.Join(root, "skills", "team", "sub", "baz"))
		mkSkill(t, filepath.Join(root, "skills", "cloud", "foo"))
		mkSkill(t, filepath.Join(root, "skills", "flat"))
		got, _ := WalkRepoSkills(root, "skills")
		want := []string{"cloud-foo", "flat", "team-sub-baz"}
		if !reflect.DeepEqual(names(got), want) {
			t.Errorf("got %v want %v", names(got), want)
		}
	})

	t.Run("hidden_dirs_ignored", func(t *testing.T) {
		root := t.TempDir()
		mkSkill(t, filepath.Join(root, "skills", "alpha"))
		mkSkill(t, filepath.Join(root, "skills", ".hidden", "x"))
		// Also a .git inside a real skill should not interfere.
		if err := os.MkdirAll(filepath.Join(root, "skills", "alpha", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, _ := WalkRepoSkills(root, "skills")
		if !reflect.DeepEqual(names(got), []string{"alpha"}) {
			t.Errorf("got %v", names(got))
		}
	})

	t.Run("stop_descend_after_skill", func(t *testing.T) {
		root := t.TempDir()
		// Outer dir has SKILL.md AND a nested subdir with another SKILL.md;
		// only the outer one should be emitted.
		mkSkill(t, filepath.Join(root, "skills", "outer"))
		mkSkill(t, filepath.Join(root, "skills", "outer", "inner"))
		got, _ := WalkRepoSkills(root, "skills")
		if !reflect.DeepEqual(names(got), []string{"outer"}) {
			t.Errorf("got %v want [outer]", names(got))
		}
	})

	t.Run("single_skill_at_repo_root", func(t *testing.T) {
		// "one repo = one skill" layout: SKILL.md at the repo top level.
		// Named after the repo directory, for skills_path "" and ".".
		root := filepath.Join(t.TempDir(), "show-your-work")
		mkSkill(t, root)
		// Sibling dirs (assets/scripts/etc.) must not be mistaken for skills,
		// and descent must stop once the root SKILL.md is found.
		if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, sp := range []string{"", "."} {
			got, err := WalkRepoSkills(root, sp)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(names(got), []string{"show-your-work"}) {
				t.Errorf("skillsPath=%q got %v want [show-your-work]", sp, names(got))
			}
			if len(got) == 1 && got[0].AbsPath != root {
				t.Errorf("AbsPath = %s, want %s", got[0].AbsPath, root)
			}
		}
	})

	t.Run("missing_skills_path", func(t *testing.T) {
		root := t.TempDir()
		got, err := WalkRepoSkills(root, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", names(got))
		}
	})

	t.Run("follows_symlink_within_repo", func(t *testing.T) {
		root := t.TempDir()
		mkSkill(t, filepath.Join(root, "elsewhere", "real-skill"))
		if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(root, "elsewhere", "real-skill"),
			filepath.Join(root, "skills", "linked"),
		); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(root, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(names(got), []string{"linked"}) {
			t.Errorf("got %v want [linked]", names(got))
		}
	})

	t.Run("rejects_symlink_escaping_repo", func(t *testing.T) {
		base := t.TempDir()
		// A directory outside the repo checkout, with a real SKILL.md in it —
		// standing in for something sensitive elsewhere on disk (a sibling
		// registered repo, ~/.ssh, etc).
		outside := filepath.Join(base, "outside")
		mkSkill(t, outside)

		repo := filepath.Join(base, "repo")
		if err := os.MkdirAll(filepath.Join(repo, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, "skills", "escape")); err != nil {
			t.Fatal(err)
		}

		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("expected the escaping symlink to be ignored, got %v", names(got))
		}
	})

	t.Run("rejects_skill_md_symlink_escaping_repo", func(t *testing.T) {
		// The containing directory is real and inside repoDir; only SKILL.md
		// itself is a symlink pointing outside. The dir-symlink containment
		// check alone doesn't catch this — SKILL.md is read via os.Stat,
		// which follows symlinks transparently.
		base := t.TempDir()
		outside := filepath.Join(base, "outside-secret.md")
		if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		repo := filepath.Join(base, "repo")
		evilDir := filepath.Join(repo, "skills", "evil")
		if err := os.MkdirAll(evilDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(evilDir, "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("expected the escaping SKILL.md symlink to be ignored, got %v", got)
		}
	})

	t.Run("rejects_sibling_symlink_escaping_repo", func(t *testing.T) {
		// SKILL.md itself is legitimate and inside repoDir, but a sibling
		// file in the same skill directory is a symlink escaping repoDir.
		// The whole directory (AbsPath) gets adopted and symlinked wholesale
		// into the store, so validating SKILL.md alone isn't enough — every
		// file in the directory must stay contained.
		base := t.TempDir()
		secret := filepath.Join(base, "id_rsa")
		if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
			t.Fatal(err)
		}
		repo := filepath.Join(base, "repo")
		evilDir := filepath.Join(repo, "skills", "evil")
		mkSkill(t, evilDir)
		if err := os.Symlink(secret, filepath.Join(evilDir, "reference.md")); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("expected the skill dir with an escaping sibling symlink to be ignored, got %v", got)
		}
	})

	t.Run("terminates_on_branching_symlink_cycle", func(t *testing.T) {
		// Two directories link to each other with two symlinks apiece
		// (branching factor 2). Every followed symlink stays inside repoDir,
		// so containment alone doesn't stop it — without deduping by real
		// path, recursive call count grows exponentially with depth and the
		// walk never returns in practice.
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		a := filepath.Join(repo, "skills", "a")
		b := filepath.Join(repo, "skills", "b")
		if err := os.MkdirAll(a, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(b, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, l := range []string{"l1", "l2"} {
			if err := os.Symlink(b, filepath.Join(a, l)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(a, filepath.Join(b, l)); err != nil {
				t.Fatal(err)
			}
		}

		done := make(chan struct{})
		go func() {
			_, _ = WalkRepoSkills(repo, "skills")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("walk did not terminate within 3s — exponential symlink fan-out")
		}
	})

	t.Run("two_unrelated_symlinks_to_shared_target_both_adopted", func(t *testing.T) {
		// Cycle detection is scoped to the current root-to-dir chain, not
		// global to the whole walk — so two sibling symlinks pointing at the
		// same shared (non-cyclic) target are each still walked and adopted
		// as their own skill, rather than the second one being silently
		// dropped because the target's real path was already "visited".
		root := t.TempDir()
		mkSkill(t, filepath.Join(root, "shared"))
		if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "shared"), filepath.Join(root, "skills", "one")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "shared"), filepath.Join(root, "skills", "two")); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(root, "skills")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"one", "two"}
		if !reflect.DeepEqual(names(got), want) {
			t.Errorf("got %v want %v", names(got), want)
		}
	})

	t.Run("terminates_on_acyclic_symlink_dag", func(t *testing.T) {
		// A cycle-free diamond chain: each node holds two symlinks to the
		// next node down. Nothing is ever its own ancestor, so chain-scoped
		// cycle detection never fires — but the same node is reachable along
		// 2^depth distinct paths, so the walk must dedupe by real path (not
		// just by ancestry) to stay bounded.
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		const depth = 13
		nodes := make([]string, depth+1)
		for i := range nodes {
			nodes[i] = filepath.Join(repo, "nodes", "d"+strconv.Itoa(i))
			if err := os.MkdirAll(nodes[i], 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < depth; i++ {
			for _, l := range []string{"a", "b"} {
				if err := os.Symlink(nodes[i+1], filepath.Join(nodes[i], l)); err != nil {
					t.Fatal(err)
				}
			}
		}
		mkSkill(t, filepath.Join(nodes[depth], "leaf"))
		if err := os.MkdirAll(filepath.Join(repo, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(nodes[0], filepath.Join(repo, "skills", "entry")); err != nil {
			t.Fatal(err)
		}

		done := make(chan struct{})
		var got []RepoSkill
		go func() {
			got, _ = WalkRepoSkills(repo, "skills")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("walk did not terminate within 5s — exponential fan-out over an acyclic symlink DAG")
		}
		// Terminating isn't enough: every distinct root-to-leaf path is its
		// own skill name, so deduping descent has to bound the *result* too.
		// The leaf is one real directory and must be reported once, not once
		// per path (2^depth of them) — each would be linked into every
		// project on a "*"-style profile.
		if len(got) != 1 {
			t.Errorf("got %d skills, want 1 — one real leaf reported once per path", len(got))
		}
	})

	t.Run("rejects_hidden_sibling_symlink_escaping_repo", func(t *testing.T) {
		// The containment check must not inherit the discovery walk's
		// "skip hidden entries" rule: the whole skill directory is linked
		// into the store and copied by export, dotfiles included, so a
		// hidden escaping symlink leaks exactly like a visible one.
		base := t.TempDir()
		secret := filepath.Join(base, "id_rsa")
		if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
			t.Fatal(err)
		}
		repo := filepath.Join(base, "repo")
		skill := filepath.Join(repo, "skills", "evil")
		mkSkill(t, skill)
		if err := os.Symlink(secret, filepath.Join(skill, ".env")); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v want no skills — hidden symlink escapes repo", names(got))
		}
	})

	t.Run("rejects_symlink_escaping_repo_inside_hidden_dir", func(t *testing.T) {
		base := t.TempDir()
		secret := filepath.Join(base, "id_rsa")
		if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
			t.Fatal(err)
		}
		repo := filepath.Join(base, "repo")
		skill := filepath.Join(repo, "skills", "evil")
		mkSkill(t, skill)
		hidden := filepath.Join(skill, ".cache")
		if err := os.MkdirAll(hidden, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, filepath.Join(hidden, "k.md")); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v want no skills — symlink in hidden dir escapes repo", names(got))
		}
	})

	t.Run("keeps_skill_with_dangling_symlink", func(t *testing.T) {
		// A symlink whose target doesn't exist can't leak anything. Real
		// repos have them (an unfetched LFS pointer, a sparse-checkout gap,
		// a generated file) and rejecting the whole skill over one is a
		// false positive, not a defence.
		repo := t.TempDir()
		skill := filepath.Join(repo, "skills", "good")
		mkSkill(t, skill)
		if err := os.Symlink("generated.md", filepath.Join(skill, "out.md")); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"good"}
		if !reflect.DeepEqual(names(got), want) {
			t.Errorf("got %v want %v — dangling in-repo symlink must not reject the skill", names(got), want)
		}
	})

	t.Run("rejects_dangling_symlink_escaping_repo", func(t *testing.T) {
		// A dangling link is only harmless while it stays in-repo. Pointing
		// outside, it is adopted now and live the moment the target appears
		// — by which time the skill is already linked into every project and
		// the scan result is cached.
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		skill := filepath.Join(repo, "skills", "sneaky")
		mkSkill(t, skill)
		if err := os.Symlink("../../../../not-there-yet/id_rsa", filepath.Join(skill, "key.md")); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v want no skills — dangling symlink points outside the repo", names(got))
		}
	})

	t.Run("alias_fan_out_scans_each_skill_dir_once", func(t *testing.T) {
		// The SKILL.md match runs before the descent dedupe so that aliases
		// each yield a skill. Without caching the containment scan that meant
		// N aliases of one skill directory cost N full recursive scans of it,
		// which a repo can turn into a multi-second stall on every shell
		// (activate runs from the claude() wrapper). Asserted as a scan count
		// rather than a duration: the property is "once per real directory",
		// and a timing budget would only be a proxy for it — and a flaky one
		// on a loaded machine.
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		shared := filepath.Join(repo, "shared")
		mkSkill(t, shared)
		skillsDir := filepath.Join(repo, "skills")
		if err := os.MkdirAll(skillsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		const aliases = 50
		for i := 0; i < aliases; i++ {
			if err := os.Symlink(shared, filepath.Join(skillsDir, "a"+strconv.Itoa(i))); err != nil {
				t.Fatal(err)
			}
		}
		escapeScans.Store(0)
		got, err := WalkRepoSkills(repo, "skills")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != aliases {
			t.Errorf("got %d skills, want %d (one per alias)", len(got), aliases)
		}
		if got := escapeScans.Load(); got != 1 {
			t.Errorf("containment scan ran %d times, want 1 — all aliases share one real directory", got)
		}
	})

	t.Run("rejects_skills_path_escaping_repo", func(t *testing.T) {
		base := t.TempDir()
		mkSkill(t, filepath.Join(base, "shared-skill"))
		repo := filepath.Join(base, "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "..")
		if len(got) != 0 {
			t.Errorf("expected skills_path escaping repoDir to yield nothing, got %v", names(got))
		}
		// Distinguishable from "this repo has no skills": a rejected path
		// used to return (nil, nil), so the repo silently stopped
		// contributing anything with no message the user could search for.
		if !errors.Is(err, ErrSkillsPathOutsideRepo) {
			t.Errorf("err = %v, want ErrSkillsPathOutsideRepo", err)
		}
	})

	t.Run("repo_root_skills", func(t *testing.T) {
		root := t.TempDir()
		mkSkill(t, filepath.Join(root, "alpha"))
		mkSkill(t, filepath.Join(root, "cat", "beta"))
		// .git at repo root must be skipped.
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, sp := range []string{"", "."} {
			got, _ := WalkRepoSkills(root, sp)
			want := []string{"alpha", "cat-beta"}
			if !reflect.DeepEqual(names(got), want) {
				t.Errorf("skillsPath=%q got %v want %v", sp, names(got), want)
			}
		}
	})
}

// TestDanglingSymlinkChainCannotEscape covers the two-hop shapes: the target
// of a dangling link lands inside the repo only lexically, while a component
// of that path is itself a symlink out of it. The intermediate sits outside
// the skill directory (so the per-skill containment scan never sees it) and
// inside the repo (so the discovery walk never descends to it).
func TestDanglingSymlinkChainCannotEscape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, base, repo string)
	}{
		{"via_in_repo_directory_symlink", func(t *testing.T, base, repo string) {
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(repo, "out")); err != nil {
				t.Fatal(err)
			}
			mkSkill(t, filepath.Join(repo, "skills", "s"))
			if err := os.Symlink("../../out/x", filepath.Join(repo, "skills", "s", "key.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"via_dotdot_collapsing_a_symlink", func(t *testing.T, base, repo string) {
			// filepath.Clean removes "out/.." lexically; the kernel resolves
			// "out" first and then applies "..", so the target actually lands
			// beside whatever "out" points at.
			deep := filepath.Join(base, "outside", "deep")
			if err := os.MkdirAll(deep, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(deep, filepath.Join(repo, "out")); err != nil {
				t.Fatal(err)
			}
			mkSkill(t, filepath.Join(repo, "skills", "s"))
			if err := os.Symlink("../../out/../secret", filepath.Join(repo, "skills", "s", "key.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"via_dotdot_collapse_behind_a_chain", func(t *testing.T, base, repo string) {
			deep := filepath.Join(base, "outside", "deep")
			if err := os.MkdirAll(deep, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(deep, filepath.Join(repo, "out")); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repo, "shared"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../out/../secret", filepath.Join(repo, "shared", "k")); err != nil {
				t.Fatal(err)
			}
			mkSkill(t, filepath.Join(repo, "skills", "s"))
			if err := os.Symlink("../../shared/k", filepath.Join(repo, "skills", "s", "key.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"via_in_repo_file_symlink", func(t *testing.T, base, repo string) {
			if err := os.MkdirAll(filepath.Join(repo, "shared"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../id_rsa", filepath.Join(repo, "shared", "k")); err != nil {
				t.Fatal(err)
			}
			mkSkill(t, filepath.Join(repo, "skills", "sneaky"))
			if err := os.Symlink("../../shared/k", filepath.Join(repo, "skills", "sneaky", "key.md")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			tc.build(t, base, repo)
			got, err := WalkRepoSkills(repo, "skills")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("got %v want no skills — dangling chain reaches outside the repo", names(got))
			}
		})
	}
}

// TestDanglingSymlinkInsideRepoStillAllowed is the false-positive guard: an
// unfetched LFS pointer or a generated file is an in-repo relative path and
// must not cost the repo its skill.
func TestDanglingSymlinkInsideRepoStillAllowed(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(repo, "skills", "good")
	mkSkill(t, skill)
	// Directly dangling, and dangling through an in-repo real directory.
	if err := os.Symlink("generated.md", filepath.Join(skill, "out.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../shared/built.md", filepath.Join(skill, "ref.md")); err != nil {
		t.Fatal(err)
	}
	got, err := WalkRepoSkills(repo, "skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("got %v want [good] — in-repo dangling links must not reject the skill", names(got))
	}
}

// TestGitDirDoesNotBlockRepoRootSkill covers the "one repo = one skill" layout
// (skills_path: "."), where the scanned directory is the repo checkout itself
// and therefore contains .git.
//
// git's own init.templateDir puts symlinked hooks into every clone — a global
// pre-commit hook is a very ordinary setup — and those resolve outside the
// repo by construction. The containment scan deliberately stopped skipping
// dotfiles (a hidden ".env -> ~/.ssh/id_rsa" was being adopted), which made it
// walk .git too and reject the whole repo.
func TestGitDirDoesNotBlockRepoRootSkill(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "global-hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(base, "repo")
	hooks := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(hooks, "pre-commit")); err != nil {
		t.Fatal(err)
	}

	got, err := WalkRepoSkills(repo, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "repo" {
		t.Errorf("got %v want [repo] — a templated git hook must not reject the repo's own skill", names(got))
	}
}

// TestHiddenEscapeStillRejectedAlongsideGitDir pins that ignoring .git did not
// re-open the hole that removing the dotfile skip closed.
func TestHiddenEscapeStillRejectedAlongsideGitDir(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(base, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(repo, ".env")); err != nil {
		t.Fatal(err)
	}
	got, err := WalkRepoSkills(repo, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v want no skills — a hidden escaping symlink outside .git must still reject", names(got))
	}
}
