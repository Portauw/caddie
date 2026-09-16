package repos

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ext:: transport executed a command — GIT_ALLOW_PROTOCOL allowlist did not block it")
	}
	// The marker being absent is not on its own evidence: it is equally
	// absent when git is missing from PATH, or when the invocation fails for
	// some unrelated reason. Without this, a refactor that dropped the
	// allowlist but broke GitCommand another way would still pass.
	if err == nil {
		t.Fatal("expected git to fail, got a clean exit — the ext:: URL was not rejected")
	}
	if !strings.Contains(stderr.String(), "transport 'ext' not allowed") {
		t.Fatalf("git failed for the wrong reason: err=%v stderr=%q", err, stderr.String())
	}
}

// TestGitCommandAllowsFileTransport is the flip side: local-path clones
// (used by the contract test fixtures, and legitimate for a local skill
// repo) must keep working.
// TestGitCommandOverridesPermissiveGitConfig is the test that actually
// exercises caddie's allowlist. Modern git already refuses ext:: by default,
// so the behavioural test above passes with or without GIT_ALLOW_PROTOCOL —
// verified by deleting the allowlist and watching it still pass. What the
// allowlist is *for* is the user whose git config re-enables the transport;
// GIT_ALLOW_PROTOCOL overrides protocol.<name>.allow, and this pins that.
func TestGitCommandOverridesPermissiveGitConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Belt and braces: git prefers XDG/GIT_CONFIG_GLOBAL when set.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	marker := filepath.Join(t.TempDir(), "pwned")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[protocol \"ext\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := GitCommand(ctx, "ls-remote", "ext::sh -c \"touch "+marker+"\"")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ext:: executed despite the allowlist — protocol.ext.allow=always won")
	}
	if err == nil || !strings.Contains(stderr.String(), "transport 'ext' not allowed") {
		t.Fatalf("expected the allowlist to reject ext::, got err=%v stderr=%q", err, stderr.String())
	}
}

// TestGitCommandAllowlistWinsOverInheritedEnv pins the mechanism os/exec
// relies on: Cmd.Env keeps the LAST occurrence of a duplicated key, so
// appending the allowlist after os.Environ() overrides a value the user
// already exported.
func TestGitCommandAllowlistWinsOverInheritedEnv(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext")
	cmd := GitCommand(context.Background(), "version")
	last := ""
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "GIT_ALLOW_PROTOCOL="); ok {
			last = v
		}
	}
	if want := strings.TrimPrefix(gitAllowedProtocols, "GIT_ALLOW_PROTOCOL="); last != want {
		t.Errorf("effective GIT_ALLOW_PROTOCOL = %q, want %q", last, want)
	}
	if strings.Contains(last, "ext") {
		t.Errorf("allowlist %q permits the ext transport", last)
	}
}

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
