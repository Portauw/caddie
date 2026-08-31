# `caddie activate` — opencode command wrappers

> **Status:** Proposed 2026-08-20

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Behind an opt-in env-config flag, have `caddie activate` generate one opencode slash-command stub per activated skill, so skills become invocable as `/<skill-name>` in the opencode TUI. Reconciled on every activate, torn down by `caddie reset`.

**Architecture:** Mirrors the existing symlink reconcile. `ReconcileCommandDir` diffs the expected stub set against what is on disk in `<project>/.opencode/command/` and minimally writes/removes. Unlike skills, stubs are *generated files*, not symlinks — a symlinked `SKILL.md` would inject the whole skill body as the prompt template. Ownership is tracked by a marker line, so hand-written commands in the same directory are never touched.

**Tech Stack:** Go (stdlib only — no new deps, consistent with the existing `go.mod`)

---

## Context

opencode discovers skills and commands through two independent mechanisms:

| | Discovery glob | Cost when idle | Invocation |
|---|---|---|---|
| Skill | `skills/**/SKILL.md` | name + description in system prompt | model calls `skill({name})` |
| Command | `{command,commands}/**/*.md` | nothing | user types `/<name>`, file body becomes the prompt |

caddie already satisfies the first: `.agents/skills/` and `.claude/skills/` are both on opencode's discovery path, so activated skills reach opencode today with zero opencode-specific code. What is missing is the second — there is no way to *deliberately* invoke a skill from the TUI. The model decides, or nothing happens.

This plan adds the second mechanism without disturbing the first.

### Constraints discovered by inspecting the opencode binary (v1.18.18)

These were read out of the shipped bundle, not the docs. Re-verify if targeting a much newer version.

1. **Discovery glob is `{command,commands}/**/*.md`** — both spellings work. This plan writes `command/` (singular).

2. **Name derivation strips only the leading directory segment:**
   ```js
   name: relative(dir, file).replaceAll("\\","/")
           .replace(/^(command|commands)\//, "")
           .replace(/\.md$/, "")
   ```
   Nested subdirectories **remain part of the command name**. `command/caddie/ship.md` becomes `/caddie/ship`, not `/ship`. This rules out the obvious isolation strategy of giving caddie its own subdirectory to wipe wholesale. Stubs must be written flat, and ownership tracked in-file.

3. **The TUI fuzzy-filters the command list** (`fuzzysort` and an `fzf` port are both bundled, `has_fuzzy_score` symbol present). A large command list is navigable — typing `/prep` narrows to `prepare-day`, `prepare-meeting`, `prep-refinement`. This is why generating a stub per skill is viable at all.

4. **Frontmatter is decoded against a closed schema** (`decodeUnknownExit`, `errors: "all"`). Do not add custom frontmatter keys as an ownership marker — an unrecognised key risks the file being rejected. Use a body comment instead.

### Collision check (performed 2026-08-20)

Against 432 skills in the store and 185 active in the ITP Vault project:

- Zero collisions with opencode built-ins (`init`, `share`, `new`, `compact`, `export`, `undo`, `redo`, `models`, `themes`, `sessions`, `help`, `exit`, `connect`, `details`, `editor`, `thinking`, `unshare` and their aliases).
- Zero cross-repo collisions.

caddie's repo-prefix scheme (ADR 001) is what buys this. A flat unprefixed namespace would have collided on at least `init` and `release`. Note that opencode lets custom commands silently override built-ins, so a future unprefixed repo could shadow `/init` without warning. Task 5 adds a guard.

### Why generated stubs, not symlinks

The command file's body *is* the prompt template, injected verbatim on every invocation. Symlinking `SKILL.md` into `command/` would drag the full skill body into context each time, destroying the progressive disclosure that makes 400+ skills survivable. The stub is three lines and delegates to the skill tool:

```markdown
---
description: Prepare for a meeting with someone
---
<!-- caddie-generated: do not edit; regenerate with `caddie activate` -->
Use the `sterling-prepare-meeting` skill. $ARGUMENTS
```

### Files involved

- **Modify:** `internal/skills/walk.go` — extract `description` from SKILL.md frontmatter
- **Create:** `internal/skills/commands.go` — `RenderCommandStub`, `ReconcileCommandDir`
- **Modify:** `internal/skills/skills.go` — carry `Description` on the store item
- **Modify:** `cmd/caddie/main.go` — read `commands:` from env yaml, wire into `cmdActivate`, `cmdReset`, `ensureProjectGitignore`, dry-run output, help text
- **Modify:** `README.md` — document the flag

### Out of scope

- Global (non-project) command generation. `~/.config/opencode/command/` is machine-wide and would leak one project's commands into every other. Project-local only; global activate skips command generation entirely.
- Claude Code `.claude/commands/`. Different runtime, different semantics, no demand yet. Do not add speculative support — see the dead `agents:` block for what that produces.

---

### Task 1: Extract skill descriptions during the walk

**Files:**
- Modify: `internal/skills/walk.go`
- Modify: `internal/skills/skills.go`

**Step 1: Add a frontmatter description reader**

Add to `walk.go`. Deliberately narrow: handles the single-line `description: ...` case, which covers the overwhelming majority of skills, and gives up cleanly on folded/multi-line blocks rather than mangling them. `config.ReadScalar` is not reused because frontmatter is delimited by `---` fences and descriptions routinely run past 500 characters.

```go
// readDescription extracts a single-line `description:` from the YAML
// frontmatter of a SKILL.md. Returns "" when the file has no frontmatter, no
// description key, or a multi-line (folded/literal/continued) value that this
// deliberately-minimal parser will not attempt to reassemble.
//
// The result is collapsed to one line and truncated at maxDescLen runes on a
// word boundary, because opencode renders it in a single-line list entry.
func readDescription(skillMD string) string {
	f, err := os.Open(skillMD)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	if !sc.Scan() || strings.TrimRight(sc.Text(), " \t\r") != "---" {
		return ""
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "---" {
			return ""
		}
		if !strings.HasPrefix(line, "description:") {
			continue
		}
		v := strings.TrimLeft(line[len("description:"):], " \t")
		// Folded (>) or literal (|) block scalars, or an empty value with the
		// text on following lines. Not worth parsing; fall back to no description.
		if v == "" || v == ">" || v == "|" ||
			strings.HasPrefix(v, ">") || strings.HasPrefix(v, "|") {
			return ""
		}
		return truncateDesc(config.StripQuotes(v))
	}
	return ""
}

const maxDescLen = 100

// truncateDesc collapses internal whitespace and cuts at maxDescLen runes,
// preferring the last word boundary.
func truncateDesc(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxDescLen {
		return s
	}
	runes := []rune(s)[:maxDescLen]
	cut := string(runes)
	if i := strings.LastIndexByte(cut, ' '); i > maxDescLen/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.") + "…"
}
```

Add imports to `walk.go`: `bufio`, `unicode/utf8`, and `github.com/Portauw/caddie/internal/config`.

> **Import-cycle check:** `internal/skills` already imports `internal/config` (`skills.go:12`), so `config.StripQuotes` is safe to call here. If that ever inverts, inline a local `stripQuotes` instead.

**Step 2: Carry the description on `RepoSkill`**

```go
type RepoSkill struct {
	Name        string
	AbsPath     string
	Description string
}
```

Populate it in `walk`, at the point where the skill is appended (`walk.go:58-61`):

```go
out = append(out, RepoSkill{
	Name:        name,
	AbsPath:     dir,
	Description: readDescription(filepath.Join(dir, "SKILL.md")),
})
```

**Step 3: Expose it on the store item**

`Scan()` in `skills.go` returns the store listing that `resolveMatchedWithSummary` consumes. Add a `Description` field to that item type and populate it by reading through the store symlink: `readDescription(filepath.Join(store, name, "SKILL.md"))`. `os.Stat` follows symlinks, so no resolution step is needed.

> Read `skills.go:1-80` first to match the existing item struct's naming and whether `Scan()` already stats each entry — piggyback on an existing stat rather than adding one.

**Step 4: Verify**

```bash
cd ~/Dev/caddie && go build ./... && go test ./...
```

Add a table test in `internal/skills/walk_test.go` (create if absent) covering: no frontmatter; frontmatter without `description`; single-line quoted; single-line unquoted; folded `>`; literal `|`; a 400-char description (asserts truncation with `…` at a word boundary); non-ASCII description (asserts rune-safe truncation, not byte-safe).

**Step 5: Commit**

```bash
git add internal/skills/
git commit -m "feat(skills): extract description from SKILL.md frontmatter"
```

---

### Task 2: Render and reconcile command stubs

**Files:**
- Create: `internal/skills/commands.go`
- Create: `internal/skills/commands_test.go`

**Step 1: Stub rendering**

```go
// Package-level marker identifying caddie-generated command stubs. Present as
// the first body line (after frontmatter) of every generated file.
//
// Ownership cannot be tracked via a subdirectory: opencode derives the command
// name from the path relative to command/, stripping only the leading segment,
// so command/caddie/ship.md would surface as "/caddie/ship". Nor via a custom
// frontmatter key: the frontmatter is decoded against a closed schema and an
// unknown key risks rejecting the file. Hence an in-body marker.
const commandMarker = "<!-- caddie-generated: do not edit; regenerate with `caddie activate` -->"

// RenderCommandStub returns the full file body for one skill's command wrapper.
// description may be empty, in which case the frontmatter carries a generic
// fallback so the TUI list entry is not blank.
func RenderCommandStub(skillName, description string) string {
	desc := description
	if desc == "" {
		desc = "Run the " + skillName + " skill"
	}
	var b strings.Builder
	b.WriteString("---\ndescription: ")
	b.WriteString(yamlQuote(desc))
	b.WriteString("\n---\n")
	b.WriteString(commandMarker)
	b.WriteString("\nUse the `")
	b.WriteString(skillName)
	b.WriteString("` skill. $ARGUMENTS\n")
	return b.String()
}

// yamlQuote wraps v in double quotes, escaping backslashes and double quotes.
// Descriptions routinely contain ':' and quote characters, which would
// otherwise break the frontmatter parse.
func yamlQuote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(v) + `"`
}
```

> `$ARGUMENTS` expands to the empty string when the user supplies none, leaving a harmless trailing space. Confirm during Task 4 manual testing that a bare `/skill-name` still reads cleanly.

**Step 2: Reconcile**

Deliberately parallel to `ReconcileSkillDir` (`reconcile.go:38`), including the "leave foreign entries alone" rule — here keyed on the marker rather than on symlink-ness.

```go
// CommandStub is one command file to be written.
type CommandStub struct {
	Name        string // skill dirname; becomes the slash-command name
	Description string
}

// ReconcileCommandDir writes one <name>.md per expected stub into targetDir and
// removes caddie-generated stubs that are no longer expected. Files lacking the
// caddie marker are never written over and never removed — hand-written
// commands coexist safely in the same directory.
//
// Files are only rewritten when their content actually differs, so an unchanged
// activate leaves mtimes alone.
func ReconcileCommandDir(targetDir string, expected []CommandStub) (ReconcileResult, error) {
	var res ReconcileResult
	if len(expected) == 0 {
		// Still sweep: the flag may have just been turned off.
		return sweepGeneratedCommands(targetDir, nil)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return res, err
	}

	want := make(map[string]CommandStub, len(expected))
	for _, e := range expected {
		if e.Name == "" {
			continue
		}
		want[e.Name+".md"] = e
	}

	res, err := sweepGeneratedCommands(targetDir, want)
	if err != nil {
		return res, err
	}

	for filename, stub := range want {
		full := filepath.Join(targetDir, filename)
		body := RenderCommandStub(stub.Name, stub.Description)

		if existing, err := os.ReadFile(full); err == nil {
			if string(existing) == body {
				continue
			}
			// Refuse to clobber a file we did not generate.
			if !isGeneratedCommand(existing) {
				continue
			}
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err == nil {
			res.Added++
		}
	}
	return res, nil
}

// sweepGeneratedCommands removes caddie-generated stubs in targetDir whose
// filename is not a key of want. A nil want removes all of them.
func sweepGeneratedCommands(targetDir string, want map[string]CommandStub) (ReconcileResult, error) {
	var res ReconcileResult
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return res, nil // nothing to sweep
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if _, ok := want[e.Name()]; ok {
			continue
		}
		full := filepath.Join(targetDir, e.Name())
		body, err := os.ReadFile(full)
		if err != nil || !isGeneratedCommand(body) {
			continue
		}
		if os.Remove(full) == nil {
			res.Removed++
		}
	}
	return res, nil
}

// isGeneratedCommand reports whether body carries the caddie marker. The marker
// sits on the first line after the frontmatter; scanning the first few hundred
// bytes is sufficient and avoids a false positive on a long hand-written file
// that happens to quote the marker further down.
func isGeneratedCommand(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte(commandMarker))
}
```

**Step 3: Tests**

`commands_test.go`, all against `t.TempDir()`:

1. Empty dir + 3 stubs → 3 files, `Added == 3`.
2. Re-run identical → `Added == 0`, `Removed == 0`, and mtimes unchanged.
3. Drop one from expected → that file removed, `Removed == 1`, others untouched.
4. Hand-written `mine.md` with no marker → survives a reconcile that does not expect it.
5. Hand-written `ship.md` with no marker, while `ship` *is* expected → not overwritten, content preserved.
6. Description containing `"` and `:` → frontmatter round-trips (assert the rendered line parses as `description: "..."`).
7. Empty expected set → all generated stubs removed, hand-written ones survive.
8. `targetDir` does not exist and expected is empty → no error, no directory created.

**Step 4: Commit**

```bash
git add internal/skills/commands.go internal/skills/commands_test.go
git commit -m "feat(skills): add opencode command stub rendering and reconcile"
```

---

### Task 3: Wire into `cmdActivate`

**Files:**
- Modify: `cmd/caddie/main.go`

**Step 1: Read the flag**

The env yaml gains an optional `commands:` list of glob patterns, matched with the same `path.Match` semantics as `skills:` (`skills.go:159`). Absent or empty means the feature is off — no directory is created, nothing is written.

```yaml
name: "ITP Vault"
directory: "~/Documents/ITP Vault"
skills:
  - "sterling:*"
  - "superpowers:*"
commands:
  - "sterling:*"        # only these become /slash-commands
```

To wrap every activated skill: `commands: ["*:*"]`.

In `cmdActivate`, after `patterns := config.ReadList(file, "skills")` (`main.go:1329`):

```go
commandPatterns := config.ReadList(file, "commands")
```

> **Verify before relying on it:** `config.ReadList` terminates its section on the first line starting with neither space nor `#` (`config.go:116`). Confirm with a fixture that a `commands:` block placed *after* the `skills:` block parses correctly, and that the commented-out `agents:` boilerplate written by `caddie create` does not swallow it.

**Step 2: Resolve the command set**

Command patterns are matched against the *already-matched* skill set, so `commands:` can only ever be a subset of `skills:`. A pattern matching an inactive skill is silently ignored — generating a command for an unlinked skill would produce a `/x` that fails at runtime.

Add alongside `resolveMatchedWithSummary`:

```go
// resolveCommandStubs filters the activated skill set by the `commands:`
// patterns and pairs each survivor with its description. Returns nil when no
// patterns are configured.
func resolveCommandStubs(matched []string, patterns []string) []skills.CommandStub
```

Reuse the existing prefix-classification and `path.Match` helpers rather than reimplementing the matching. Sort by name for deterministic output.

**Step 3: Include in the fingerprint**

`ComputeFingerprint(name, matched)` currently hashes env name + skill list. Command stub content depends on the skill *descriptions* too, so a skill whose description changed upstream must invalidate the fingerprint. Extend it:

```go
func ComputeFingerprint(envName string, sortedSkills []string, commandStubs []CommandStub) string
```

Append each `stub.Name + "\t" + stub.Description + "\n"` after the skills block. Update all call sites.

> This changes the hash for every existing user, forcing exactly one full reconcile on upgrade. Acceptable and self-healing. Note it in the release notes.

**Step 4: Wire the reconcile**

Project-local only. Inside the `if projectDir != ""` branch (`main.go:1371`), after `ensureClaudeSkillsSymlink`:

```go
cmdDir := filepath.Join(projectDir, ".opencode", "command")
cres, err := skills.ReconcileCommandDir(cmdDir, commandStubs)
if err != nil {
	fmt.Printf("%s⚠%s  Could not reconcile opencode commands: %v\n", ansiYellow, ansiReset, err)
}
```

In the global `else` branch, sweep only — a project may have been deactivated:

```go
// No global command generation: ~/.config/opencode/command/ is machine-wide
// and would leak one project's commands into every other project.
```

**Step 5: Report and extend dry-run**

Append to the success line when non-zero, matching the existing `changeDesc` style:

```
✓ 185 skills (+2 added), 24 commands
```

In the dry-run block (`main.go:1335-1350`), add the command count, the target directory, and the list of command names under a `Would manage:` entry.

**Step 6: Gitignore**

Add `.opencode/command/` to the `entries` slice in `ensureProjectGitignore` (`main.go:1509`).

> **Caveat to surface in the README:** this ignores the *whole* directory, so a hand-written command placed there would also be ignored. Users who want to version their own commands should place them in `.opencode/commands/` (plural — also discovered, per the glob) and leave the singular directory to caddie. Document this explicitly; it is the one sharp edge in the design.

**Step 7: Verify**

```bash
cd ~/Dev/caddie && go build ./... && go test ./...
```

Then against a real environment:

```bash
caddie activate <env> --dry-run     # shows command count + names, writes nothing
caddie activate <env>
ls "$PROJECT/.opencode/command/" | wc -l
head -5 "$PROJECT/.opencode/command/"<some-skill>.md
```

**Step 8: Commit**

```bash
git add cmd/caddie/main.go internal/skills/
git commit -m "feat(activate): generate opencode command wrappers behind commands: flag"
```

---

### Task 4: Teardown in `cmdReset` and on environment switch

**Files:**
- Modify: `cmd/caddie/main.go`

**Step 1: Per-environment project cleanup**

In the `cmdReset` loop over environments with a `directory:` field (`main.go:685-708`), after the `.claude/skills` symlink removal:

```go
cres, _ := skills.ReconcileCommandDir(filepath.Join(ed, ".opencode", "command"), nil)
commandsCleaned += cres.Removed
```

Report the count alongside the existing `Cleaned %d project-local symlink(s)` line. Only caddie-generated stubs are removed; hand-written commands survive a full reset.

**Step 2: Empty directory cleanup**

After sweeping, remove `.opencode/command/` and `.opencode/` if empty, so a reset does not leave stray directories in a project that never had them. Use `os.Remove` (fails harmlessly on non-empty), never `RemoveAll`.

**Step 3: Environment-switch staleness**

Switching a project from env A (with commands) to env B (without) is already handled: `commandStubs` is nil, `ReconcileCommandDir` sweeps everything generated. Verify explicitly — this is the most likely regression.

**Step 4: Verify**

```bash
caddie activate <env-with-commands>
ls "$PROJECT/.opencode/command/" | wc -l          # non-zero
printf -- '---\ndescription: mine\n---\nhand written\n' > "$PROJECT/.opencode/command/mine.md"

caddie activate <env-without-commands>
ls "$PROJECT/.opencode/command/"                   # only mine.md

caddie reset
ls "$PROJECT/.opencode/command/"                   # still only mine.md
```

**Step 5: Commit**

```bash
git add cmd/caddie/main.go
git commit -m "feat(reset): tear down generated opencode command wrappers"
```

---

### Task 5: Built-in collision guard

**Files:**
- Modify: `cmd/caddie/main.go`

opencode lets custom commands override built-ins silently. A skill named `init` or `share` would shadow `/init` with no warning. No collision exists in the current 432-skill store, but a future repo without a prefix could introduce one.

**Step 1: Warn, do not block**

```go
// opencodeBuiltins are slash commands shipped by opencode. A generated stub
// with a matching name silently overrides the built-in, so warn rather than
// fail — the user may genuinely want the override.
var opencodeBuiltins = map[string]bool{
	"connect": true, "compact": true, "summarize": true, "details": true,
	"editor": true, "exit": true, "quit": true, "q": true, "export": true,
	"help": true, "init": true, "models": true, "new": true, "clear": true,
	"redo": true, "sessions": true, "resume": true, "continue": true,
	"share": true, "themes": true, "thinking": true, "undo": true,
	"unshare": true,
}
```

Emit one grouped warning listing all shadowed names, not one line per collision.

**Step 2: Skip non-directory store entries**

The store can contain stray files — the ITP Vault currently holds a zero-byte macOS `Icon` file in `.agents/skills/`. Skill discovery ignores it because the glob requires `SKILL.md` inside a directory, but a command generator iterating store entries would emit `/Icon`. Skip entries that are not directories (or symlinks resolving to directories) when building the stub set.

**Step 3: Verify**

Create a fixture skill named `init`, activate, confirm the warning fires and the stub is still written.

**Step 4: Commit**

```bash
git add cmd/caddie/main.go
git commit -m "feat(activate): warn on opencode built-in command collisions"
```

---

### Task 6: Documentation

**Files:**
- Modify: `README.md`
- Modify: `cmd/caddie/main.go` (help text)
- Modify: `cmd/caddie/main.go` (`cmdCreate` scaffold)

**Step 1: README**

Document under the environment-config section:

- What `commands:` does and that it defaults to off
- That patterns are a filter over `skills:`, never a superset
- That it is project-local only, and why
- The `.opencode/commands/` (plural) escape hatch for hand-written commands, given that `.opencode/command/` is gitignored
- That stubs are regenerated on every activate and hand-edits are preserved but will drift

**Step 2: Help text**

Add a line to the `ACTIVATE` section noting that `commands:` in the env config generates opencode slash-commands.

**Step 3: `caddie create` scaffold**

Add a *commented* `commands:` example to the generated env file (`main.go:860-864`, `:1002-1005`).

> **Read this before writing the scaffold.** Those two sites already emit a commented `agents:` / `claude:` block that no Go code has ever read — see the "weakest parts" note below. A commented `commands:` example is only acceptable because the parser genuinely handles it when uncommented. Verify that end-to-end before committing, or leave the scaffold alone and document in the README only.

**Step 4: Commit**

```bash
git add README.md cmd/caddie/main.go
git commit -m "docs: document commands: flag for opencode slash-command wrappers"
```

---

## Verification

1. **Off by default:** an env with no `commands:` key produces no `.opencode/` directory at all.
2. **Subset semantics:** `commands: ["sterling:*"]` against `skills: ["sterling:*", "superpowers:*"]` generates only sterling stubs.
3. **Descriptions land:** `head -3 .opencode/command/sterling-prepare-meeting.md` shows a real one-line description, not the `Run the ... skill` fallback.
4. **Invocable:** launch opencode in the project, type `/prep`, confirm fuzzy filtering narrows to the prepare-* commands and that selecting one causes the agent to load the corresponding skill.
5. **Context cost:** confirm the invoked command injects ~3 lines, not the full SKILL.md body.
6. **Idempotent:** two consecutive activates report `0 added, 0 removed` and leave mtimes unchanged.
7. **Fingerprint short-circuit:** an unchanged third activate exits via the `(unchanged)` path without touching `.opencode/`.
8. **Description change invalidates:** edit a skill's frontmatter description, `caddie scan && caddie activate`, confirm the stub is rewritten.
9. **Hand-written survives:** a marker-less `.md` in the same directory survives activate, env switch, and full reset.
10. **Env switch sweeps:** switching to an env without `commands:` removes every generated stub.
11. **Reset is clean:** `caddie reset` leaves no generated stubs and no empty `.opencode/` directory.
12. **Collision warning:** a skill named `init` produces a grouped warning naming it.
13. **No regression:** `go test ./...` passes, including `tests/contract` — the contract suite compares against the frozen bash implementation, which has no concept of commands, so confirm the new code is inert when `commands:` is absent.

---

## Weakest parts of this plan

Recorded so the next editor does not have to rediscover them.

1. **`readDescription` is a fourth hand-rolled YAML parser** in a codebase that already has three, and it gives up on folded scalars. Some skills will fall back to the generic description. The alternative is a YAML dependency in a project that has stayed stdlib-only since inception — not worth it for one field, but revisit if a second frontmatter field is ever needed.

2. **The gitignore entry is blunt.** Ignoring `.opencode/command/` wholesale means a hand-written command placed there is invisible to git while still being protected from caddie. The plural-directory escape hatch works but is non-obvious and depends on the two-spelling glob, which is an opencode implementation detail that could change.

3. **Binary-derived constraints.** The name-derivation rule, the closed frontmatter schema, and the two-spelling glob were all read out of the v1.18.18 bundle. If any changes upstream, Task 2's ownership scheme and Task 3's paths need revisiting. The `description` field and the `{command,commands}` glob are documented publicly; the name-derivation regex and the schema strictness are not.

4. **This is caddie's second per-tool integration.** The first — the `agents:` / `claude:` block scaffolded by `caddie create` and documented in the README — is written but never read by any Go code. Shipping a second half-wired tool integration alongside a dead one invites a third. Either delete the `agents:` scaffold in this pass or file an issue to do so.

5. **`default_environment` remains a live regression.** Documented in the help text (`main.go:484`), functional in `ai-env-frozen:172,196`, silently dropped in the Go port. This plan touches `config.go` for the `commands:` list; fixing it in the same pass costs almost nothing and removes a documented-but-false claim.
