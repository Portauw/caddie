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
