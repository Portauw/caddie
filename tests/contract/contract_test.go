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

// buildSharedBinary compiles cmd/ai-env once for the test process. It also
// refreshes internal/legacy/ai-env-legacy.sh (the //go:embed target) from the
// frozen bash script at the repo root, matching scripts/build.sh.
func buildSharedBinary() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot determine caller path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	src := filepath.Join(root, "ai-env")
	dst := filepath.Join(root, "internal", "legacy", "ai-env-legacy.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("read bash script: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return "", fmt.Errorf("seed embed file: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "ai-env-contract-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(tmpDir, "ai-env")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/ai-env")
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

// setupEnvDir creates a fresh AI_ENV_DIR containing an empty environments/
// subdir and returns its path. Use for tests that need a valid ai-env layout.
func setupEnvDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "environments"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
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
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "list"), runWith(t, bashBin, opts, "list"))
	})

	t.Run("single_minimal", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "solo.yaml"), "skills:\n  - \"*\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "list"), runWith(t, bashBin, opts, "list"))
	})

	t.Run("full_fields", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "alpha.yaml"),
			"name: \"Alpha Display\"\ndescription: \"first env\"\nskills:\n  - \"a:*\"\n  - \"b:*\"\n")
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "beta.yaml"),
			"description: \"second env\"\nskills:\n  - \"c:*\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "list"), runWith(t, bashBin, opts, "list"))
	})

	t.Run("ls_alias", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
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

// TestEditContract: edit invokes $EDITOR on the env file and prints "Updated: <name>".
// Uses EDITOR=true (no-op) so the test doesn't hang.
func TestEditContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	t.Run("success", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			envFile := filepath.Join(aiEnvDir, "environments", "myenv.yaml")
			mustWrite(t, envFile, "skills:\n  - \"*\"\n")
			opts := runOpts{aiEnvDir: aiEnvDir, env: []string{"EDITOR=true"}}
			r := runWith(t, bin, opts, "edit", "myenv")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q", bin, r.exitCode, r.stderr)
			}
			if _, err := os.Stat(envFile); err != nil {
				t.Errorf("%s: env file gone after edit: %v", bin, err)
			}
		}
	})

	t.Run("not_found", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir, env: []string{"EDITOR=true"}}
		diffResult(t, runWith(t, goBin, opts, "edit", "ghost"), runWith(t, bashBin, opts, "edit", "ghost"))
	})

	t.Run("no_arg", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir, env: []string{"EDITOR=true"}}
		diffResult(t, runWith(t, goBin, opts, "edit"), runWith(t, bashBin, opts, "edit"))
	})
}

// TestDeleteContract: success with "y", cancel with "n", not found.
func TestDeleteContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	setup := func(t *testing.T) (aiEnvDir, envFile string) {
		aiEnvDir = setupEnvDir(t)
		envFile = filepath.Join(aiEnvDir, "environments", "myenv.yaml")
		mustWrite(t, envFile, "skills:\n  - \"*\"\n")
		return
	}

	t.Run("confirm_yes", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, envFile := setup(t)
			opts := runOpts{aiEnvDir: aiEnvDir, stdin: "y\n"}
			r := runWith(t, bin, opts, "delete", "myenv")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q stdout=%q", bin, r.exitCode, r.stderr, r.stdout)
			}
			if _, err := os.Stat(envFile); err == nil {
				t.Errorf("%s: env file still exists after delete", bin)
			}
		}
	})

	t.Run("confirm_no", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, envFile := setup(t)
			opts := runOpts{aiEnvDir: aiEnvDir, stdin: "n\n"}
			r := runWith(t, bin, opts, "delete", "myenv")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d", bin, r.exitCode)
			}
			if _, err := os.Stat(envFile); err != nil {
				t.Errorf("%s: env file removed despite 'n': %v", bin, err)
			}
		}
	})

	t.Run("compare_prompt", func(t *testing.T) {
		a, _ := setup(t)
		b, _ := setup(t)
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a, stdin: "y\n"}, "delete", "myenv"),
			runWith(t, bashBin, runOpts{aiEnvDir: b, stdin: "y\n"}, "delete", "myenv"),
		)
	})

	t.Run("not_found", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "delete", "ghost"), runWith(t, bashBin, opts, "delete", "ghost"))
	})

	t.Run("rm_alias", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir, envFile := setup(t)
			opts := runOpts{aiEnvDir: aiEnvDir, stdin: "y\n"}
			r := runWith(t, bin, opts, "rm", "myenv")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d", bin, r.exitCode)
			}
			if _, err := os.Stat(envFile); err == nil {
				t.Errorf("%s: env file still exists after rm", bin)
			}
		}
	})
}

// TestCloneContract: success, src missing, dest exists.
func TestCloneContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	t.Run("success", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			content := "name: \"Src\"\nskills:\n  - \"*\"\n"
			mustWrite(t, filepath.Join(aiEnvDir, "environments", "src.yaml"), content)
			opts := runOpts{aiEnvDir: aiEnvDir}
			r := runWith(t, bin, opts, "clone", "src", "dst")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d stderr=%q", bin, r.exitCode, r.stderr)
			}
			got, _ := os.ReadFile(filepath.Join(aiEnvDir, "environments", "dst.yaml"))
			if string(got) != content {
				t.Errorf("%s: dest content %q want %q", bin, string(got), content)
			}
		}
	})

	t.Run("src_missing", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "clone", "ghost", "new"), runWith(t, bashBin, opts, "clone", "ghost", "new"))
	})

	t.Run("dest_exists", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "a.yaml"), "skills:\n  - \"*\"\n")
		mustWrite(t, filepath.Join(aiEnvDir, "environments", "b.yaml"), "skills:\n  - \"*\"\n")
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "clone", "a", "b"), runWith(t, bashBin, opts, "clone", "a", "b"))
	})

	t.Run("cp_alias", func(t *testing.T) {
		for _, bin := range []string{goBin, bashBin} {
			aiEnvDir := setupEnvDir(t)
			mustWrite(t, filepath.Join(aiEnvDir, "environments", "src.yaml"), "skills:\n  - \"*\"\n")
			opts := runOpts{aiEnvDir: aiEnvDir}
			r := runWith(t, bin, opts, "cp", "src", "dst")
			if r.exitCode != 0 {
				t.Fatalf("%s: exit=%d", bin, r.exitCode)
			}
			if _, err := os.Stat(filepath.Join(aiEnvDir, "environments", "dst.yaml")); err != nil {
				t.Errorf("%s: dst missing: %v", bin, err)
			}
		}
	})

	t.Run("compare_output_success", func(t *testing.T) {
		a := setupEnvDir(t)
		b := setupEnvDir(t)
		mustWrite(t, filepath.Join(a, "environments", "src.yaml"), "skills:\n  - \"*\"\n")
		mustWrite(t, filepath.Join(b, "environments", "src.yaml"), "skills:\n  - \"*\"\n")
		diffResult(t,
			runWith(t, goBin, runOpts{aiEnvDir: a}, "clone", "src", "dst"),
			runWith(t, bashBin, runOpts{aiEnvDir: b}, "clone", "src", "dst"),
		)
	})
}

// TestRepoListContract: native `repo list` must match bash across empty (no
// sources file), no-repos-section, single, and multi-repo fixtures. Status
// column depends on whether `repo_dir/.git` exists; we skip the status path
// since neither impl clones, so both emit `(not cloned)`.
func TestRepoListContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

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
	bashBin := filepath.Join(repoRoot(t), "ai-env")

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
	bashBin := filepath.Join(repoRoot(t), "ai-env")

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
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	t.Run("no_store", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "inventory"), runWith(t, bashBin, opts, "inventory"))
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

	t.Run("symlinks_plugin_and_repo", func(t *testing.T) {
		aiEnvDir := setupEnvDir(t)
		store := filepath.Join(aiEnvDir, "skills")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		// Plain local skills: one hyphenated, one without.
		for _, d := range []string{"local-foo", "standalone"} {
			if err := os.MkdirAll(filepath.Join(store, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// A "plugin" symlink: target path contains "mkt/plg" (from sources).
		pluginTarget := filepath.Join(aiEnvDir, "mkt/plg/skills/plug-skill")
		if err := os.MkdirAll(pluginTarget, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(pluginTarget, filepath.Join(store, "plug-skill")); err != nil {
			t.Fatal(err)
		}
		// A "repo" symlink: target path contains "repos/lenny".
		repoTarget := filepath.Join(aiEnvDir, "repos/lenny/skills/lenny-thing")
		if err := os.MkdirAll(repoTarget, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(repoTarget, filepath.Join(store, "lenny-thing")); err != nil {
			t.Fatal(err)
		}
		// An unclassified symlink (no match) -> "plugin" fallback.
		unknownTarget := filepath.Join(aiEnvDir, "somewhere/else/lonely")
		if err := os.MkdirAll(unknownTarget, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(unknownTarget, filepath.Join(store, "lonely")); err != nil {
			t.Fatal(err)
		}

		mustWrite(t, filepath.Join(aiEnvDir, "sources.yaml"),
			"sources:\n"+
				"  - name: \"mysrc\"\n    marketplace: \"mkt\"\n    plugin: \"plg\"\n"+
				"repos:\n"+
				"  - name: \"lenny\"\n    url: \"u\"\n    skills_path: \"skills\"\n    prefix: \"true\"\n")

		opts := runOpts{aiEnvDir: aiEnvDir}
		diffResult(t, runWith(t, goBin, opts, "inventory"), runWith(t, bashBin, opts, "inventory"))
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

// TestHelpContract: `help`, `--help`, `-h` are native — must match bash byte-for-byte.
func TestHelpContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

	for _, flag := range []string{"help", "--help", "-h"} {
		t.Run(strings.TrimLeft(flag, "-"), func(t *testing.T) {
			got := run(t, goBin, flag)
			want := run(t, bashBin, flag)
			diffResult(t, got, want)
		})
	}
}

// TestRepoUpdateContract: native `repo update` must match bash — pull all,
// pull by name, "not found" error when name mismatches, no-repos-section dies.
func TestRepoUpdateContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

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

// TestResetContract: native `reset` must match bash across -f success, -f with
// nothing to clean (idempotent), confirmation 'n' cancels, confirmation 'y'
// proceeds. HOME is overridden to a temp dir so the real user's ~/.agents is
// never touched.
func TestResetContract(t *testing.T) {
	goBin := buildGoBinary(t)
	bashBin := filepath.Join(repoRoot(t), "ai-env")

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
		a := setupEnvDir(t)
		homeA := t.TempDir()
		b := setupEnvDir(t)
		homeB := t.TempDir()
		got := runWith(t, goBin, runOpts{aiEnvDir: a, home: homeA}, "reset", "-f")
		want := runWith(t, bashBin, runOpts{aiEnvDir: b, home: homeB}, "reset", "-f")
		// Bash has a `[[: 0\n0: arithmetic syntax error` quirk when
		// $plugins_to_enable is empty (grep -c . returns "0\n0"); we don't
		// reproduce that noise. Compare stdout + exit only.
		if got.stdout != want.stdout {
			t.Errorf("stdout mismatch\n  go:   %q\n  bash: %q", got.stdout, want.stdout)
		}
		if got.exitCode != want.exitCode {
			t.Errorf("exit code mismatch: go=%d bash=%d", got.exitCode, want.exitCode)
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

	t.Run("force_stdout_parity_seeded", func(t *testing.T) {
		a, homeA := setup(t)
		b, homeB := setup(t)
		got := runWith(t, goBin, runOpts{aiEnvDir: a, home: homeA}, "reset", "-f")
		want := runWith(t, bashBin, runOpts{aiEnvDir: b, home: homeB}, "reset", "-f")
		if got.stdout != want.stdout {
			t.Errorf("stdout mismatch\n  go:   %q\n  bash: %q", got.stdout, want.stdout)
		}
		if got.exitCode != want.exitCode {
			t.Errorf("exit code mismatch: go=%d bash=%d", got.exitCode, want.exitCode)
		}
	})
}
