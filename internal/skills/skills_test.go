package skills

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFilterByPatterns(t *testing.T) {
	items := []Item{
		{DirName: "itp-engineering-writing-skills", Prefix: "itp-engineering", Remainder: "writing-skills"},
		{DirName: "itp-engineering-writing-plans", Prefix: "itp-engineering", Remainder: "writing-plans"},
		{DirName: "itp-engineering-code-review", Prefix: "itp-engineering", Remainder: "code-review"},
		{DirName: "sterling-writing-skills", Prefix: "sterling", Remainder: "writing-skills"},
	}
	for _, tc := range []struct {
		name     string
		patterns []string
		want     []string
	}{
		{"empty", nil, nil},
		{"inclusion", []string{"itp-engineering:*"}, []string{"itp-engineering:writing-skills", "itp-engineering:writing-plans", "itp-engineering:code-review"}},
		{"exact_exclusion", []string{"itp-engineering:*", "!itp-engineering:writing-skills"}, []string{"itp-engineering:writing-plans", "itp-engineering:code-review"}},
		{"wildcard_exclusion", []string{"itp-engineering:*", "!itp-engineering:writing-*"}, []string{"itp-engineering:code-review"}},
		{"exclusion_first", []string{"!itp-engineering:writing-*", "itp-engineering:*"}, []string{"itp-engineering:code-review"}},
		{"exclusion_wins_over_exact", []string{"!itp-engineering:writing-skills", "itp-engineering:*", "itp-engineering:writing-skills"}, []string{"itp-engineering:writing-plans", "itp-engineering:code-review"}},
		{"overlapping_inclusions", []string{"itp-engineering:*", "itp-engineering:code-review", "itp-engineering:*"}, []string{"itp-engineering:writing-skills", "itp-engineering:writing-plans", "itp-engineering:code-review"}},
		{"exclusion_only", []string{"!itp-engineering:writing-skills"}, nil},
		{"exclude_all", []string{"*", "!*"}, nil},
		{"exclude_included_prefix", []string{"itp-engineering:*", "!itp-engineering:*"}, nil},
		{"exclude_missing", []string{"itp-engineering:code-review", "!missing:*"}, []string{"itp-engineering:code-review"}},
		{"outside_included_set", []string{"itp-engineering:code-review", "!sterling:*"}, []string{"itp-engineering:code-review"}},
		{"multiple_exclusions", []string{"*", "!itp-engineering:writing-*", "!sterling:*"}, []string{"itp-engineering:code-review"}},
		{"prefix_scoped_exclusion", []string{"*", "!itp-engineering:writing-*"}, []string{"itp-engineering:code-review", "sterling:writing-skills"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, item := range FilterByPatterns(items, tc.patterns) {
				got = append(got, item.SkillID())
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("selected %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterByPatternsEscapedExclamation(t *testing.T) {
	items := []Item{{DirName: "!custom-skill", Prefix: "!custom", Remainder: "skill"}}
	if got := FilterByPatterns(items, []string{`\!custom:*`}); len(got) != 1 || got[0].SkillID() != "!custom:skill" {
		t.Errorf("escaped exclamation should be a literal inclusion, got %v", got)
	}
}

func TestPatternMatchesIn(t *testing.T) {
	items := []Item{
		{Prefix: "itp-engineering", Remainder: "writing-skills"},
		{Prefix: "itp-engineering", Remainder: "code-review"},
	}
	for _, tc := range []struct {
		pattern string
		want    int
	}{
		{"itp-engineering:*", 2},
		{"!itp-engineering:writing-skills", 1},
		{"!itp-engineering:*", 2},
		{"!missing:*", 0},
		{"!", 0},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			if got := PatternMatchesIn(items, tc.pattern); got != tc.want {
				t.Errorf("matches = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestScanAttributesByCheckoutDir guards the prefix-capture bug: classifying a
// store symlink by an unanchored substring of its target meant a repo whose
// name is a prefix of another's swallowed that repo's skills, so `gws-beta:*`
// matched nothing ("the source may have been removed") while `gws:*` silently
// activated gws-beta's skills.
func TestScanAttributesByCheckoutDir(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CADDIE_DIR", cfg)

	mustWrite(t, filepath.Join(cfg, "sources.yaml"),
		"repos:\n  - name: gws\n    url: https://example.com/gws.git\n  - name: gws-beta\n    url: https://example.com/gws-beta.git\n")

	store := filepath.Join(cfg, "skills")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	// One skill in each checkout, linked into the store under its prefixed name.
	for _, tc := range []struct{ repo, skill string }{
		{"gws", "mail"},
		{"gws-beta", "experimental"},
	} {
		real := filepath.Join(cfg, "repos", tc.repo, "skills", tc.skill)
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(real, "SKILL.md"), "x\n")
		if err := os.Symlink(real, filepath.Join(store, tc.repo+"-"+tc.skill)); err != nil {
			t.Fatal(err)
		}
	}

	items, err := Scan()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.DirName] = it.Prefix
	}
	want := map[string]string{
		"gws-mail":              "gws",
		"gws-beta-experimental": "gws-beta",
	}
	for dir, wantPrefix := range want {
		if got[dir] != wantPrefix {
			t.Errorf("%s attributed to %q, want %q", dir, got[dir], wantPrefix)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
