package export

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyDirExcludesGit covers the "one repo = one skill" layout
// (skills_path: "."), where the skill directory is the git checkout itself.
// .git is not skill content: it carries the remote URL — which for some setups
// embeds a token — and init.templateDir leaves symlinked hooks in it pointing
// outside the repo entirely, which `aws s3 cp --recursive` would follow.
func TestCopyDirExcludesGit(t *testing.T) {
	src := t.TempDir()
	outside := filepath.Join(t.TempDir(), "global-hook")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "SKILL.md"), "# skill\n")
	mustWrite(t, filepath.Join(src, "reference.md"), "notes\n")
	mustWrite(t, filepath.Join(src, ".git", "config"), "url = https://user:TOKEN@example.com/x\n")
	if err := os.MkdirAll(filepath.Join(src, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, ".git", "hooks", "pre-commit")); err != nil {
		t.Fatal(err)
	}
	// A dotfile that is NOT .git must still be copied — only VCS metadata is
	// special, and skipping dotfiles generally is its own bug.
	mustWrite(t, filepath.Join(src, ".keep"), "kept\n")

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git must not be exported: %v", err)
	}
	for _, name := range []string{"SKILL.md", "reference.md", ".keep"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("%s should have been copied: %v", name, err)
		}
	}
	// The dry-run plan has to agree with what is actually copied, or the
	// preview under-reports by the size of the checkout's history.
	if got := countFiles(src); got != 3 {
		t.Errorf("countFiles = %d, want 3 (SKILL.md, reference.md, .keep — not .git)", got)
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
