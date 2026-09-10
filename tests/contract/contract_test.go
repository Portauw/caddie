// Package contract runs the Go binary and the frozen bash script against
// the same inputs and asserts their observable behavior matches. This is the
// safety net for the strangler migration — any command ported to Go must
// keep its contract test green.
package contract

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// repoRoot returns the repository root (two levels up from this test file).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// sharedGoBinary is built once per `go test` invocation by TestMain and
// reused across every contract test. Path is absolute.
var sharedGoBinary string

// buildGoBinary returns the shared binary path, failing the test if TestMain
// could not build it.
func buildGoBinary(t *testing.T) string {
	t.Helper()
	if sharedGoBinary == "" {
		t.Fatal("sharedGoBinary not initialized; TestMain did not run")
	}
	return sharedGoBinary
}

// buildSharedBinary compiles cmd/caddie once for the test process.
func buildSharedBinary() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot determine caller path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	tmpDir, err := os.MkdirTemp("", "caddie-contract-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(tmpDir, "caddie")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/caddie")
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build: %w", err)
	}
	return out, nil
}

func TestMain(m *testing.M) {
	bin, err := buildSharedBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sharedGoBinary = bin
	code := m.Run()
	_ = os.RemoveAll(filepath.Dir(bin))
	os.Exit(code)
}

type runResult struct {
	stdout, stderr string
	exitCode       int
}

type runOpts struct {
	cwd      string
	aiEnvDir string
	home     string // overrides $HOME for the child process
	stdin    string // piped to child process stdin
	env      []string
}

func run(t *testing.T, bin string, args ...string) runResult {
	return runWith(t, bin, runOpts{aiEnvDir: t.TempDir()}, args...)
}

func runWith(t *testing.T, bin string, opts runOpts, args ...string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if opts.cwd != "" {
		cmd.Dir = opts.cwd
	}
	env := os.Environ()
	if opts.aiEnvDir != "" {
		env = append(env, "AI_ENV_DIR="+opts.aiEnvDir)
	}
	if opts.home != "" {
		env = append(env, "HOME="+opts.home)
	}
	env = append(env, opts.env...)
	cmd.Env = env
	if opts.stdin != "" {
		cmd.Stdin = strings.NewReader(opts.stdin)
	}

	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run %s: %v", bin, err)
		}
	}
	out, errOut := stdout.String(), stderr.String()
	// The frozen bash predates the caddie rename and emits "ai-env" in
	// user-facing strings. Normalize so byte-diff parity tests still pass.
	if strings.HasSuffix(bin, "ai-env-frozen") {
		out = strings.ReplaceAll(out, "ai-env", "caddie")
		errOut = strings.ReplaceAll(errOut, "ai-env", "caddie")
	}
	return runResult{stdout: out, stderr: errOut, exitCode: code}
}

// TestVersionContract: native --version handler in Go must match the bash
// implementation byte-for-byte.
func TestVersionContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	for _, flag := range []string{"--version", "-v"} {
		t.Run(strings.TrimLeft(flag, "-"), func(t *testing.T) {
			got := run(t, goBin, flag)
			want := run(t, bashBin, flag)

			if got.stdout != want.stdout {
				t.Errorf("stdout mismatch\n  go:   %q\n  bash: %q", got.stdout, want.stdout)
			}
			if got.exitCode != want.exitCode {
				t.Errorf("exit code mismatch: go=%d bash=%d", got.exitCode, want.exitCode)
			}
		})
	}
}

// TestWhichContract: `which` prints the resolved profile path. Go-only
// assertions: the frozen bash resolves environments, which no longer exist.
func TestWhichContract(t *testing.T) {
	goBin := buildGoBinary(t)

	t.Run("profile_in_cwd", func(t *testing.T) {
		projectDir := realTempDir(t)
		profilePath := filepath.Join(projectDir, ".caddie.yaml")
		mustWrite(t, profilePath, "skills:\n  - \"*\"\n")
		got := runWith(t, goBin, runOpts{cwd: projectDir, aiEnvDir: t.TempDir()}, "which")
		if got.exitCode != 0 {
			t.Fatalf("exit %d, stderr %q", got.exitCode, got.stderr)
		}
		if strings.TrimSpace(got.stdout) != profilePath {
			t.Errorf("stdout = %q, want exactly %q", got.stdout, profilePath)
		}
	})

	t.Run("walk_up_from_subdir", func(t *testing.T) {
		projectDir := realTempDir(t)
		profilePath := filepath.Join(projectDir, ".caddie.yaml")
		mustWrite(t, profilePath, "skills:\n  - \"*\"\n")
		nested := filepath.Join(projectDir, "deep", "deeper")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		got := runWith(t, goBin, runOpts{cwd: nested, aiEnvDir: t.TempDir()}, "which")
		if got.exitCode != 0 {
			t.Fatalf("exit %d, stderr %q", got.exitCode, got.stderr)
		}
		if strings.TrimSpace(got.stdout) != profilePath {
			t.Errorf("stdout = %q, want exactly the ancestor's profile %q", got.stdout, profilePath)
		}
	})

	t.Run("no_profile", func(t *testing.T) {
		projectDir := t.TempDir()
		got := runWith(t, goBin, runOpts{cwd: projectDir, aiEnvDir: t.TempDir()}, "which")
		if got.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", got.exitCode)
		}
		if strings.TrimSpace(got.stdout) != "" {
			t.Errorf("stdout = %q, want empty so command substitution stays safe", got.stdout)
		}
		if !strings.Contains(got.stderr, "No caddie profile found") {
			t.Errorf("stderr %q missing the not-found message", got.stderr)
		}
		if !strings.Contains(got.stderr, "caddie init") {
			t.Errorf("stderr %q missing the caddie init hint", got.stderr)
		}
	})
}

// setupEnvDir creates a fresh AI_ENV_DIR containing an empty environments/
// subdir and returns its path. Use for tests that need a valid caddie layout.
func setupEnvDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "environments"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// realTempDir returns t.TempDir() with symlinks resolved. On darwin, t.TempDir()
// lives under /var, which is itself a symlink to /private/var; a child process's
// os.Getwd() reports the resolved /private/var path, so exact-match assertions
// against a subprocess's stdout must compare against the resolved form.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
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

func diffResult(t *testing.T, got, want runResult) {
	t.Helper()
	if got.stdout != want.stdout {
		t.Errorf("stdout mismatch\n  go:   %q\n  bash: %q", got.stdout, want.stdout)
	}
	if got.stderr != want.stderr {
		t.Errorf("stderr mismatch\n  go:   %q\n  bash: %q", got.stderr, want.stderr)
	}
	if got.exitCode != want.exitCode {
		t.Errorf("exit code mismatch: go=%d bash=%d", got.exitCode, want.exitCode)
	}
}

// TestSourceRejected verifies that the removed `source` subcommand dies
// with a clear message instead of falling through to legacy. This is the
// user-facing "plugin sources are gone" contract.
func TestSourceRejected(t *testing.T) {
	goBin := buildGoBinary(t)
	for _, sub := range []string{"list", "add", "remove"} {
		t.Run(sub, func(t *testing.T) {
			r := runWith(t, goBin, runOpts{aiEnvDir: t.TempDir()}, "source", sub)
			if r.exitCode == 0 {
				t.Fatalf("expected non-zero exit, got stdout=%q", r.stdout)
			}
			if !strings.Contains(r.stderr, "has been removed") {
				t.Errorf("stderr should mention removal, got %q", r.stderr)
			}
		})
	}
}

// TestEditContract: edit opens the nearest .caddie.yaml in $EDITOR and prints
// "Updated: <path>". Uses EDITOR=true (no-op) so the test doesn't hang. The
// frozen bash resolves named environments and can never match this behavior,
// so these assertions are Go-only.
func TestEditContract(t *testing.T) {
	goBin := buildGoBinary(t)

	t.Run("opens_nearest_profile", func(t *testing.T) {
		projectDir := realTempDir(t)
		profilePath := filepath.Join(projectDir, ".caddie.yaml")
		mustWrite(t, profilePath, "skills:\n  - \"*\"\n")
		opts := runOpts{cwd: projectDir, aiEnvDir: t.TempDir(), env: []string{"EDITOR=true"}}
		r := runWith(t, goBin, opts, "edit")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if _, err := os.Stat(profilePath); err != nil {
			t.Errorf("profile gone after edit: %v", err)
		}
		if !strings.Contains(r.stdout, profilePath) {
			t.Errorf("stdout %q missing profile path %q", r.stdout, profilePath)
		}
	})

	t.Run("walk_up_from_subdir", func(t *testing.T) {
		projectDir := realTempDir(t)
		profilePath := filepath.Join(projectDir, ".caddie.yaml")
		mustWrite(t, profilePath, "skills:\n  - \"*\"\n")
		nested := filepath.Join(projectDir, "deep", "deeper")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		opts := runOpts{cwd: nested, aiEnvDir: t.TempDir(), env: []string{"EDITOR=true"}}
		r := runWith(t, goBin, opts, "edit")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if !strings.Contains(r.stdout, profilePath) {
			t.Errorf("stdout %q missing the ancestor's profile path %q", r.stdout, profilePath)
		}
	})

	t.Run("rejects_argument", func(t *testing.T) {
		opts := runOpts{cwd: t.TempDir(), aiEnvDir: t.TempDir(), env: []string{"EDITOR=true"}}
		r := runWith(t, goBin, opts, "edit", "myenv")
		if r.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", r.exitCode)
		}
		if !strings.Contains(r.stderr, "Usage") {
			t.Errorf("stderr %q missing Usage message", r.stderr)
		}
	})

	t.Run("no_profile", func(t *testing.T) {
		projectDir := t.TempDir()
		opts := runOpts{cwd: projectDir, aiEnvDir: t.TempDir(), env: []string{"EDITOR=true"}}
		r := runWith(t, goBin, opts, "edit")
		if r.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", r.exitCode)
		}
		if !strings.Contains(r.stderr, "No caddie profile found") {
			t.Errorf("stderr %q missing the not-found message", r.stderr)
		}
	})
}

// TestRepoListContract: native `repo list` must match bash across empty (no
// sources file), no-repos-section, single, and multi-repo fixtures. Status
// column depends on whether `repo_dir/.git` exists; we skip the status path
// since neither impl clones, so both emit `(not cloned)`.
func TestRepoListContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	t.Run("no_sources_file", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "repo", "list"), runWith(t, bashBin, opts, "repo", "list"))
	})

	t.Run("sources_without_repos_section", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\n  - name: \"x\"\n    marketplace: \"m\"\n    plugin: \"p\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "repo", "list"), runWith(t, bashBin, opts, "repo", "list"))
	})

	t.Run("single_repo", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\nrepos:\n  - name: \"lenny\"\n    url: \"https://example.com/lenny\"\n    skills_path: \"skills\"\n    prefix: \"true\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "repo", "list"), runWith(t, bashBin, opts, "repo", "list"))
	})

	t.Run("multi_repo_prefix_variants", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		// Four prefix variants: explicit, true, false, (absent).
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"repos:\n"+
				"  - name: \"alpha\"\n    url: \"u1\"\n    skills_path: \"skills\"\n    prefix: \"custom\"\n"+
				"  - name: \"bravo\"\n    url: \"u2\"\n    skills_path: \"s\"\n    prefix: \"true\"\n"+
				"  - name: \"charlie\"\n    url: \"u3\"\n    skills_path: \"skills\"\n    prefix: \"false\"\n"+
				"  - name: \"delta\"\n    url: \"u4\"\n    skills_path: \"skills\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "repo", "list"), runWith(t, bashBin, opts, "repo", "list"))
	})

	t.Run("ls_alias", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"repos:\n  - name: \"alpha\"\n    url: \"u1\"\n    skills_path: \"skills\"\n    prefix: \"true\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "repo", "ls"), runWith(t, bashBin, opts, "repo", "ls"))
	})
}

// makeBareRepo creates a local bare git repo with a single committed file,
// usable as a clone URL for offline contract tests. Returns the bare repo path.
func makeBareRepo(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	bare := t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		// Silence git config warnings about missing identity.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run(work, "git", "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(work, "skills", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "skills", "alpha", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "git", "add", "-A")
	run(work, "git", "commit", "-q", "-m", "init")
	run(work, "git", "clone", "-q", "--bare", work, bare)
	return bare
}

// TestRepoAddContract: native `repo add` must match bash — success clones
// into $CONFIG_DIR/repos/<name> and appends to sources.yaml, missing-args dies,
// duplicate names die.
func TestRepoAddContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	t.Run("missing_args", func(t *testing.T) {
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "repo", "add"),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "repo", "add"),
		)
	})

	t.Run("success_clone", func(t *testing.T) {
		url := makeBareRepo(t)
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			opts := runOpts{aiEnvDir: aiEnvDir}
			r := runWith(t, bin, opts, "repo", "add", "mine", url)
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q stdout=%q", bin, r.exitCode, r.stderr, r.stdout)
			}
			// sources.yaml should contain the repo block.
			body, _ := os.ReadFile(filepath.Join(aiEnvDir, "sources.yaml"))
			want := "  - name: \"mine\"\n    url: \"" + url + "\"\n    skills_path: \"skills\"\n"
			if !strings.Contains(string(body), want) {
				t.Errorf("%s: sources.yaml missing repo block, got %q", bin, string(body))
			}
			if !strings.Contains(string(body), "repos:") {
				t.Errorf("%s: sources.yaml missing repos: header, got %q", bin, string(body))
			}
			// Clone happened.
			if _, err := os.Stat(filepath.Join(aiEnvDir, "repos", "mine", ".git")); err != nil {
				t.Errorf("%s: clone target missing: %v", bin, err)
			}
		}
	})

	t.Run("duplicate_name", func(t *testing.T) {
		url := makeBareRepo(t)
		// Seed sources.yaml with the target name already present.
		seed := "sources:\n\nrepos:\n  - name: \"mine\"\n    url: \"u\"\n    skills_path: \"skills\"\n"
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		mustWrite(t, filepath.Join(a, "sources.yaml"), seed)
		mustWrite(t, filepath.Join(b, "sources.yaml"), seed)
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "repo", "add", "mine", url),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "repo", "add", "mine", url),
		)
	})
}

// TestRepoRemoveContract: native `repo remove` must match bash — success cleans
// the checkout + sources.yaml block, not-found dies, `rm` alias works.
func TestRepoRemoveContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	seedRegistered := func(t *testing.T, aiEnvDir, name string) {
		t.Helper()
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\n\nrepos:\n  - name: \""+name+"\"\n    url: \"u\"\n    skills_path: \"skills\"\n")
		// Fake a cloned checkout dir — doesn't need to be a real git repo.
		if err := os.MkdirAll(filepath.Join(aiEnvDir, "repos", name, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("success", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			seedRegistered(t, aiEnvDir, "mine")
			opts := runOpts{aiEnvDir: aiEnvDir}
			r := runWith(t, bin, opts, "repo", "remove", "mine")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q", bin, r.exitCode, r.stderr)
			}
			// Checkout gone.
			if _, err := os.Stat(filepath.Join(aiEnvDir, "repos", "mine")); !os.IsNotExist(err) {
				t.Errorf("%s: checkout still present: %v", bin, err)
			}
			// sources.yaml no longer contains the name.
			body, _ := os.ReadFile(filepath.Join(aiEnvDir, "sources.yaml"))
			if strings.Contains(string(body), "mine") {
				t.Errorf("%s: repo block still present: %q", bin, string(body))
			}
		}
	})

	t.Run("not_found", func(t *testing.T) {
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		seed := "sources:\n\nrepos:\n  - name: \"other\"\n    url: \"u\"\n    skills_path: \"skills\"\n"
		mustWrite(t, filepath.Join(a, "sources.yaml"), seed)
		mustWrite(t, filepath.Join(b, "sources.yaml"), seed)
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "repo", "remove", "ghost"),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "repo", "remove", "ghost"),
		)
	})

	t.Run("no_repos_section", func(t *testing.T) {
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		mustWrite(t, filepath.Join(a, "sources.yaml"), "sources:\n")
		mustWrite(t, filepath.Join(b, "sources.yaml"), "sources:\n")
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "repo", "remove", "ghost"),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "repo", "remove", "ghost"),
		)
	})

	t.Run("rm_alias", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			seedRegistered(t, aiEnvDir, "mine")
			opts := runOpts{aiEnvDir: aiEnvDir}
			r := runWith(t, bin, opts, "repo", "rm", "mine")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q", bin, r.exitCode, r.stderr)
			}
			if _, err := os.Stat(filepath.Join(aiEnvDir, "repos", "mine")); !os.IsNotExist(err) {
				t.Errorf("%s: checkout still present", bin)
			}
		}
	})
}

// TestInventoryContract: native `inventory` must match bash across an empty
// store, mixed-prefix plain dirs, and dirs + symlinks that resolve to the
// sources/repos cache (to exercise plugin/repo classification).
func TestInventoryContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	t.Run("no_store", func(t *testing.T) {
		// Go-only assertion: the frozen bash points at its own "init" (now
		// caddie's machine-setup command); the Go binary must point at
		// "caddie setup" instead, since "init" now creates a profile.
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir}
		got := runWith(t, goBin, opts, "inventory")
		if got.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", got.exitCode)
		}
		if !strings.Contains(got.stderr, "caddie setup") {
			t.Errorf("stderr %q missing the caddie setup hint", got.stderr)
		}
	})

	t.Run("empty_store", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		if err := os.MkdirAll(filepath.Join(aiEnvDir, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "inventory"), runWith(t, bashBin, opts, "inventory"))
	})

	t.Run("mixed_plain_dirs", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		store := filepath.Join(aiEnvDir, "skills")
		for _, d := range []string{"foo-bar", "foo-baz", "standalone", "other-thing"} {
			if err := os.MkdirAll(filepath.Join(store, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "inventory"), runWith(t, bashBin, opts, "inventory"))
	})

	t.Run("repo_symlink_and_plain_dirs", func(t *testing.T) {
		// Go-only assertion: after dropping plugin sources, symlinks into a
		// registered repo get the repo's prefix + "(repo)" is no longer
		// printed; everything else (plain dirs, orphan symlinks) is TypeLocal.
		aiEnvDir := setupEnvDir(t)
		store := filepath.Join(aiEnvDir, "skills")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{"local-foo", "standalone"} {
			if err := os.MkdirAll(filepath.Join(store, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		repoTarget := filepath.Join(aiEnvDir, "repos/lenny/skills/lenny-thing")
		if err := os.MkdirAll(repoTarget, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(repoTarget, filepath.Join(store, "lenny-thing")); err != nil {
			t.Fatal(err)
		}
		orphanTarget := filepath.Join(aiEnvDir, "somewhere/else/lonely")
		if err := os.MkdirAll(orphanTarget, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(orphanTarget, filepath.Join(store, "lonely")); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\nrepos:\n  - name: \"lenny\"\n    url: \"u\"\n    skills_path: \"skills\"\n    prefix: \"true\"\n")

		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir}, "inventory")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		// Repo skill dirname is "lenny-thing"; repo's effective prefix is
		// "lenny", so inventory strips it and shows "lenny:thing".
		for _, want := range []string{"lenny:thing", "local:foo", "local:standalone", "local:lonely"} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("missing %q in inventory:\n%s", want, r.stdout)
			}
		}
		for _, banned := range []string{"(plugin)", "(repo)", "mysrc"} {
			if strings.Contains(r.stdout, banned) {
				t.Errorf("inventory still contains removed marker %q:\n%s", banned, r.stdout)
			}
		}
	})

	t.Run("filter_argument", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		store := filepath.Join(aiEnvDir, "skills")
		for _, d := range []string{"foo-bar", "foo-baz", "other-thing"} {
			if err := os.MkdirAll(filepath.Join(store, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "inventory", "foo:"), runWith(t, bashBin, opts, "inventory", "foo:"))
	})
}

// TestHelpContract: `help`, `--help`, `-h` all print the Go-native help.
// After removing plugin-source commands the help diverges from bash — so this
// is a Go-only assertion rather than a byte-diff against the frozen bash.
func TestHelpContract(t *testing.T) {
	goBin := buildGoBinary(t)

	for _, flag := range []string{"help", "--help", "-h"} {
		t.Run(strings.TrimLeft(flag, "-"), func(t *testing.T) {
			r := run(t, goBin, flag)
			if r.exitCode != 0 {
				t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
			}
			for _, must := range []string{"USAGE", "SETUP", "GIT REPOS", "ENVIRONMENTS", "caddie repo add"} {
				if !strings.Contains(r.stdout, must) {
					t.Errorf("help missing %q:\n%s", must, r.stdout)
				}
			}
			for _, banned := range []string{"source list", "source add", "source remove", "plugin source"} {
				if strings.Contains(r.stdout, banned) {
					t.Errorf("help still mentions removed %q:\n%s", banned, r.stdout)
				}
			}
		})
	}
}

// TestRepoUpdateContract: native `repo update` must match bash — pull all,
// pull by name, "not found" error when name mismatches, no-repos-section dies.
func TestRepoUpdateContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	t.Run("no_repos_section", func(t *testing.T) {
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "repo", "update"),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "repo", "update"),
		)
	})

	t.Run("missing_sources_file", func(t *testing.T) {
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "repo", "update"),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "repo", "update"),
		)
	})

	t.Run("name_not_found", func(t *testing.T) {
		bare := makeBareRepo(t)
		seed := "sources:\n\nrepos:\n  - name: \"mine\"\n    url: \"" + bare + "\"\n    skills_path: \"skills\"\n"
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		mustWrite(t, filepath.Join(a, "sources.yaml"), seed)
		mustWrite(t, filepath.Join(b, "sources.yaml"), seed)
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "repo", "update", "ghost"),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "repo", "update", "ghost"),
		)
	})

	// For the clone-and-update paths the output includes the commit count, so
	// we assert on exit-code + on-disk state + repos match, not stdout parity.
	t.Run("clones_then_up_to_date", func(t *testing.T) {
		url := makeBareRepo(t)
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
				"sources:\n\nrepos:\n  - name: \"mine\"\n    url: \""+url+"\"\n    skills_path: \"skills\"\n")
			opts := runOpts{aiEnvDir: aiEnvDir}
			// First invocation clones.
			r1 := runWith(t, bin, opts, "repo", "update", "mine")
			if r1.exitCode != 0 {
				t.Fatalf("%s clone: exit=%d stderr=%q stdout=%q", bin, r1.exitCode, r1.stderr, r1.stdout)
			}
			if _, err := os.Stat(filepath.Join(aiEnvDir, "repos", "mine", ".git")); err != nil {
				t.Fatalf("%s: clone dir missing: %v", bin, err)
			}
			// Second invocation is up-to-date.
			r2 := runWith(t, bin, opts, "repo", "update", "mine")
			if r2.exitCode != 0 {
				t.Fatalf("%s up-to-date: exit=%d stderr=%q stdout=%q", bin, r2.exitCode, r2.stderr, r2.stdout)
			}
			if !strings.Contains(r2.stdout, "already up to date") {
				t.Errorf("%s: expected up-to-date marker, got stdout=%q", bin, r2.stdout)
			}
		}
	})

	t.Run("all_mode_iterates_two", func(t *testing.T) {
		url1 := makeBareRepo(t)
		url2 := makeBareRepo(t)
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
				"sources:\n\nrepos:\n"+
					"  - name: \"one\"\n    url: \""+url1+"\"\n    skills_path: \"skills\"\n"+
					"  - name: \"two\"\n    url: \""+url2+"\"\n    skills_path: \"skills\"\n")
			opts := runOpts{aiEnvDir: aiEnvDir}
			r := runWith(t, bin, opts, "repo", "update")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q stdout=%q", bin, r.exitCode, r.stderr, r.stdout)
			}
			for _, n := range []string{"one", "two"} {
				if _, err := os.Stat(filepath.Join(aiEnvDir, "repos", n, ".git")); err != nil {
					t.Errorf("%s: %s not cloned: %v", bin, n, err)
				}
			}
		}
	})
}

// TestInitContract: `init` writes .caddie.yaml in cwd from piped answers,
// and refuses when one already exists. Go-only assertions.
func TestInitContract(t *testing.T) {
	goBin := buildGoBinary(t)

	t.Run("writes_profile", func(t *testing.T) {
		projectDir := t.TempDir()
		opts := runOpts{
			cwd:      projectDir,
			aiEnvDir: t.TempDir(),
			stdin:    "My Project\nA description\nsuperpowers:*\n\n",
		}
		got := runWith(t, goBin, opts, "init")
		if got.exitCode != 0 {
			t.Fatalf("exit %d, stderr %q", got.exitCode, got.stderr)
		}
		data, err := os.ReadFile(filepath.Join(projectDir, ".caddie.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		want := "name: \"My Project\"\ndescription: \"A description\"\n\nskills:\n  - \"superpowers:*\"\n"
		if string(data) != want {
			t.Errorf("profile contents\n got: %q\nwant: %q", data, want)
		}
	})

	t.Run("refuses_when_present", func(t *testing.T) {
		projectDir := t.TempDir()
		mustWrite(t, filepath.Join(projectDir, ".caddie.yaml"), "skills:\n  - \"*\"\n")
		opts := runOpts{cwd: projectDir, aiEnvDir: t.TempDir(), stdin: "\n\n\n"}
		got := runWith(t, goBin, opts, "init")
		if got.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", got.exitCode)
		}
		if !strings.Contains(got.stderr, "already exists") {
			t.Errorf("stderr %q missing the refusal", got.stderr)
		}
	})

	t.Run("defaults_from_empty_input", func(t *testing.T) {
		projectDir := realTempDir(t)
		opts := runOpts{cwd: projectDir, aiEnvDir: t.TempDir(), stdin: "\n\n\n"}
		got := runWith(t, goBin, opts, "init")
		if got.exitCode != 0 {
			t.Fatalf("exit %d, stderr %q", got.exitCode, got.stderr)
		}
		data, err := os.ReadFile(filepath.Join(projectDir, ".caddie.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("name: %q\ndescription: \"\"\n\nskills:\n  - \"*\"\n", filepath.Base(projectDir))
		if string(data) != want {
			t.Errorf("profile contents\n got: %q\nwant: %q", data, want)
		}
	})

	t.Run("gitignore_written_at_repo_root_from_subdir", func(t *testing.T) {
		repo := realTempDir(t)
		if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		sub := filepath.Join(repo, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		opts := runOpts{cwd: sub, aiEnvDir: t.TempDir(), stdin: "\n\n\n"}
		got := runWith(t, goBin, opts, "init")
		if got.exitCode != 0 {
			t.Fatalf("exit %d, stderr %q", got.exitCode, got.stderr)
		}
		data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
		if err != nil {
			t.Fatalf("repo root .gitignore not written: %v", err)
		}
		if !strings.Contains(string(data), ".caddie.yaml") {
			t.Errorf(".gitignore at repo root %q missing .caddie.yaml", data)
		}
	})
}

// TestResetContract: native `reset` must match bash across -f success, -f with
// nothing to clean (idempotent), confirmation 'n' cancels, confirmation 'y'
// proceeds. HOME is overridden to a temp dir so the real user's ~/.agents is
// never touched.
func TestResetContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	setup := func(t *testing.T) (aiEnvDir, home string) {
		t.Helper()
		aiEnvDir = setupEnvDir(t)
		home = t.TempDir()
		// Seed a managed symlink layout: store with one real dir and one
		// symlink, ~/.agents/skills with a managed symlink.
		store := filepath.Join(aiEnvDir, "skills")
		if err := os.MkdirAll(filepath.Join(store, "real-skill"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(home, "target-skill")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(store, "linked-skill")); err != nil {
			t.Fatal(err)
		}
		agents := filepath.Join(home, ".agents", "skills")
		if err := os.MkdirAll(agents, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(agents, "linked-skill")); err != nil {
			t.Fatal(err)
		}
		return
	}

	t.Run("force_success", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, home := setup(t)
			opts := runOpts{aiEnvDir: aiEnvDir, home: home}
			r := runWith(t, bin, opts, "reset", "-f")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q stdout=%q", bin, r.exitCode, r.stderr, r.stdout)
			}
			// Skill store must be gone.
			if _, err := os.Stat(filepath.Join(aiEnvDir, "skills")); !os.IsNotExist(err) {
				t.Errorf("%s: skill store still present: %v", bin, err)
			}
			// Managed symlink in ~/.agents/skills must be gone.
			if _, err := os.Lstat(filepath.Join(home, ".agents", "skills", "linked-skill")); !os.IsNotExist(err) {
				t.Errorf("%s: managed symlink still present: %v", bin, err)
			}
		}
	})

	t.Run("force_idempotent_empty", func(t *testing.T) {
		// Go-only: bash reset scans for plugin re-enables which we removed.
		// Just assert the Go handler succeeds on an empty layout and doesn't
		// reference plugin state.
		a := setupEnvDir(t)
		home := t.TempDir()
		r := runWith(t, goBin, runOpts{aiEnvDir: a, home: home}, "reset", "-f")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q stdout=%q", r.exitCode, r.stderr, r.stdout)
		}
		for _, banned := range []string{"re-enable", "plugin", "enabledPlugins"} {
			if strings.Contains(r.stdout, banned) {
				t.Errorf("reset output still mentions plugins %q:\n%s", banned, r.stdout)
			}
		}
	})

	t.Run("confirm_no_cancels", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, home := setup(t)
			opts := runOpts{aiEnvDir: aiEnvDir, home: home, stdin: "n\n"}
			r := runWith(t, bin, opts, "reset")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q", bin, r.exitCode, r.stderr)
			}
			// Store must still exist — user said no.
			if _, err := os.Stat(filepath.Join(aiEnvDir, "skills")); err != nil {
				t.Errorf("%s: skill store removed despite 'n': %v", bin, err)
			}
			if !strings.Contains(r.stdout, "Aborted.") {
				t.Errorf("%s: expected Aborted. in stdout, got %q", bin, r.stdout)
			}
		}
	})

	t.Run("confirm_yes_proceeds", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, home := setup(t)
			opts := runOpts{aiEnvDir: aiEnvDir, home: home, stdin: "y\n"}
			r := runWith(t, bin, opts, "reset")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q stdout=%q", bin, r.exitCode, r.stderr, r.stdout)
			}
			if _, err := os.Stat(filepath.Join(aiEnvDir, "skills")); !os.IsNotExist(err) {
				t.Errorf("%s: skill store not removed: %v", bin, err)
			}
		}
	})

	t.Run("force_seeded_cleanup", func(t *testing.T) {
		// Go-only: verify reset -f cleans the seeded managed layout end-to-end.
		aiEnvDir, home := setup(t)
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "reset", "-f")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if _, err := os.Stat(filepath.Join(aiEnvDir, "skills")); !os.IsNotExist(err) {
			t.Errorf("skill store not removed: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(home, ".agents", "skills", "linked-skill")); !os.IsNotExist(err) {
			t.Errorf("managed symlink not cleaned: %v", err)
		}
	})
}

// cloneBareInto runs `git clone <bare> <dest>` under a controlled environment.
// Used by scan tests to stage a checked-out repo without network access.
func cloneBareInto(t *testing.T, bare, dest string) {
	t.Helper()
	cmd := exec.Command("git", "clone", "-q", bare, dest)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
}

// TestScanContract: Go-only asserts on scan's filesystem side-effects and key
// stdout markers. Scan is not byte-diffed against bash because bash also scans
// plugin sources (which the Go impl no longer does).
func TestScanContract(t *testing.T) {
	goBin := buildGoBinary(t)

	t.Run("no_repos", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if !strings.Contains(r.stdout, "skills in canonical store") {
			t.Errorf("expected counts line, got %q", r.stdout)
		}
		if !strings.Contains(r.stdout, "0 local, 0 from repos") {
			t.Errorf("expected 0/0 split, got %q", r.stdout)
		}
	})

	t.Run("single_repo_clone_and_sync", func(t *testing.T) {
		bare := makeBareRepo(t)
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		cloneBareInto(t, bare, filepath.Join(aiEnvDir, "repos", "mine"))
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\n\nrepos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n    skills_path: \"skills\"\n")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q stdout=%q", r.exitCode, r.stderr, r.stdout)
		}
		link := filepath.Join(aiEnvDir, "skills", "alpha")
		li, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("expected symlink at %s: %v", link, err)
		}
		if li.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", link)
		}
		if !strings.Contains(r.stdout, "0 local, 1 from repos") {
			t.Errorf("expected 0/1 split, got %q", r.stdout)
		}
	})

	t.Run("nested_categories", func(t *testing.T) {
		// Build a bare repo whose layout uses category dirs:
		// skills/cloud/foo/SKILL.md and skills/web/bar/SKILL.md.
		work := t.TempDir()
		bare := t.TempDir()
		gitRun := func(dir string, args ...string) {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
		}
		gitRun(work, "git", "init", "-q", "-b", "main")
		for _, p := range []string{"cloud/foo", "web/bar"} {
			d := filepath.Join(work, "skills", p)
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitRun(work, "git", "add", "-A")
		gitRun(work, "git", "commit", "-q", "-m", "init")
		gitRun(work, "git", "clone", "-q", "--bare", work, bare)

		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		cloneBareInto(t, bare, filepath.Join(aiEnvDir, "repos", "mine"))
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\n\nrepos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n    skills_path: \"skills\"\n")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q stdout=%q", r.exitCode, r.stderr, r.stdout)
		}
		for _, want := range []string{"cloud-foo", "web-bar"} {
			link := filepath.Join(aiEnvDir, "skills", want)
			li, err := os.Lstat(link)
			if err != nil {
				t.Errorf("expected symlink %s: %v", want, err)
				continue
			}
			if li.Mode()&os.ModeSymlink == 0 {
				t.Errorf("%s is not a symlink", want)
			}
		}
		// Category dirs themselves must not appear as skills.
		for _, banned := range []string{"cloud", "web"} {
			if _, err := os.Lstat(filepath.Join(aiEnvDir, "skills", banned)); err == nil {
				t.Errorf("category dir %s should not be a store entry", banned)
			}
		}
	})

	t.Run("prefix_true", func(t *testing.T) {
		bare := makeBareRepo(t)
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		cloneBareInto(t, bare, filepath.Join(aiEnvDir, "repos", "mine"))
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n    skills_path: \"skills\"\n    prefix: \"true\"\n")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if _, err := os.Lstat(filepath.Join(aiEnvDir, "skills", "mine-alpha")); err != nil {
			t.Errorf("expected prefixed symlink mine-alpha: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(aiEnvDir, "skills", "alpha")); err == nil {
			t.Errorf("unprefixed symlink should not exist")
		}
	})

	t.Run("prefix_literal", func(t *testing.T) {
		bare := makeBareRepo(t)
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		cloneBareInto(t, bare, filepath.Join(aiEnvDir, "repos", "mine"))
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n    skills_path: \"skills\"\n    prefix: \"zz\"\n")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if _, err := os.Lstat(filepath.Join(aiEnvDir, "skills", "zz-alpha")); err != nil {
			t.Errorf("expected prefixed symlink zz-alpha: %v", err)
		}
	})

	t.Run("local_shadow_warning", func(t *testing.T) {
		bare := makeBareRepo(t)
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		cloneBareInto(t, bare, filepath.Join(aiEnvDir, "repos", "mine"))
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n    skills_path: \"skills\"\n")
		// Seed a real local dir at the same skill name.
		localDir := filepath.Join(aiEnvDir, "skills", "alpha")
		if err := os.MkdirAll(localDir, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(localDir, "SKILL.md"), "local version")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if !strings.Contains(r.stdout, "shadowing repo versions") {
			t.Errorf("expected shadow warning, got %q", r.stdout)
		}
		li, err := os.Lstat(localDir)
		if err != nil {
			t.Fatal(err)
		}
		if li.Mode()&os.ModeSymlink != 0 {
			t.Errorf("local dir was replaced with symlink")
		}
	})

	t.Run("broken_symlink_cleanup", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		store := filepath.Join(aiEnvDir, "skills")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		broken := filepath.Join(store, "gone")
		if err := os.Symlink("/does/not/exist/anywhere", broken); err != nil {
			t.Fatal(err)
		}
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d", r.exitCode)
		}
		if _, err := os.Lstat(broken); !os.IsNotExist(err) {
			t.Errorf("broken symlink not removed: %v", err)
		}
	})

	t.Run("sweep_from_agent_dir", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		agentSkill := filepath.Join(home, ".agents", "skills", "dropped")
		if err := os.MkdirAll(agentSkill, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(agentSkill, "SKILL.md"), "swept me")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home}, "scan")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if _, err := os.Stat(filepath.Join(aiEnvDir, "skills", "dropped", "SKILL.md")); err != nil {
			t.Errorf("swept skill missing in store: %v", err)
		}
		if _, err := os.Stat(agentSkill); !os.IsNotExist(err) {
			t.Errorf("source dir still present after sweep: %v", err)
		}
	})

	t.Run("cache_skip", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		opts := runOpts{aiEnvDir: aiEnvDir, home: home}
		r1 := runWith(t, goBin, opts, "scan")
		if r1.exitCode != 0 {
			t.Fatalf("first scan exit=%d", r1.exitCode)
		}
		r2 := runWith(t, goBin, opts, "scan", "-v")
		if r2.exitCode != 0 {
			t.Fatalf("second scan exit=%d", r2.exitCode)
		}
		if !strings.Contains(r2.stdout, "Scan cached") {
			t.Errorf("expected cache skip, got %q", r2.stdout)
		}
	})

	t.Run("force_rescan", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		opts := runOpts{aiEnvDir: aiEnvDir, home: home}
		r1 := runWith(t, goBin, opts, "scan", "-f")
		if r1.exitCode != 0 {
			t.Fatalf("first -f exit=%d", r1.exitCode)
		}
		r2 := runWith(t, goBin, opts, "scan", "-f")
		if r2.exitCode != 0 {
			t.Fatalf("second -f exit=%d", r2.exitCode)
		}
		if strings.Contains(r2.stdout, "Scan cached") {
			t.Errorf("force should bypass cache: %q", r2.stdout)
		}
		if !strings.Contains(r2.stdout, "Scan complete") {
			t.Errorf("expected scan complete marker: %q", r2.stdout)
		}
	})
}

// setupActivateFixture creates a ready-to-activate env: caddie dir with a
// cloned repo + sources.yaml + env YAML binding `directory:` to projectDir.
// Returns (goBin, aiEnvDir, home, projectDir).
func setupActivateFixture(t *testing.T, profileName string, skillPatterns []string) (aiEnvDir, home, projectDir string) {
	t.Helper()
	aiEnvDir = setupEnvDir(t)
	home = t.TempDir()
	projectDir = t.TempDir()
	bare := makeBareRepo(t)
	cloneBareInto(t, bare, filepath.Join(aiEnvDir, "repos", "mine"))
	mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
		"repos:\n  - name: \"mine\"\n    url: \""+bare+"\"\n    skills_path: \"skills\"\n")

	body := "name: \"" + profileName + "\"\nskills:\n"
	for _, p := range skillPatterns {
		body += "  - \"" + p + "\"\n"
	}
	mustWrite(t, filepath.Join(projectDir, ".caddie.yaml"), body)
	return aiEnvDir, home, projectDir
}

// TestActivateContract exercises the native `activate`/`use` handler.
// Bash parity is not required where the Go side intentionally diverges
// (no SOURCES.md generation); those subtests assert Go's own invariants.
func TestActivateContract(t *testing.T) {
	goBin := buildGoBinary(t)

	t.Run("first_run", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}, "activate")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q stdout=%q", r.exitCode, r.stderr, r.stdout)
		}
		link := filepath.Join(projectDir, ".agents", "skills", "alpha")
		if li, err := os.Lstat(link); err != nil || li.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected skill symlink at %s: err=%v", link, err)
		}
		claude := filepath.Join(projectDir, ".claude", "skills")
		if li, err := os.Lstat(claude); err != nil || li.Mode()&os.ModeSymlink == 0 {
			t.Errorf(".claude/skills missing or not a symlink: %v", err)
		}
		if _, err := os.Stat(filepath.Join(projectDir, ".claude", ".caddie-fingerprint")); err != nil {
			t.Errorf("fingerprint not written: %v", err)
		}
	})

	t.Run("idempotent_second_run", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		opts := runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}
		_ = runWith(t, goBin, opts, "activate")
		r := runWith(t, goBin, opts, "activate")
		if r.exitCode != 0 {
			t.Fatalf("second exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if !strings.Contains(r.stdout, "unchanged") {
			t.Errorf("expected 'unchanged' on re-activate, got %q", r.stdout)
		}
	})

	t.Run("walk_up_from_subdir", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		nested := filepath.Join(projectDir, "deep", "deeper")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home, cwd: nested}, "activate")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q stdout=%q", r.exitCode, r.stderr, r.stdout)
		}
		if _, err := os.Lstat(filepath.Join(projectDir, ".agents", "skills", "alpha")); err != nil {
			t.Errorf("expected skill symlink in project dir after walk-up resolve: %v", err)
		}
		if _, err := os.Stat(filepath.Join(nested, ".agents")); !os.IsNotExist(err) {
			t.Errorf(".agents should NOT be created in the nested cwd: err=%v", err)
		}
	})

	t.Run("dry_run", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}, "activate", "--dry-run")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if !strings.Contains(r.stdout, "Dry run") {
			t.Errorf("expected 'Dry run' in stdout, got %q", r.stdout)
		}
		if !strings.Contains(r.stdout, "Profile:") || !strings.Contains(r.stdout, "Folder:") {
			t.Errorf("expected 'Profile:' and 'Folder:' labels, got %q", r.stdout)
		}
		// No mutations allowed.
		if _, err := os.Stat(filepath.Join(projectDir, ".agents", "skills")); !os.IsNotExist(err) {
			t.Errorf(".agents/skills should not exist after dry-run: %v", err)
		}
		if _, err := os.Stat(filepath.Join(projectDir, ".claude", ".caddie-fingerprint")); !os.IsNotExist(err) {
			t.Errorf("fingerprint should not be written on dry-run: %v", err)
		}
	})

	t.Run("no_profile_found", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		home := t.TempDir()
		cwd := t.TempDir()
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home, cwd: cwd}, "activate")
		if r.exitCode == 0 {
			t.Errorf("expected non-zero exit, got stdout=%q", r.stdout)
		}
		if !strings.Contains(r.stdout, "No caddie profile found") && !strings.Contains(r.stderr, "No caddie profile found") {
			t.Errorf("expected 'No caddie profile found', got stdout=%q stderr=%q", r.stdout, r.stderr)
		}
	})

	t.Run("positional_arg_rejected", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}, "activate", "proj")
		if r.exitCode == 0 {
			t.Errorf("expected non-zero exit for positional argument")
		}
		if !strings.Contains(r.stderr, "Unknown argument") && !strings.Contains(r.stdout, "Unknown argument") {
			t.Errorf("expected 'Unknown argument', got stderr=%q stdout=%q", r.stderr, r.stdout)
		}
	})

	t.Run("gitignore_appended_idempotent", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		// Init a real .git dir in project.
		if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		opts := runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}
		_ = runWith(t, goBin, opts, "activate")
		_ = runWith(t, goBin, opts, "activate")
		body, err := os.ReadFile(filepath.Join(projectDir, ".gitignore"))
		if err != nil {
			t.Fatalf("gitignore missing: %v", err)
		}
		for _, e := range []string{".claude/skills/", ".agents/skills/", ".claude/.caddie-fingerprint"} {
			count := strings.Count(string(body), e+"\n")
			if count != 1 {
				t.Errorf("entry %q appears %d times (want 1) in: %s", e, count, string(body))
			}
		}
	})

	t.Run("no_sources_manifest", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}, "activate")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d", r.exitCode)
		}
		if _, err := os.Stat(filepath.Join(projectDir, ".agents", "SOURCES.md")); !os.IsNotExist(err) {
			t.Errorf(".agents/SOURCES.md should NOT exist (bash parity divergence): err=%v", err)
		}
	})

	t.Run("fingerprint_invalidated_by_skill_change", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"*"})
		opts := runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}
		r1 := runWith(t, goBin, opts, "activate")
		if r1.exitCode != 0 {
			t.Fatalf("first activate exit=%d stderr=%q", r1.exitCode, r1.stderr)
		}
		// Add a new skill to the store directly.
		extra := filepath.Join(aiEnvDir, "skills", "bravo")
		if err := os.MkdirAll(extra, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(extra, "SKILL.md"), "x")
		// Bust scan cache so new skill is considered.
		_ = os.Remove(filepath.Join(aiEnvDir, ".last-scan"))

		r2 := runWith(t, goBin, opts, "activate")
		if r2.exitCode != 0 {
			t.Fatalf("second activate exit=%d stderr=%q", r2.exitCode, r2.stderr)
		}
		if strings.Contains(r2.stdout, "unchanged") {
			t.Errorf("expected reconcile (not 'unchanged') after new skill added, got %q", r2.stdout)
		}
		if _, err := os.Lstat(filepath.Join(projectDir, ".agents", "skills", "bravo")); err != nil {
			t.Errorf("new skill 'bravo' not linked after second activate: %v", err)
		}
	})

	t.Run("orphaned_pattern_warning", func(t *testing.T) {
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"ghost-source:*"})
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}, "activate")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q stdout=%q", r.exitCode, r.stderr, r.stdout)
		}
		if !strings.Contains(r.stdout, `"ghost-source:*"`) || !strings.Contains(r.stdout, "matches 0 skills") {
			t.Errorf("expected orphaned-pattern warning, got %q", r.stdout)
		}
	})

	t.Run("orphaned_pattern_warning_fast_path", func(t *testing.T) {
		// First activate reconciles (no fingerprint yet) and should warn.
		// Second activate hits the unchanged fast path and must NOT repeat
		// the warning, since nothing changed. --dry-run bypasses the fast
		// path entirely and must keep warning regardless.
		aiEnvDir, home, projectDir := setupActivateFixture(t, "proj", []string{"ghost-source:*"})
		opts := runOpts{aiEnvDir: aiEnvDir, home: home, cwd: projectDir}

		r1 := runWith(t, goBin, opts, "activate")
		if r1.exitCode != 0 {
			t.Fatalf("first exit=%d stderr=%q", r1.exitCode, r1.stderr)
		}
		if !strings.Contains(r1.stdout, "matches 0 skills") {
			t.Errorf("expected warning on first (reconciling) activate, got %q", r1.stdout)
		}

		r2 := runWith(t, goBin, opts, "activate")
		if r2.exitCode != 0 {
			t.Fatalf("second exit=%d stderr=%q", r2.exitCode, r2.stderr)
		}
		if !strings.Contains(r2.stdout, "unchanged") {
			t.Errorf("expected 'unchanged' on second activate, got %q", r2.stdout)
		}
		if strings.Contains(r2.stdout, "matches 0 skills") {
			t.Errorf("warning should not repeat on the fast path, got %q", r2.stdout)
		}

		r3 := runWith(t, goBin, opts, "activate", "--dry-run")
		if r3.exitCode != 0 {
			t.Fatalf("dry-run exit=%d stderr=%q", r3.exitCode, r3.stderr)
		}
		if !strings.Contains(r3.stdout, "matches 0 skills") {
			t.Errorf("expected dry-run to still warn after fast path settled, got %q", r3.stdout)
		}
	})
}

// makeExportStore populates an AI_ENV_DIR's skill store with the given
// skills. Each name becomes `skills/<name>/` containing SKILL.md (required
// by bash's resolve pipeline) plus a content.txt file so the copy step has
// something to move.
func makeExportStore(t *testing.T, aiEnvDir string, names ...string) {
	t.Helper()
	store := filepath.Join(aiEnvDir, "skills")
	for _, n := range names {
		dir := filepath.Join(store, n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "SKILL.md"), "# "+n+"\n")
		mustWrite(t, filepath.Join(dir, "content.txt"), n+" content\n")
	}
}

// TestExportContract is Go-only except for dir_all_flag, which still runs as
// a bash byte-diff parity test: --all never reads a profile, so it exercises
// the frozen bash export machinery independent of the profile migration. The
// rest assert against the native implementation directly, since export now
// resolves the cwd .caddie.yaml instead of a named registry entry, and bash
// has no equivalent behavior to diff against. S3 subtests are deferred (see
// NOTE at bottom) because they require a live aws CLI + S3 endpoint.
func TestExportContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env-frozen")

	// diffPathNormalized compares two runResults after substituting per-side
	// path variables (target dir, AI_ENV_DIR) with stable placeholders. The go
	// and bash runs must write to independent directories, so verbatim stdout
	// will never match — but after substitution, the structure of the output
	// (counts, colors, ordering) must be identical.
	diffPathNormalized := func(t *testing.T, got, want runResult, goPaths, bashPaths []string) {
		t.Helper()
		norm := func(s string, paths []string) string {
			// Replace longest-first so nested paths don't leave fragments.
			sorted := make([]string, len(paths))
			copy(sorted, paths)
			sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
			for i, p := range sorted {
				// Also handle /private prefix darwin adds via realpath — bash
				// resolves symlinks when printing real_dir, so /var/.. → /private/var/..
				s = strings.ReplaceAll(s, p, fmt.Sprintf("<P%d>", i))
				if strings.HasPrefix(p, "/var/") {
					s = strings.ReplaceAll(s, "/private"+p, fmt.Sprintf("<P%d>", i))
				}
			}
			return s
		}
		g := runResult{stdout: norm(got.stdout, goPaths), stderr: norm(got.stderr, goPaths), exitCode: got.exitCode}
		w := runResult{stdout: norm(want.stdout, bashPaths), stderr: norm(want.stderr, bashPaths), exitCode: want.exitCode}
		diffResult(t, g, w)
	}

	// Common fixture: two-skill store, plus a cwd profile matching both.
	setupStore := func(t *testing.T) (aiEnvDir, cwd string) {
		aiEnvDir = setupEnvDir(t)
		makeExportStore(t, aiEnvDir, "foo-one", "foo-two")
		cwd = realTempDir(t)
		mustWrite(t, filepath.Join(cwd, ".caddie.yaml"), "skills:\n  - \"foo:*\"\n")
		return
	}

	t.Run("dir_basic", func(t *testing.T) {
		aiEnvDir, cwd := setupStore(t)
		target := filepath.Join(t.TempDir(), "out")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, cwd: cwd}, "export", "--to", target)
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		for _, n := range []string{"foo-one", "foo-two"} {
			if _, err := os.Stat(filepath.Join(target, n, "SKILL.md")); err != nil {
				t.Errorf("%s/%s/SKILL.md missing: %v", target, n, err)
			}
		}
	})

	// dir_all_flag is intentionally left as a bash byte-diff parity test:
	// --all never reads a profile, so it still exercises the frozen bash and
	// guards the export machinery independent of the profile migration.
	t.Run("dir_all_flag", func(t *testing.T) {
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		makeExportStore(t, a, "foo-one", "bar-two", "baz")
		makeExportStore(t, b, "foo-one", "bar-two", "baz")
		ta := filepath.Join(t.TempDir(), "out-a")
		tb := filepath.Join(t.TempDir(), "out-b")
		got := runWith(t, goBin, runOpts{aiEnvDir: a}, "export", "--all", "--to", ta)
		want := runWith(t, bashBin, runOpts{aiEnvDir: b}, "export", "--all", "--to", tb)
		diffPathNormalized(t, got, want, []string{ta, a}, []string{tb, b})
	})

	t.Run("dir_clean", func(t *testing.T) {
		aiEnvDir, cwd := setupStore(t)
		target := filepath.Join(t.TempDir(), "out")
		// Pre-populate the target with a stale directory that should be removed.
		if err := os.MkdirAll(filepath.Join(target, "stale"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(target, "stale", "old.txt"), "old\n")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, cwd: cwd}, "export", "--to", target, "--clean")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if _, err := os.Stat(filepath.Join(target, "stale")); !os.IsNotExist(err) {
			t.Errorf("%s/stale should be removed: %v", target, err)
		}
	})

	t.Run("dir_dry_run", func(t *testing.T) {
		aiEnvDir, cwd := setupStore(t)
		target := filepath.Join(t.TempDir(), "out")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, cwd: cwd}, "export", "--to", target, "--dry-run")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		// Dry run must not create the target.
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("%s should not exist after dry-run: %v", target, err)
		}
		// The fixture's "foo:*" pattern matches both foo-one and foo-two, and
		// there is nothing to remove (target doesn't exist yet), so
		// internal/export.Local's summary line reads "would export 2 skills,
		// remove 0" (see the Dry run branch in internal/export/export.go).
		const wantLine = "Dry run: would export 2 skills, remove 0"
		if !strings.Contains(r.stdout, wantLine) {
			t.Errorf("stdout %q missing dry-run summary %q", r.stdout, wantLine)
		}
	})

	t.Run("no_profile_no_all", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		cwd := realTempDir(t) // no .caddie.yaml here
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, cwd: cwd}, "export", "--to", "/tmp/x")
		if r.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", r.exitCode)
		}
		if !strings.Contains(r.stderr, "No caddie profile found") {
			t.Errorf("stderr %q missing the not-found message", r.stderr)
		}
		// export's hint must point to --all as the alternative to a profile;
		// the frozen bash said "Specify an environment name or use --all."
		if !strings.Contains(r.stderr, "--all") {
			t.Errorf("stderr %q missing the --all hint", r.stderr)
		}
	})

	t.Run("missing_to_flag", func(t *testing.T) {
		aiEnvDir, cwd := setupStore(t)
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, cwd: cwd}, "export")
		if r.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", r.exitCode)
		}
		if !strings.Contains(r.stderr, "Usage: caddie export") {
			t.Errorf("stderr %q missing the usage message", r.stderr)
		}
	})

	t.Run("no_match_warns", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		makeExportStore(t, aiEnvDir, "foo-one")
		cwd := realTempDir(t)
		mustWrite(t, filepath.Join(cwd, ".caddie.yaml"), "skills:\n  - \"nonexistent:*\"\n")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, cwd: cwd}, "export", "--to", "/tmp/nomatch")
		if r.exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if !strings.Contains(r.stdout, "No skills matched") {
			t.Errorf("stdout %q missing the 'No skills matched' warning", r.stdout)
		}
	})

	t.Run("rejects_positional_arg", func(t *testing.T) {
		aiEnvDir, cwd := setupStore(t)
		target := filepath.Join(t.TempDir(), "out")
		r := runWith(t, goBin, runOpts{aiEnvDir: aiEnvDir, cwd: cwd}, "export", "demo", "--to", target)
		if r.exitCode == 0 {
			t.Errorf("expected non-zero exit for positional argument")
		}
		if !strings.Contains(r.stderr, "no longer takes a profile name") {
			t.Errorf("stderr %q missing the positional-arg rejection message", r.stderr)
		}
	})

	// NOTE: S3 subtests are deferred. They would require a live aws CLI plus
	// either a real bucket or a local emulator (minio/moto). Skipping here
	// keeps `go test` hermetic. When run with a reachable endpoint, the
	// subtests would exercise:
	//   - s3_basic:   export to s3:// URI, verify objects uploaded
	//   - s3_clean:   stale prefixes removed via `aws s3 rm --recursive`
	//   - s3_dry_run: "upload" plan lines without any aws calls
	//   - s3_aws_env: AWS_PROFILE / AWS_ENDPOINT_URL threaded as flags
	t.Run("s3_deferred", func(t *testing.T) {
		t.Skip("requires aws CLI + S3 endpoint (see NOTE in source)")
	})
}
