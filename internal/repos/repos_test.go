package repos

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGitCommandBlocksExtTransport guards against a regression of the
// ext:: RCE: a repo URL like `ext::sh -c '...'` must not execute anything
// when handed to a GitCommand-built subprocess.
func TestGitCommandBlocksExtTransport(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := GitCommand(ctx, "ls-remote", "ext::sh -c \"touch "+marker+"\"")
	_ = cmd.Run()

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ext:: transport executed a command — GIT_ALLOW_PROTOCOL allowlist did not block it")
	}
}

// TestGitCommandAllowsFileTransport is the flip side: local-path clones
// (used by the contract test fixtures, and legitimate for a local skill
// repo) must keep working.
func TestGitCommandAllowsFileTransport(t *testing.T) {
	src := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := GitCommand(ctx, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("-C", src, "init", "--quiet")
	run("-C", src, "-c", "user.email=t@t.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "x", "--quiet")

	dst := filepath.Join(t.TempDir(), "clone")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := GitCommand(ctx, "clone", "--depth", "1", "--", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("local clone should be allowed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err != nil {
		t.Fatalf("expected clone at %s: %v", dst, err)
	}
}

func TestValidName(t *testing.T) {
	// The checkout path is built by joining the name onto Dir(), and
	// `repo remove` rm -rf's it, so traversal here is a delete primitive.
	bad := []string{
		"",
		".",
		"..",
		"../../../Documents/thesis",
		"foo/bar",
		`foo\bar`,
		"/etc",
		"-upload-pack=touch",
		"~/Documents",
		"a\x00b",
	}
	for _, name := range bad {
		if err := ValidName(name); err == nil {
			t.Errorf("ValidName(%q) = nil, want an error", name)
		}
		if _, err := CheckoutDir(name); err == nil {
			t.Errorf("CheckoutDir(%q) = nil error, want one", name)
		}
	}

	good := []string{"lenny", "gws-beta", "my_repo", "Repo.2", "skills4"}
	for _, name := range good {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", name, err)
		}
		dir, err := CheckoutDir(name)
		if err != nil {
			t.Fatalf("CheckoutDir(%q) = %v", name, err)
		}
		if got, want := filepath.Dir(dir), filepath.Clean(Dir()); got != want {
			t.Errorf("CheckoutDir(%q) parent = %q, want %q", name, got, want)
		}
	}
}
