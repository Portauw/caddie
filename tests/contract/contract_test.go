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
