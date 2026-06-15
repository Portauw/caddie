package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
