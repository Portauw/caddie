package skills

import (
	"os"
	"path/filepath"
	"reflect"
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

	t.Run("rejects_skills_path_escaping_repo", func(t *testing.T) {
		base := t.TempDir()
		mkSkill(t, filepath.Join(base, "shared-skill"))
		repo := filepath.Join(base, "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := WalkRepoSkills(repo, "..")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("expected skills_path escaping repoDir to yield nothing, got %v", names(got))
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
