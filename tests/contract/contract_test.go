// Package contract runs the Go binary and the frozen bash script against
// the same inputs and asserts their observable behavior matches. This is the
// safety net for the strangler migration — any command ported to Go must
// keep its contract test green.
package contract

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// buildGoBinary compiles cmd/ai-env into a temp path and returns it.
func buildGoBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)

	// Ensure the embed target exists (scripts/build.sh normally does this).
	src := filepath.Join(root, "ai-env")
	dst := filepath.Join(root, "internal", "legacy", "ai-env-legacy.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read bash script: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("seed embed file: %v", err)
	}

	out := filepath.Join(t.TempDir(), "ai-env")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/ai-env")
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	return out
}

type runResult struct {
	stdout, stderr string
	exitCode       int
}

type runOpts struct {
	cwd      string
	aiEnvDir string
	home     string // overrides $HOME for the child process
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
	cmd.Env = env

	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run %s: %v", bin, err)
		}
	}
	return runResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}

// TestVersionContract: native --version handler in Go must match the bash
// implementation byte-for-byte.
func TestVersionContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

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

// TestWhichContract: native `which` must match bash across the three resolution
// strategies (project config, directory match, no-match).
func TestWhichContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	setupFixture := func(t *testing.T) (aiEnvDir, projectDir string) {
		t.Helper()
		aiEnvDir = t.TempDir()
		if err := os.MkdirAll(filepath.Join(aiEnvDir, "environments"), 0o755); err != nil {
			t.Fatal(err)
		}
		projectDir = t.TempDir()
		return
	}

	t.Run("project_config", func(t *testing.T) {
		aiEnvDir, projectDir := setupFixture(t)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "my-proj.yaml"), "skills:\n  - \"*\"\n")
		mustWrite(t, filepath.Join(projectDir, ".ai-env.yaml"), "environment: \"my-proj\"\n")
		opts := runOpts{cwd: projectDir, aiEnvDir: aiEnvDir}

		got := runWith(t, goBin, opts, "which")
		want := runWith(t, bashBin, opts, "which")
		diffResult(t, got, want)
	})

	t.Run("unknown_env_reference", func(t *testing.T) {
		aiEnvDir, projectDir := setupFixture(t)
		mustWrite(t, filepath.Join(projectDir, ".ai-env.yaml"), "environment: \"ghost\"\n")
		opts := runOpts{cwd: projectDir, aiEnvDir: aiEnvDir}

		got := runWith(t, goBin, opts, "which")
		want := runWith(t, bashBin, opts, "which")
		diffResult(t, got, want)
	})

	t.Run("directory_match", func(t *testing.T) {
		aiEnvDir, projectDir := setupFixture(t)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "matched.yaml"),
			"directory: \""+projectDir+"\"\n")
		opts := runOpts{cwd: projectDir, aiEnvDir: aiEnvDir}

		got := runWith(t, goBin, opts, "which")
		want := runWith(t, bashBin, opts, "which")
		diffResult(t, got, want)
	})

	t.Run("no_match", func(t *testing.T) {
		aiEnvDir, projectDir := setupFixture(t)
		opts := runOpts{cwd: projectDir, aiEnvDir: aiEnvDir}

		got := runWith(t, goBin, opts, "which")
		want := runWith(t, bashBin, opts, "which")
		diffResult(t, got, want)
	})

	t.Run("active_alias", func(t *testing.T) {
		aiEnvDir, projectDir := setupFixture(t)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "my-proj.yaml"), "skills:\n  - \"*\"\n")
		mustWrite(t, filepath.Join(projectDir, ".ai-env.yaml"), "environment: \"my-proj\"\n")
		opts := runOpts{cwd: projectDir, aiEnvDir: aiEnvDir}

		got := runWith(t, goBin, opts, "active")
		want := runWith(t, bashBin, opts, "active")
		diffResult(t, got, want)
	})
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

// TestListContract: native `list`/`ls` across empty, single, and multi-env
// fixtures with optional name/description/skills fields.
func TestListContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	t.Run("empty", func(t *testing.T) {
		aiEnvDir := t.TempDir()
		os.MkdirAll(filepath.Join(aiEnvDir, "environments"), 0o755)
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "list"), runWith(t, bashBin, opts, "list"))
	})

	t.Run("single_minimal", func(t *testing.T) {
		aiEnvDir := t.TempDir()
		os.MkdirAll(filepath.Join(aiEnvDir, "environments"), 0o755)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "solo.yaml"), "skills:\n  - \"*\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "list"), runWith(t, bashBin, opts, "list"))
	})

	t.Run("full_fields", func(t *testing.T) {
		aiEnvDir := t.TempDir()
		os.MkdirAll(filepath.Join(aiEnvDir, "environments"), 0o755)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "alpha.yaml"),
			"name: \"Alpha Display\"\ndescription: \"first env\"\nskills:\n  - \"a:*\"\n  - \"b:*\"\n")
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "beta.yaml"),
			"description: \"second env\"\nskills:\n  - \"c:*\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "list"), runWith(t, bashBin, opts, "list"))
	})

	t.Run("ls_alias", func(t *testing.T) {
		aiEnvDir := t.TempDir()
		os.MkdirAll(filepath.Join(aiEnvDir, "environments"), 0o755)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "a.yaml"), "skills:\n  - \"*\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "ls"), runWith(t, bashBin, opts, "ls"))
	})
}

// TestSourceContract covers list (empty + populated), add (success + dup + missing cache),
// remove (success + missing).
func TestSourceContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	setup := func(t *testing.T) (aiEnvDir, home string) {
		t.Helper()
		aiEnvDir = t.TempDir()
		home = t.TempDir()
		mustWrite(t, filepath.Join(home, ".claude/plugins/cache/mkt/plg/.keep"), "")
		return
	}

	t.Run("list_empty", func(t *testing.T) {
		aiEnvDir, home := setup(t)
		opts := runOpts{aiEnvDir: aiEnvDir, home: home}
		diffResult(t, runWith(t, goBin, opts, "source", "list"), runWith(t, bashBin, opts, "source", "list"))
	})

	t.Run("list_populated", func(t *testing.T) {
		aiEnvDir, home := setup(t)
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\n"+
				"  - name: \"foo\"\n    marketplace: \"m1\"\n    plugin: \"p1\"\n"+
				"  - name: \"bar\"\n    marketplace: \"m2\"\n    plugin: \"p2\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir, home: home}
		diffResult(t, runWith(t, goBin, opts, "source", "list"), runWith(t, bashBin, opts, "source", "list"))
	})

	t.Run("add_success", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, home := setup(t)
			opts := runOpts{aiEnvDir: aiEnvDir, home: home}
			r := runWith(t, bin, opts, "source", "add", "foo", "mkt", "plg")
			if r.exitCode != 0 {
				t.Fatalf("add with %s: exit=%d stderr=%q", bin, r.exitCode, r.stderr)
			}
			got, _ := os.ReadFile(filepath.Join(aiEnvDir, "sources.yaml"))
			want := "sources:\n  - name: \"foo\"\n    marketplace: \"mkt\"\n    plugin: \"plg\"\n"
			if string(got) != want {
				t.Errorf("%s wrote %q, want %q", bin, string(got), want)
			}
		}
	})

	t.Run("add_duplicate", func(t *testing.T) {
		aiEnvDir, home := setup(t)
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\n  - name: \"foo\"\n    marketplace: \"mkt\"\n    plugin: \"plg\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir, home: home}
		diffResult(t,
			runWith(t, goBin, opts, "source", "add", "foo", "mkt", "plg"),
			runWith(t, bashBin, opts, "source", "add", "foo", "mkt", "plg"),
		)
	})

	t.Run("add_missing_cache", func(t *testing.T) {
		aiEnvDir, home := setup(t)
		opts := runOpts{aiEnvDir: aiEnvDir, home: home}
		got := runWith(t, goBin, opts, "source", "add", "foo", "nope", "gone")
		want := runWith(t, bashBin, opts, "source", "add", "foo", "nope", "gone")
		// AvailableMarketplaces listing order may vary; compare first line + exit.
		gotLine := strings.SplitN(got.stderr, "\n", 2)[0]
		wantLine := strings.SplitN(want.stderr, "\n", 2)[0]
		if gotLine != wantLine {
			t.Errorf("first stderr line mismatch\n  go:   %q\n  bash: %q", gotLine, wantLine)
		}
		if got.exitCode != want.exitCode {
			t.Errorf("exit code mismatch: go=%d bash=%d", got.exitCode, want.exitCode)
		}
	})

	t.Run("remove_success", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, home := setup(t)
			mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
				"sources:\n"+
					"  - name: \"foo\"\n    marketplace: \"m1\"\n    plugin: \"p1\"\n"+
					"  - name: \"bar\"\n    marketplace: \"m2\"\n    plugin: \"p2\"\n")
			opts := runOpts{aiEnvDir: aiEnvDir, home: home}
			r := runWith(t, bin, opts, "source", "remove", "foo")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q", bin, r.exitCode, r.stderr)
			}
			got, _ := os.ReadFile(filepath.Join(aiEnvDir, "sources.yaml"))
			if strings.Contains(string(got), "foo") {
				t.Errorf("%s: foo still present after remove: %q", bin, string(got))
			}
			if !strings.Contains(string(got), "bar") {
				t.Errorf("%s: bar accidentally removed: %q", bin, string(got))
			}
		}
	})

	t.Run("remove_not_found", func(t *testing.T) {
		aiEnvDir, home := setup(t)
		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"), "sources:\n")
		opts := runOpts{aiEnvDir: aiEnvDir, home: home}
		diffResult(t,
			runWith(t, goBin, opts, "source", "remove", "ghost"),
			runWith(t, bashBin, opts, "source", "remove", "ghost"),
		)
	})
}

// TestHelpFallthrough: `help` is not ported yet — the Go binary must delegate
// to legacy and produce identical output.
func TestHelpFallthrough(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	got := run(t, goBin, "help")
	want := run(t, bashBin, "help")

	if got.stdout != want.stdout {
		t.Errorf("stdout mismatch (len go=%d bash=%d)", len(got.stdout), len(want.stdout))
	}
	if got.exitCode != want.exitCode {
		t.Errorf("exit code mismatch: go=%d bash=%d", got.exitCode, want.exitCode)
	}
}
