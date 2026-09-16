package skills

import (
	"os"
	"path/filepath"
	"testing"
)

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
