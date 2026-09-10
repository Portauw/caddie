# caddie Profile Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace caddie's central environment registry with a folder-local `.caddie.yaml` profile that is the single source of truth, and remove the environment concept, the global skill directories and the term "environment" from the codebase and docs.

**Architecture:** `.caddie.yaml` in a working folder holds `name`, `description` and `skills`. Resolution is a walk up from cwd to `/` for the first `.caddie.yaml`; no match is a hard error. Everything central that remains (`sources.yaml`, repo clones, the skill store) is shared infrastructure, not per-folder configuration. caddie writes to exactly two places after this change: `~/.config/caddie` and the folder containing the resolved profile.

**Tech Stack:** Go 1.22, no external dependencies. Hand-rolled YAML scalar/list readers in `internal/config` (no YAML library, deliberately). Standard library `testing`. Contract tests shell out to the built binary.

**Design doc:** `docs/plans/2026-09-10-caddie-profile-design.md`. Read it before starting.

---

## Before you start

**Build and test commands:**

```bash
cd /Users/pieter/Dev/caddie/repo
go build ./...                      # must stay green after every task
go test ./internal/...              # fast unit tests
go test ./tests/contract/           # slow, builds the binary, ~30s
./scripts/build.sh                  # produces dist/caddie
```

**Two rules specific to this repo:**

1. **Go will not compile with dangling references.** Every task must leave `go build ./...` green. This is why the config helpers (`EnvDir`, `EnvFile`, `EnvExists`, `ListEnvs`, `ResolveFromCwd`) are deleted in Task 10, after every caller is gone, and not in Task 1.
2. **`tests/contract/contract_test.go` diffs the Go binary against `ai-env-frozen`**, a frozen bash predecessor at the repo root. It cannot learn about profiles. Parity is kept only for `version`, `repo *` and `inventory`. Every other parity assertion is deleted or converted to a Go-only assertion, as each task specifies.

**Line numbers in this plan are from the pre-change tree and will drift as you work.** Locate functions by name, not by line.

**Vocabulary:** the word is "profile". Not "environment", not "env", not "binding". This applies to identifiers, comments, output strings, error messages and docs.

---

### Task 1: Rename the resolver and test it

The existing `FindProjectConfig` already implements the walk-up. This task renames it into the new vocabulary and puts a test around it before anything depends on it.

**Files:**
- Modify: `internal/config/config.go:140` (`FindProjectConfig`)
- Modify: `cmd/caddie/main.go:1237` (only external caller)
- Create: `internal/config/resolve_test.go`

**Step 1: Write the failing test**

Create `internal/config/resolve_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProfile(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".caddie.yaml")
	if err := os.WriteFile(path, []byte("skills:\n  - \"*\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindProfile(t *testing.T) {
	t.Run("in_cwd", func(t *testing.T) {
		root := t.TempDir()
		want := writeProfile(t, root)
		if got := FindProfile(root); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})

	t.Run("in_ancestor", func(t *testing.T) {
		root := t.TempDir()
		want := writeProfile(t, root)
		deep := filepath.Join(root, "a", "b", "c")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := FindProfile(deep); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})

	t.Run("nearest_wins", func(t *testing.T) {
		root := t.TempDir()
		writeProfile(t, root)
		child := filepath.Join(root, "child")
		want := writeProfile(t, child)
		if got := FindProfile(child); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})

	t.Run("none_found", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "x", "y")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := FindProfile(deep); got != "" {
			t.Errorf("got %q want empty string", got)
		}
	})
}
```

Note on `none_found`: `t.TempDir()` lives under `/var/folders/...` on macOS, so the walk to `/` will not encounter a stray `.caddie.yaml`. If this test ever flakes on a machine with a `.caddie.yaml` in a temp ancestor, that is the reason.

**Step 2: Run it and watch it fail**

```bash
go test ./internal/config/ -run TestFindProfile -v
```

Expected: compile error, `undefined: FindProfile`.

**Step 3: Rename the function**

In `internal/config/config.go`, rename `FindProjectConfig` to `FindProfile` and update its doc comment:

```go
// FindProfile walks from dir up to "/" looking for .caddie.yaml, the caddie
// profile. Returns the path to the nearest one, or "" if there is none.
func FindProfile(dir string) string {
```

The body is unchanged. Update the one caller inside the same file (`ResolveFromCwd`, line 160) and the one in `cmd/caddie/main.go:1237`.

**Step 4: Run the tests**

```bash
go test ./internal/config/ -run TestFindProfile -v
go build ./...
```

Expected: four subtests PASS, build green.

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/resolve_test.go cmd/caddie/main.go
git commit -m "refactor(config): rename FindProjectConfig to FindProfile, add resolution tests"
```

---

### Task 2: Rewrite activate's resolution

The heart of the change. `cmdActivate` currently holds the picker, the `EnvExists` guard, the `directory:` handling and the project-versus-global branch. All of it collapses.

**Files:**
- Modify: `cmd/caddie/main.go:1216` (`cmdActivate`)
- Modify: `tests/contract/contract_test.go:1512` (`TestActivateContract`) and `setupActivateFixture`

**Correction added during execution:** the original plan omitted the contract
tests here. `TestActivateContract` has nine subtests and its
`setupActivateFixture` writes an environment YAML with a `directory:` field,
so this task leaves the suite red unless the fixture and subtests are rewritten
in the same commit. `setupActivateFixture` must instead write `.caddie.yaml`
into the project dir. Subtest mapping: `explicit_env_first_run` becomes a
cwd-based `first_run`; `explicit_env_idempotent_second_run` drops the
positional argument; `cwd_auto_resolve` becomes `walk_up_from_subdir` and
asserts resolution from a nested folder; `unknown_env_name` is replaced by
`positional_arg_rejected`; `no_env_found` becomes `no_profile_found` asserting
exit 1 and the not-found message; `dry_run`, `gitignore_appended_idempotent`,
`no_sources_manifest` and `fingerprint_invalidated_by_skill_change` keep their
coverage but resolve from cwd. The legacy `.ai-env.yaml` dual-writes in these
fixtures exist only for the bash oracle and can go.

**Step 1: Replace the resolution block**

Delete everything from `name := ""` through the `if !config.EnvExists(name)` guard, and the `file` / `displayName` / `directory` / `projectDir` block that follows it. The flag loop stays, minus the positional `name` argument. Replace with:

```go
func cmdActivate(args []string) {
	dryRun := false
	force := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		case "--force", "-f":
			force = true
		default:
			die("Unknown argument: " + a)
		}
	}

	cwd, _ := os.Getwd()
	profile := config.FindProfile(cwd)
	if profile == "" {
		die(fmt.Sprintf("No caddie profile found for %s\n   Run %scaddie init%s to create one.",
			cwd, ansiCyan, ansiReset))
	}

	profileDir := filepath.Dir(profile)
	displayName := config.ReadScalar(profile, "name")
	label := cmp.Or(displayName, filepath.Base(profileDir))

	targetAgents := filepath.Join(profileDir, ".agents", "skills")
	fingerprintFile := filepath.Join(profileDir, ".claude", ".caddie-fingerprint")

	fmt.Printf("%s%s%s %s→ %s%s\n", ansiBold, label, ansiReset, ansiDim, profileDir, ansiReset)

	activateSyncRepos(force)

	patterns := config.ReadList(profile, "skills")
```

The positional argument is now rejected rather than silently ignored, because `caddie activate engineering` must not look like it worked.

**Step 2: Fix the rest of the function**

Three follow-on edits in the same function:

- The dry-run block prints `Environment:` and `Project dir:`. Change to `Profile:` (value `label`) and `Folder:` (value `profileDir`).
- `newFP := skills.ComputeFingerprint(name, matched)` no longer has a `name`. Leave it as `skills.ComputeFingerprint(profileDir, matched)` for now; Task 12 drops the parameter.
- The `if projectDir != ""` / `else` branch at the end collapses to the project-local path only:

```go
	ensureClaudeSkillsSymlink(filepath.Join(profileDir, ".claude", "skills"), targetAgents)
	ensureProjectGitignore(profileDir)
```

Delete the global-clearing lines (`globalAgents`, the `ReconcileSkillDir(globalAgents, nil, store)` call and the `ensureClaudeSkillsSymlink` on `$HOME`).

**Step 3: Build**

```bash
go build ./...
```

Expected: green. `config.ResolveFromCwd` is now called only by `cmdWhich`.

**Step 4: Smoke test by hand**

```bash
cd /tmp && mkdir -p caddie-smoke/deep && cd caddie-smoke
printf 'name: "smoke"\nskills:\n  - "superpowers:*"\n' > .caddie.yaml
go run /Users/pieter/Dev/caddie/repo/cmd/caddie activate --dry-run
cd deep && go run /Users/pieter/Dev/caddie/repo/cmd/caddie activate --dry-run
```

Expected: both print `smoke → /tmp/caddie-smoke` and the same skill count; the second proves the walk-up. Then:

```bash
cd /tmp && go run /Users/pieter/Dev/caddie/repo/cmd/caddie activate --dry-run
```

Expected: exit 1, `No caddie profile found for /tmp`.

Clean up with `rm -rf /tmp/caddie-smoke`.

**Step 5: Commit**

```bash
git add cmd/caddie/main.go
git commit -m "feat(activate): resolve the folder-local profile, drop environments and global mode"
```

---

### Task 3: Rewrite which

**Files:**
- Modify: `cmd/caddie/main.go:728` (`cmdWhich`)
- Modify: `tests/contract/contract_test.go:158` (`TestWhichContract`)

**Step 1: Rewrite the command**

`which` printed an environment name. There is no name to print any more, so it prints the profile path and what it resolves to:

```go
func cmdWhich(_ []string) {
	cwd, err := os.Getwd()
	if err != nil {
		die(err.Error())
	}
	profile := config.FindProfile(cwd)
	if profile == "" {
		fmt.Printf("%s⚠%s  No caddie profile found for %s\n", ansiYellow, ansiReset, cwd)
		os.Exit(1)
	}
	patterns := config.ReadList(profile, "skills")
	matched, _ := resolveMatchedWithSummary(patterns)
	fmt.Println(profile)
	fmt.Printf("  %s%d pattern(s), %d skill(s)%s\n", ansiDim, len(patterns), len(matched), ansiReset)
}
```

**Step 2: Replace the contract test**

`TestWhichContract` has six `diffResult` parity assertions and fixtures built on `environments/`. The frozen bash cannot produce this output. Replace the whole function with Go-only assertions:

```go
// TestWhichContract: `which` prints the resolved profile path. Go-only
// assertions: the frozen bash resolves environments, which no longer exist.
func TestWhichContract(t *testing.T) {
	goBin := buildGoBinary(t)

	t.Run("profile_in_cwd", func(t *testing.T) {
		projectDir := t.TempDir()
		mustWrite(t, filepath.Join(projectDir, ".caddie.yaml"), "skills:\n  - \"*\"\n")
		got := runWith(t, goBin, runOpts{cwd: projectDir, aiEnvDir: t.TempDir()}, "which")
		if got.exitCode != 0 {
			t.Fatalf("exit %d, stderr %q", got.exitCode, got.stderr)
		}
		if !strings.Contains(got.stdout, ".caddie.yaml") {
			t.Errorf("stdout %q does not name the profile file", got.stdout)
		}
	})

	t.Run("no_profile", func(t *testing.T) {
		projectDir := t.TempDir()
		got := runWith(t, goBin, runOpts{cwd: projectDir, aiEnvDir: t.TempDir()}, "which")
		if got.exitCode != 1 {
			t.Errorf("exit code = %d, want 1", got.exitCode)
		}
		if !strings.Contains(got.stdout, "No caddie profile found") {
			t.Errorf("stdout %q missing the not-found message", got.stdout)
		}
	})
}
```

The `active` alias keeps working because it maps to the same handler; no separate test needed.

**Step 3: Run**

```bash
go test ./tests/contract/ -run TestWhichContract -v
```

Expected: two subtests PASS.

**Step 4: Commit**

```bash
git add cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "feat(which): print the resolved profile path"
```

---

### Task 4: Delete list, delete, clone and show

Four commands whose entire reason to exist was managing a central registry.

**Files:**
- Modify: `cmd/caddie/main.go:28` (the `nativeCommands` map)
- Modify: `cmd/caddie/main.go` (delete `cmdList:101`, `cmdDelete:159`, `cmdClone:188`, `cmdShow:745`)
- Modify: `tests/contract/contract_test.go` (delete `TestListContract:264`, `TestDeleteContract:353`, `TestCloneContract:423`, `TestShowContract:912`)

**Step 1: Remove the dispatch entries**

Delete these keys from `nativeCommands`: `list`, `ls`, `delete`, `rm`, `clone`, `cp`, `show`, `info`. They will fall through to the unknown-command error, which is correct.

**Step 2: Delete the four functions and their tests**

Delete the function bodies and the four contract test functions in full.

**Step 3: Build and check for orphans**

```bash
go build ./...
go vet ./...
```

Expected: green. No import becomes unused: `io` survives via `scanOpts.out` and `io.Discard`, `bufio` via `cmdReset` and the new `cmdInit`. If `go vet` does flag one, remove it.

**Step 4: Run the full contract suite**

```bash
go test ./tests/contract/
```

Expected: PASS. The remaining parity tests (`version`, `repo *`, `inventory`) are untouched.

**Step 5: Commit**

```bash
git add cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "feat: remove the central registry commands"
```

---

### Task 5: setup and init swap places

`init` becomes "create a profile here". The old `init` becomes `setup`.

**Files:**
- Modify: `cmd/caddie/main.go:883` (`cmdInit` becomes `cmdSetup`)
- Modify: `cmd/caddie/main.go:799` (`cmdCreate` becomes the new `cmdInit`, rewritten)
- Modify: `cmd/caddie/main.go:28` (dispatch map)
- Modify: `tests/contract/contract_test.go:980` (`TestCreateContract`), `:1057` (`TestInitContract`)

**Step 1: Rename the old init**

Rename `cmdInit` to `cmdSetup`. Change the banner from `Initializing caddie skill profile manager...` to `Setting up caddie...`. Its body is slimmed down in Task 11; leave it alone here beyond the rename.

**Step 2: Replace create with the new init**

Delete `cmdCreate` and write `cmdInit` in its place:

```go
// cmdInit prompts for name, description and skill patterns, then writes
// ./.caddie.yaml. Prompt text is gated on tty so piped-stdin tests still match.
func cmdInit(args []string) {
	if len(args) > 0 {
		die("Usage: caddie init  (creates .caddie.yaml in the current folder)")
	}
	cwd, err := os.Getwd()
	if err != nil {
		die(err.Error())
	}
	target := filepath.Join(cwd, ".caddie.yaml")
	if _, err := os.Stat(target); err == nil {
		die(fmt.Sprintf("A profile already exists here: %s\n   Edit it with %scaddie edit%s.",
			target, ansiCyan, ansiReset))
	}

	base := filepath.Base(cwd)
	fmt.Printf("%sCreating profile in %s%s%s\n\n", ansiBold, ansiCyan, cwd, ansiReset)

	tty := isTerminal(os.Stdin)
	br := bufio.NewReader(os.Stdin)
	readLine := func(prompt string) string {
		if tty {
			fmt.Print(prompt)
		}
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			return ""
		}
		return strings.TrimRight(line, "\n")
	}

	displayName := readLine(fmt.Sprintf("Name [%s]: ", base))
	if displayName == "" {
		displayName = base
	}
	description := readLine("Description: ")

	fmt.Println()
	fmt.Println("Skill patterns (enter one per line, empty line to finish):")
	fmt.Println(`  Examples: "superpowers:*", "gws:gmail-*", "*" (all)`)
	var patterns []string
	for {
		p := readLine("  - ")
		if p == "" {
			break
		}
		patterns = append(patterns, p)
	}
	if len(patterns) == 0 {
		patterns = []string{"*"}
		fmt.Printf("%sℹ%s  Defaulting to all skills (\"*\")\n", ansiBlue, ansiReset)
	}

	var body strings.Builder
	fmt.Fprintf(&body, "name: \"%s\"\n", displayName)
	fmt.Fprintf(&body, "description: \"%s\"\n", description)
	body.WriteString("\n")
	body.WriteString("skills:\n")
	for _, p := range patterns {
		fmt.Fprintf(&body, "  - \"%s\"\n", p)
	}

	if err := os.WriteFile(target, []byte(body.String()), 0o644); err != nil {
		die(err.Error())
	}
	ensureProjectGitignore(cwd)

	fmt.Println()
	fmt.Printf("%s✓%s  Created %s\n", ansiGreen, ansiReset, target)
	fmt.Printf("  Preview:  %scaddie activate --dry-run%s\n", ansiCyan, ansiReset)
	fmt.Printf("  Activate: %scaddie activate%s\n", ansiCyan, ansiReset)
}
```

No `directory:` prompt: the folder is the location. No `agents:` comment block: it documented a schema nothing reads.

**Step 3: Update the dispatch map**

```go
	"init":   cmdInit,
	"setup":  cmdSetup,
```

Remove the `create` and `new` keys.

**Step 4: Rewrite the two contract tests**

Delete `TestCreateContract`. Rewrite `TestInitContract` to cover profile creation, driving the prompts through piped stdin:

```go
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
}
```

**Step 5: Run**

```bash
go test ./tests/contract/ -run 'TestInitContract' -v
go build ./...
```

Expected: two subtests PASS.

**Step 6: Commit**

```bash
git add cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "feat(init): create a profile in the current folder"
```

---

### Task 6: edit operates on the nearest profile

**Files:**
- Modify: `cmd/caddie/main.go:139` (`cmdEdit`)
- Modify: `tests/contract/contract_test.go:319` (`TestEditContract`)

**Step 1: Rewrite**

```go
func cmdEdit(args []string) {
	if len(args) > 0 {
		die("Usage: caddie edit  (opens the nearest .caddie.yaml)")
	}
	cwd, err := os.Getwd()
	if err != nil {
		die(err.Error())
	}
	profile := config.FindProfile(cwd)
	if profile == "" {
		die(fmt.Sprintf("No caddie profile found for %s\n   Run %scaddie init%s to create one.",
			cwd, ansiCyan, ansiReset))
	}

	editor := cmp.Or(os.Getenv("EDITOR"), "vim")
	// `sh -c` preserves $EDITOR's word-splitting (e.g. "code --wait").
	cmd := exec.Command("sh", "-c", editor+` "$0"`, profile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	fmt.Printf("%s✓%s  Updated: %s\n", ansiGreen, ansiReset, profile)
}
```

**Step 2: Convert the contract test to Go-only**

`TestEditContract` currently has two parity assertions. Keep the existing `EDITOR` stub approach (look at how the current test fakes an editor and reuse it verbatim), but drop `bashBin` and `diffResult`, assert exit code 0 and that stdout contains the profile path. Add a subtest asserting exit 1 with "No caddie profile found" in an empty temp dir.

**Step 3: Run**

```bash
go test ./tests/contract/ -run TestEditContract -v
```

**Step 4: Commit**

```bash
git add cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "feat(edit): open the nearest profile instead of a named environment"
```

---

### Task 7: export uses the cwd profile

**Files:**
- Modify: `cmd/caddie/main.go:1554` (`cmdExport`)
- Modify: `tests/contract/contract_test.go:1682` (`TestExportContract`)

**Step 1: Rewrite the argument handling**

Drop the positional `name`. Keep `--to`, `--dry-run`, `--clean`, `--all`. The patterns block becomes:

```go
	if target == "" {
		die("Usage: caddie export [--all] --to <dir-or-s3> [--clean] [--dry-run]")
	}

	var patterns []string
	if all {
		patterns = []string{"*:*"}
	} else {
		cwd, _ := os.Getwd()
		profile := config.FindProfile(cwd)
		if profile == "" {
			die(fmt.Sprintf("No caddie profile found for %s\n   Run %scaddie init%s, or use --all.",
				cwd, ansiCyan, ansiReset))
		}
		patterns = config.ReadList(profile, "skills")
	}
```

Reject any positional argument in the flag loop, as in Task 2, so `caddie export engineering --to x` fails loudly.

**Step 2: Update the contract test**

`TestExportContract` has four parity assertions. Convert to Go-only: assert the exported directory contains the expected skill directories for a `.caddie.yaml` in cwd, and that `--all` still works without a profile. Keep the existing fixture helpers.

**Step 3: Run**

```bash
go test ./tests/contract/ -run TestExportContract -v
```

**Step 4: Commit**

```bash
git add cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "feat(export): export the cwd profile, drop the environment argument"
```

---

### Task 8: reset stops walking environments

**Files:**
- Modify: `cmd/caddie/main.go:619` (`cmdReset`)
- Modify: `tests/contract/contract_test.go:1111` (`TestResetContract`, already Go-only)

**Step 1: Delete the environment walk**

Remove the whole `projectCleaned` block that reads `config.EnvDir()` and cleans each environment's `directory:`. With no registry, caddie does not know which folders to clean, and that is the accepted trade-off in the design.

Update the summary output:
- Remove `Project-local skill symlinks (for all environments with directory: set)` from "Will remove".
- Remove `Environment configs (~/.config/caddie/environments/)` from "Will keep".
- Change the closing hint from `caddie scan && caddie activate <env>` to `caddie scan && caddie activate`.

Leave the `~/.agents/skills` and `~/.claude/skills` cleanup in place for now: Task 11 handles it, and reset is the one command that should still clean them up on an old machine.

**Step 2: Run**

```bash
go test ./tests/contract/ -run TestResetContract -v
```

Expected: PASS, possibly after updating expected output strings in the test.

**Step 3: Commit**

```bash
git add cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "refactor(reset): drop the environment walk"
```

---

### Task 9: move the orphaned-pattern warning into activate

**Files:**
- Modify: `cmd/caddie/main.go:1093` (`runScan`, the trailing env loop)
- Modify: `cmd/caddie/main.go` (`cmdActivate`)

**Step 1: Delete the loop in runScan**

Remove everything from the `// Orphaned-pattern warnings` comment to the end of `runScan`. It reads `config.EnvDir()`, which is about to be deleted.

**Step 2: Add the check to activate**

In `cmdActivate`, after `patterns := config.ReadList(profile, "skills")` and before resolving, warn per pattern:

```go
	if items, err := skills.Scan(); err == nil {
		for _, p := range patterns {
			if p != "" && skills.PatternMatchesIn(items, p) == 0 {
				fmt.Printf("%s⚠%s  Pattern %s\"%s\"%s matches 0 skills — source may have been removed\n",
					ansiYellow, ansiReset, ansiCyan, p, ansiReset)
			}
		}
	}
```

**Step 3: Verify by hand**

```bash
cd /tmp && mkdir -p caddie-orphan && cd caddie-orphan
printf 'skills:\n  - "ghost-source:*"\n' > .caddie.yaml
go run /Users/pieter/Dev/caddie/repo/cmd/caddie activate --dry-run
```

Expected: a warning naming `ghost-source:*`, then a dry-run showing 0 skills. Clean up with `rm -rf /tmp/caddie-orphan`.

**Step 4: Commit**

```bash
git add cmd/caddie/main.go
git commit -m "feat(activate): warn on orphaned patterns where they are actionable"
```

---

### Task 10: delete the environment helpers

Every caller is now gone, so the compiler will confirm this is safe.

**Files:**
- Modify: `internal/config/config.go` (delete `EnvDir:37`, `EnvFile:40`, `EnvExists:43`, `ListEnvs:84`, `ResolveFromCwd:159`)

**Step 1: Delete the five functions**

**Step 2: Build**

```bash
go build ./... && go vet ./...
```

Expected: green. A failure here means a caller was missed in Tasks 2 to 9; fix that caller rather than keeping the helper.

**Step 3: Confirm the vocabulary is gone from Go code**

```bash
grep -rin "environment\|envdir\|envfile" cmd internal --include="*.go"
```

Expected: no hits, or only in `AI_ENV_DIR` handling (Task 13) and in `tests/`. Fix any stragglers in comments and strings now.

**Step 4: Full test run**

```bash
go test ./...
```

**Step 5: Commit**

```bash
git add internal/config/config.go cmd/caddie/main.go
git commit -m "refactor(config): delete the environment helpers"
```

---

### Task 11: remove the global skill directories

**Files:**
- Modify: `internal/skills/sync.go:50` (delete `SweepAgentDir`)
- Modify: `cmd/caddie/main.go` (`cmdSetup`, `runScan:1125`, `cmdReset`)

**Step 1: Delete SweepAgentDir and its caller**

Delete the function from `internal/skills/sync.go`, and in `runScan` delete the `swept := skills.SweepAgentDir(home, log)` call and the `if swept > 0` block that reports it. Remove the now-unused `home` variable if nothing else in `runScan` uses it.

**Step 2: Slim down cmdSetup**

`cmdSetup` keeps: mkdir the config dir and the skill store, create `sources.yaml` if missing, print next steps, run `cmdScan`. Delete:

- the `envDir` mkdir and its output line
- the backup loop over `claudeSkills` and `agentSkills`
- the whole migration loop that moves bare files and dirs into the store
- the `ensureClaudeSkillsSymlink(claudeSkills, agentSkills)` call
- the example environment block that writes `example.yaml`

Add, in their place, the one-time removal of caddie's own global symlink:

```go
	// Global skill dirs are no longer managed. Remove caddie's own symlink,
	// but never touch a real directory the user owns.
	claudeSkills := filepath.Join(home, ".claude", "skills")
	if li, err := os.Lstat(claudeSkills); err == nil && li.Mode()&os.ModeSymlink != 0 {
		if target, _ := os.Readlink(claudeSkills); target == "../.agents/skills" {
			if os.Remove(claudeSkills) == nil {
				fmt.Printf("%sℹ%s  Removed the managed ~/.claude/skills symlink\n", ansiBlue, ansiReset)
			}
		}
	}
```

The `target == "../.agents/skills"` check is the safety catch: a symlink the user made themselves, pointing somewhere else, is left alone.

Update the closing next-steps to `caddie init` rather than `caddie create my-project`, and drop the shell-integration snippet's reference to environments if it has one.

**Step 3: Update the setup contract test**

`TestInitContract` was repurposed in Task 5. Add a `TestSetupContract` asserting that `setup` creates the config dir, the store and `sources.yaml` under a temp `HOME` plus temp `AI_ENV_DIR`, and that it does not create `~/.agents/skills`.

**Step 4: Run**

```bash
go test ./... && go build ./...
```

**Step 5: Commit**

```bash
git add internal/skills/sync.go cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "feat: stop managing global skill directories"
```

---

### Task 12: drop the fingerprint's name parameter

**Files:**
- Modify: `internal/skills/reconcile.go:15` (`ComputeFingerprint`)
- Modify: `cmd/caddie/main.go` (the one call site in `cmdActivate`)

**Step 1: Change the signature**

```go
// ComputeFingerprint returns the SHA-256 of the sorted skill list. The
// fingerprint file lives inside the profile folder, so the profile needs no
// separate identity in the hash.
func ComputeFingerprint(sortedSkills []string) string {
	var b strings.Builder
	for _, s := range sortedSkills {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
```

**Step 2: Update the call site**

`newFP := skills.ComputeFingerprint(matched)`

**Step 3: Note the one-time effect**

Every existing `.caddie-fingerprint` now mismatches, so the next `activate` in each folder reconciles once instead of short-circuiting. That is correct and self-healing; no migration needed. Mention it in the commit body.

**Step 4: Build and test**

```bash
go build ./... && go test ./...
```

**Step 5: Commit**

```bash
git add internal/skills/reconcile.go cmd/caddie/main.go
git commit -m "refactor(skills): drop the name parameter from ComputeFingerprint

The fingerprint file lives in the profile folder, so the profile has no
separate identity to hash. Existing fingerprints mismatch once and
reconcile on the next activate."
```

---

### Task 13: rename AI_ENV_DIR to CADDIE_DIR

**Files:**
- Modify: `internal/config/config.go:32` (`Dir`)
- Modify: `tests/contract/contract_test.go:94` (`runWith`)

**Step 1: Rename the variable**

```go
// Dir returns the caddie config directory: $CADDIE_DIR or ~/.config/caddie.
func Dir() string {
	return cmp.Or(os.Getenv("CADDIE_DIR"), filepath.Join(Home(), ".config", "caddie"))
}
```

No fallback to the old name. This is a hard break, consistent with the rest.

**Step 2: Make the harness export both**

In `runWith`, the frozen bash still needs `AI_ENV_DIR` for the surviving parity tests, and the Go binary now needs `CADDIE_DIR`. Export both from the single `opts.aiEnvDir` field:

```go
	if opts.aiEnvDir != "" {
		// The Go binary reads CADDIE_DIR; the frozen bash only knows
		// AI_ENV_DIR. Export both so parity tests still isolate correctly.
		env = append(env, "CADDIE_DIR="+opts.aiEnvDir, "AI_ENV_DIR="+opts.aiEnvDir)
	}
```

**Step 3: Run the whole suite**

```bash
go test ./tests/contract/
```

Expected: PASS. A failure in a `repo *` or `inventory` test here means the Go binary escaped to the real `~/.config/caddie`; check the export above before anything else.

**Step 4: Commit**

```bash
git add internal/config/config.go tests/contract/contract_test.go
git commit -m "refactor(config): rename AI_ENV_DIR to CADDIE_DIR"
```

---

### Task 14: rewrite help

**Files:**
- Modify: `cmd/caddie/main.go:406` (`cmdHelp`)
- Modify: `tests/contract/contract_test.go:799` (`TestHelpContract`, already Go-only)

**Step 1: Rewrite the text**

The command list must match the new surface exactly: `init`, `setup`, `edit`, `which`, `activate`, `scan`, `export`, `repo`, `inventory`, `reset`, `help`, `--version`. Remove `create`, `list`, `delete`, `clone`, `show`.

The "Files" section changes to:

```
  <folder>/.caddie.yaml                 Profile (source of truth)
  <folder>/.agents/skills/              Managed skill symlinks
  <folder>/.claude/skills               Symlink to .agents/skills
  ~/.config/caddie/sources.yaml         Registered skill repos
  ~/.config/caddie/skills/              Namespaced skill store
```

Drop the `~/.config/caddie/environments/` line and the `environment: "my-project"` example, replacing the latter with a full profile example.

**Step 2: Update the test**

`TestHelpContract` asserts on Go output only. Update its expected substrings to the new command list, and add an assertion that the output does not contain the word "environment".

**Step 3: Run**

```bash
go test ./tests/contract/ -run TestHelpContract -v
```

**Step 4: Commit**

```bash
git add cmd/caddie/main.go tests/contract/contract_test.go
git commit -m "docs(help): rewrite for the profile model"
```

---

### Task 15: documentation and the naming audit

**Files:**
- Modify: `README.md`
- Modify: `docs/OVERVIEW.md`
- Replace: `examples/backend-api.yaml`, `examples/frontend-react.yaml`, `examples/data-pipeline.yaml`
- Delete: `.agents/SOURCES.md`
- Create: `docs/adr/003-profile-replaces-environment.md`

**Step 1: Run the audit and work the list**

```bash
grep -rin "environment\|ai-env\|AI_ENV" README.md docs/ examples/ .agents/ 2>/dev/null
```

Every hit outside `docs/plans/` and `docs/adr/001|002` is in scope. `ai-env-frozen` itself is deliberately untouched.

**Step 2: OVERVIEW.md**

The largest job. Rewrite: the four core concepts (concept 3 "Environments" becomes "Profiles", concept 4 "Bindings" disappears entirely), the mermaid graph's `E` and `P` subgraphs, the activation pipeline's step 1, the mental-model diagram's `.caddie.yaml` caption, and the "day in the life" section, which currently implies a central profile per project.

**Step 3: examples/**

Replace three environment files with one `examples/profile.yaml`:

```yaml
# .caddie.yaml — drop this in the folder you work in.
name: "Backend API"
description: "Python FastAPI service"

skills:
  - "superpowers:*"
  - "itp-eng-backend:*"
  - "gws:gmail-*"
```

The old examples documented `model`, `permission_mode`, `mcp_servers`, `env` and `hooks`. No code has ever read those keys. Do not carry them forward.

**Step 4: Delete .agents/SOURCES.md**

It claims to be auto-generated by `ai-env activate` and points at `~/.config/ai-env/repos`. Nothing generates it. `git rm .agents/SOURCES.md`.

**Step 5: Write ADR 003**

Follow the format of `docs/adr/001-repo-skill-prefix-naming.md`. Record: the decision, the three accepted trade-offs from the design doc (no reuse mechanism, not portable because the file stays gitignored, hard break with no shim), the rejected alternatives (`extends:`, templates, folder registry), and a note that `docs/sharing-session.html` is superseded and not rewritten.

**Step 6: Commit**

```bash
git add README.md docs/ examples/ .agents/
git commit -m "docs: rewrite for the profile model, add ADR 003"
```

---

### Task 16: convert the local bindings

Run once, on Pieter's machine, after everything above is merged. Not shipped code.

**Step 1: Dry run**

```bash
ENVD=~/.config/caddie/environments
find ~/Dev ~/Documents -maxdepth 4 -name .caddie.yaml | while read -r f; do
  env=$(sed -n 's/^environment: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "$f")
  if [ -n "$env" ] && [ -f "$ENVD/$env.yaml" ]; then
    echo "OK   $f <- $env"
  else
    echo "SKIP $f (env='$env')"
  fi
done
```

Expected: 15 lines, all `OK`. Investigate any `SKIP` before continuing.

**Step 2: Convert**

Same loop, with the read replaced by the write:

```bash
    grep -v '^directory:' "$ENVD/$env.yaml" > "$f"
```

**Step 3: Verify one of each shape**

```bash
cat ~/Dev/caddie/.caddie.yaml                      # one-to-one env
cat "~/Documents/ITP Vault/.caddie.yaml"           # shared archetype (engineering)
cd ~/Dev/caddie && caddie activate --dry-run
cd ~/Dev/skills && caddie activate --dry-run
```

Expected: each prints its own profile name and a plausible skill count. The two folders that shared `skills` now hold independent copies, which is the accepted trade-off.

**Step 4: Remove the registry and the home profile**

```bash
rm -rf ~/.config/caddie/environments
rm ~/.caddie.yaml
caddie activate --dry-run
```

Expected: unchanged output. Nothing reads that directory any more.

`~/.caddie.yaml` exists on this machine and contains `environment: "engineering"`.
Left in place it resolves for every folder under `$HOME` that has no profile of
its own, and since it carries no `skills:` key that is a silent zero-skill
activation which also writes `~/.gitignore`, recreates `~/.claude/skills` and
drops a fingerprint in `~/.claude/`. That is global mode resurrected by
accident, which Task 11 exists to remove. Delete it rather than converting it.

Verify afterwards that a folder with no profile fails as designed:

```bash
cd /tmp && caddie activate
```

Expected: exit 1, `No caddie profile found for /tmp`. If it instead reports a
profile, something above `/tmp` still holds a `.caddie.yaml`.

---

## Decisions taken during execution

- **Task 2 landed the "Run `caddie init` to create one" message ahead of the
  command that makes it true.** At that commit `init` still did machine setup
  and `create` still wrote to the central registry, so the hint pointed at
  nothing. Kept as-is deliberately: Task 5 swaps the two commands, and the
  branch is not merged until all 16 tasks land, so no user meets the
  intermediate state. Revisit only if this branch ever needs to ship partially.
- **`~/.caddie.yaml` is deleted, not converted.** See Task 16 step 4.

## Open items to resolve during implementation

- **Non-Claude runtimes reading `~/.agents/skills`.** Check before Task 11. `.agents/` is a cross-tool convention and opencode appears in `docs/plans/`. If something does read it, Task 11 needs a different shape.
- **Whether `setup` should ever run implicitly.** The design leaves this open. The plan does not implement auto-run; `activate` dies with a hint if the config dir is missing. Decide before closing out.
