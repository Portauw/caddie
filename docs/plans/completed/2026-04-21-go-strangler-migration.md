# Go Strangler Migration

> **Status: COMPLETED 2026-04-21.** All subcommands run native Go. The
> bash script is retained as `ai-env-frozen` at the repo root and as
> `internal/legacy/ai-env-legacy.sh` (embedded but unused) — kept as the
> parity oracle for contract tests and an emergency rollback path. During
> the port, plugin sources were dropped in favor of git repos as the sole
> skill origin, and `.agents/SOURCES.md` generation was removed.

Incrementally replace the 2782-line `ai-env` bash script with a Go implementation, one subcommand at a time, without breaking the current `cp ai-env /usr/local/bin/ai-env` install UX.

## Architecture

```
cmd/ai-env/main.go            Entry point + dispatcher
internal/
  version/version.go          Single source of truth for VERSION
  legacy/legacy.go            Embeds bash script, extracts & execs on fallthrough
  legacy/ai-env-legacy.sh     Build artifact (refreshed by scripts/build.sh)
scripts/build.sh              Refresh embed + go build -o dist/ai-env
tests/contract/               Diff-tests: Go binary vs frozen bash
ai-env                        FROZEN bash script (do not edit)
dist/ai-env                   Compiled Go binary (gitignored)
```

The Go binary is the entry point. Subcommands registered in `nativeCommands` run Go handlers; anything else falls through to the embedded bash via `legacy.Exec`. The user sees one binary — the transition is invisible.

## Ground rules

- **Bash is frozen.** No edits to `./ai-env` during migration. Open bugs but don't fix in bash.
- **Contract tests before porting.** Each subcommand gets a test in `tests/contract/` that runs both implementations against a shared `AI_ENV_DIR` tmpdir and diffs stdout/stderr/exit code/filesystem state.
- **No behavior changes during port.** Byte-identical output (including ANSI codes, prefixes, error messages). Improvements happen in a separate PR after the port lands.
- **Shared state compatibility.** YAML schema, symlink layout, and `compute_fingerprint` algorithm must round-trip between bash and Go — users will mid-migrate.

## Port order (easiest → hardest)

1. `--version`, `-v` — done in this PR (proves the scaffold).
2. `help`, `--help`, `-h` — currently delegates to legacy; port after scaffold is proven.
3. `which` / `active` — read-only.
4. `list` / `ls`, `show` / `info` — read-only.
5. `source` (list/add/remove) — first YAML CRUD; drops embedded `python3`.
6. `create`, `clone`, `delete`, `reset`, `edit` — env file CRUD.
7. `repo` (list/add/remove/update) — git ops (`go-git`).
8. `init` — symlinks + gitignore.
9. `inventory` — isolated.
10. `export` — S3 via `aws-sdk-go-v2`.
11. `scan` — 430 LOC, self-contained.
12. `activate` — 225 LOC, most coupled. Port last.

## Risks

- **Fingerprint drift**: if Go computes fingerprints differently, `activate` thrashes. Port `compute_fingerprint` as a pure function first and golden-test against bash output on a large corpus.
- **YAML formatting drift**: Go YAML libs reorder keys / change quoting. Use `gopkg.in/yaml.v3` node mode to preserve structure.
- **Scope creep**: resist "fixing" bugs while porting. Behavior-preserving first; improvements second.

## Scaffold checklist (this PR)

- [x] `go.mod` (module `github.com/Portauw/ai-env`, go 1.22)
- [x] `cmd/ai-env/main.go` — dispatcher
- [x] `internal/legacy/legacy.go` — embed + extract + exec
- [x] `internal/version/version.go`
- [x] `scripts/build.sh`
- [x] `tests/contract/contract_test.go` — `--version` native, `help` fallthrough
- [x] `.gitignore` — `dist/`, `internal/legacy/ai-env-legacy.sh`

## Verification

Requires Go toolchain locally (`brew install go`). Then:

```bash
./scripts/build.sh
./dist/ai-env --version        # native Go
./dist/ai-env help             # falls through to embedded bash
cd tests/contract && go test   # contract tests pass
```
