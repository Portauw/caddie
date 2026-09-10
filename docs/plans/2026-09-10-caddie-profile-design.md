# Design: `.caddie.yaml` becomes the profile, environments are removed

- **Date:** 2026-09-10
- **Status:** Designed, not implemented
- **Supersedes:** the central environment registry introduced with `ai-env`

## Problem

caddie keeps skill profiles centrally in `~/.config/caddie/environments/<name>.yaml`.
A project folder holds a `.caddie.yaml` that is nothing more than a pointer:

```yaml
environment: "caddie"
```

Two files, in two places, for one decision. The config you care about while working
in a folder does not live in that folder, and the pointer is meaningless on its own.

## Decision

`.caddie.yaml` becomes the profile itself. The environment concept, the central
registry and the term "environment" are removed. The folder you work in is the
source of truth.

### What is removed

| Removed | Note |
|---|---|
| The environment concept and the term | Replaced by "profile" |
| `~/.config/caddie/environments/` | No central registry, no runtime lookup |
| The `directory:` field | The profile's own location is the target |
| The interactive environment picker | Nothing central left to pick from |
| Global activation mode | No profile means no activation |
| `~/.agents/skills` and `~/.claude/skills` | See "Global directories" below |
| `SweepAgentDir` | Its inbox is gone; unused in practice |

### What stays central

`sources.yaml`, the repo clones under `~/.config/caddie/repos/`, the namespaced
skill store at `~/.config/caddie/skills/`, and `export`. Those are shared
infrastructure, not per-folder decisions, and they are unaffected.

## The profile

`.caddie.yaml`, in the folder you work in:

```yaml
name: "caddie"
description: "Caddie CLI development"
skills:
  - "superpowers:*"
  - "sterling:*"
  - "itp-engineering:*"
```

Three fields. `skills` drives everything; `name` and `description` are display
only. There is no `directory:`, and no `agents:` block: the commented
`model` / `permission_mode` / `system_prompt_file` / `mcp_servers` / `hooks`
example that current environments carry is not read by any code and does not
survive this change.

## Resolution

Replaces `config.ResolveFromCwd`:

1. Walk up from cwd to `/` looking for `.caddie.yaml`. First hit wins.
2. Nothing found: exit 1 with `No caddie profile found. Run 'caddie init' to create one.`
3. `profileDir` is the folder containing that file.
4. Target is `profileDir/.agents/skills`, with `profileDir/.claude/skills` symlinked to it.

Directory nesting is now the only inheritance mechanism, and it already works:
`~/Documents/ITP Vault/.caddie.yaml` covers the subtree, and
`~/Documents/ITP Vault/Personal/.caddie.yaml` overrides it for that branch.

A `.caddie.yaml` placed in `$HOME` would be found by the walk-up and act as a
catch-all. This is an emergent property of the algorithm, not a documented feature.

## Command surface

| Today | After |
|---|---|
| `init` (machine setup) | `setup`, same job, also auto-run when `~/.config/caddie` is missing |
| `create <name>` | `init`, writes `./.caddie.yaml`, refuses if one exists |
| `list` / `ls` | removed |
| `edit <name>` | `edit`, opens the nearest `.caddie.yaml` in `$EDITOR` |
| `delete <name>` | removed, it is `rm .caddie.yaml` |
| `clone <src> <dest>` | removed |
| `show <name>` | removed, folded into `activate --dry-run` |
| `which` | profile path, pattern count, resolved skill count |
| `activate [name]` | `activate`, no positional arg; `--dry-run` and `--force` stay |
| `export <env> --to` | `export --to` uses the cwd profile; `--all` unchanged |
| `reset` | store, sources and state only; no environment walk |
| `scan` | no per-environment orphaned-pattern warnings |
| `repo`, `inventory`, `source`, `version`, `help` | unchanged |

`caddie init` prompts for display name (default: folder basename), description,
then skill patterns one per line, reusing the loop `create` has today.

### Orphaned patterns

`scan` currently loops every environment and warns when a pattern matches zero
skills. With no registry there is nothing to loop. The check moves into
`activate` and warns only for the profile being activated, which is where the
warning is actionable.

### Gitignore

`.caddie.yaml` stays gitignored. `ensureProjectGitignore` already maintains a
managed block for `.agents/skills` and `.claude/skills`; `.caddie.yaml` is added
to that block so a new profile is ignored in its own repo without anyone
remembering to do it.

## Global directories

`~/.agents/skills` and `~/.claude/skills` are removed entirely. Only the
project-level `<proj>/.agents/skills` remains, and it is unchanged.

Evidence for removing rather than keeping an empty inbox:

- Both directories are empty today.
- The store holds 460 entries and every one is a symlink into a repo clone.
  `SweepAgentDir`, the "drop a folder in and caddie adopts it" path, has never
  produced a surviving skill.
- Global activation is already being deleted by the resolution change above.

`setup` deletes `~/.claude/skills` only when it is caddie's own symlink to
`../.agents/skills`. If it is a real directory with content, it is left alone.

Non-repo skills are still possible: create the directory directly in the store
at `~/.config/caddie/skills/<name>/`. `CleanBrokenStoreLinks` prunes broken
symlinks only, so a real directory there survives scans.

The invariant this buys: caddie writes to exactly two places, its own config
directory and the folder you are standing in.

## Implementation notes

### `internal/config`

Delete `EnvDir`, `EnvFile`, `EnvExists`, `ListEnvs`, and the `directory:`
longest-prefix scan inside `ResolveFromCwd`. `FindProjectConfig` becomes the
whole resolver. `ReadScalar`, `ReadList`, `StripQuotes`, `Home`, `ExpandTilde`
and `Dir` are unchanged, so no YAML dependency is introduced.

While here: `AI_ENV_DIR` is a leftover from the pre-caddie name (commit b0af6d7
renamed the tool but not the variable). Rename to `CADDIE_DIR` and drop the old
name, consistent with the hard break.

### `cmd/caddie/main.go`

- Delete `cmdList`, `cmdDelete`, `cmdClone`, `cmdShow`.
- Rename `cmdInit` to `cmdSetup` and strip it down to: mkdir config dir and
  store, create `sources.yaml` if missing, remove the caddie-owned
  `~/.claude/skills` symlink, run scan. The backup block, the migration block
  and the home symlink setup all go.
- Rename `cmdCreate` to `cmdInit`, rewritten to write `./.caddie.yaml`.
- Rewrite the first ~70 lines of `cmdActivate`: the picker, the `EnvExists`
  guard, the `directory:` handling and the project-versus-global branch collapse
  into "find the file or die".
- `cmdExport` and `cmdReset` lose their environment arguments.
- `runScan` loses the orphan loop.
- `cmdHelp` rewritten.

Expect roughly 200 to 250 lines removed and 60 added, before docs.

### `internal/skills`

- Delete `SweepAgentDir`.
- `ComputeFingerprint(name, matched)` takes the environment name today. The
  fingerprint already lives inside the profile folder, so the name adds nothing.
  Drop the parameter.

### Tests

Corrected after review: `internal/skills/walk_test.go` is not the only test file.
`tests/contract/contract_test.go` is 66KB, 21 test functions and 43 parity
assertions that run the Go binary and the frozen `ai-env-frozen` bash against
identical fixtures, diffing stdout, stderr and exit code byte-for-byte.

27 of those 43 parity assertions cover commands this change alters or deletes:
`which` (6), `show` (4), `list` (4), `export` (4), `clone` (3), `edit` (2),
`delete` (2), `create` (2). The frozen bash cannot learn about profiles, so
those assertions cannot survive.

`activate`, `scan`, `init`, `reset` and `help` already assert against Go output
only, with no `diffResult` call. The most complex commands are already free of
the oracle.

**Decision:** keep the oracle for the commands this change does not touch
(`version`, `repo *`, `inventory`), delete the test functions for removed
commands, and convert `which`, `edit`, `export` and `create` to Go-only
assertions. The `CADDIE_DIR` rename therefore requires the harness to export
both `AI_ENV_DIR` and `CADDIE_DIR`, since the frozen bash only understands the
old name and would otherwise run the Go binary against the real
`~/.config/caddie`.

New unit test: a table test for resolution covering profile in cwd, profile in
an ancestor, nested profiles where the nearest wins, and no profile anywhere.

## Trade-offs accepted

**No reuse mechanism.** Today 9 of 15 bindings share an archetype: `skills` in 3
folders, `engineering`, `content` and `sterling` in 2 each. After this change
those patterns are duplicated per folder. Adding a source prefix to "engineering"
becomes an edit in every folder that used it, and there is no longer a command
that tells you which folders those are. `extends:`, templates and a folder
registry were all considered and rejected in favour of the simpler model.
Directory nesting covers the cases that share an ancestor; cross-tree cases
duplicate.

**Not portable.** `.caddie.yaml` stays gitignored, so nothing travels to
teammates. The win being bought is the removal of central indirection, not
shareability. If that changes later, committing the file is a one-line decision.

**Hard break, no migration code and no compatibility shim.** An old
`.caddie.yaml` containing `environment:` produces a profile with no `skills`
key, so activate resolves zero skills. Anyone else running caddie converts by
hand. Accepted deliberately to avoid carrying both models at once.

## Documentation and naming audit

The rename is not confined to code. Run the audit rather than scanning by eye:

```
grep -rin "environment\|\benv\b" --include="*.go" --include="*.md" --include="*.yaml" .
```

- `README.md`: concepts, quick start, command table.
- `docs/OVERVIEW.md`: the bigger job. Concepts 3 and 4, the mermaid graph, the
  activation pipeline diagram, the mental-model picture and "a day in the life"
  all encode the central registry.
- `examples/*.yaml`: worse than stale. `backend-api.yaml` documents `model`,
  `permission_mode`, `mcp_servers`, `env` and `hooks`, none of which any code
  reads and none of which exist in the new schema. Replace with one or two
  small profile examples.
- `.agents/SOURCES.md`: still says "auto-generated by `ai-env activate`" and
  points at `~/.config/ai-env/repos`. Nothing generates it any more. Delete.
- `docs/sharing-session.html`: 28KB deck built around environments. Out of scope
  to rewrite; note in the ADR that it is superseded.
- New `docs/adr/003-profile-replaces-environment.md`, recording the reuse
  trade-off explicitly, since that is the decision most likely to be questioned
  later.

## Work order

1. Resolver in `internal/config`, plus tests.
2. Command surface in `cmd/caddie/main.go`.
3. Global directory removal and `SweepAgentDir` deletion.
4. Docs, examples, ADR 003.
5. Convert the local bindings last, so activate keeps working during the work.

## Local conversion, run once

Not shipped. Print first, then write:

```bash
ENVD=~/.config/caddie/environments
find ~/Dev ~/Documents -maxdepth 4 -name .caddie.yaml | while read -r f; do
  env=$(sed -n 's/^environment: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "$f")
  if [ -n "$env" ] && [ -f "$ENVD/$env.yaml" ]; then
    grep -v '^directory:' "$ENVD/$env.yaml" > "$f"
    echo "OK   $f <- $env"
  else
    echo "SKIP $f (env='$env')"
  fi
done
```

Then `rm -rf ~/.config/caddie/environments`.

## Open items

- Whether any non-Claude runtime reads `~/.agents/skills`. `.agents/` exists as a
  cross-tool convention and opencode appears in the plans folder. Verify before
  step 3.
- Whether `caddie setup` should ever run implicitly, or only print a hint when
  the config directory is missing.
