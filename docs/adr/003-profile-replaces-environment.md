| **Status**  | DECIDED                              |
|-------------|---------------------------------------|
| **Date**    | 2026-09-11                            |
| **Owner**   | Pieter Portauw                        |
| **Driver**  | Folder-local profiles (Go rewrite)    |

## Context and Problem Statement

The bash implementation (`ai-env-frozen`) stored named profiles centrally at `~/.config/caddie/environments/<name>.yaml`. A project's `.caddie.yaml` held a single pointer line, `environment: "backend-api"`, and `caddie activate` looked the name up in the central registry before resolving skills.

This meant a profile's actual content, its skill list, lived somewhere other than the project it applied to. Auditing a real setup meant opening two files: the project's `.caddie.yaml` to find the name, then the central environments directory to find what that name meant. It also meant the registry accreted entries for projects long gone, with no way to tell from the project folder alone whether its pointer still resolved to anything.

The Go rewrite (this repo) collapses the two files into one: `.caddie.yaml`, placed directly in the project folder, now holds `name`, `description`, and `skills` itself. There is no `environment:` pointer and no central environments directory. `caddie activate` walks up from the current directory to the nearest `.caddie.yaml` and resolves its `skills` patterns directly; nothing else is consulted.

## Decision

A profile is a `.caddie.yaml` file in the folder it applies to. It is not a reference to a profile, it is the profile. `caddie init` creates one, `caddie edit` opens the nearest one, `caddie which` prints the nearest one's path, `caddie activate` and `caddie export` resolve the nearest one's `skills` list against the canonical store.

This is a deliberate collapse of the old two-file model (pointer + registry entry) into one file, at the cost of the three trade-offs below.

### Trade-off 1: no reuse mechanism across folders

Under the old model, one environment file could be pointed at by many projects' `.caddie.yaml` files. Under the new model, each folder's `.caddie.yaml` is its own copy; there is no way to share a skill list across folders except copying the file by hand.

This is a real cost, not a hypothetical one. A survey of existing project bindings found 9 of 15 sharing what amounts to the same archetype (near-identical `skills` lists), and specific patterns repeated across folders: `skills` appearing in 3 folders' lists, and `engineering`, `content`, and `sterling` each appearing in 2. None of that repetition is expressed in the new model; it is duplicated by hand into each `.caddie.yaml`.

Three ways to keep some form of reuse were considered and rejected for this iteration (see Alternatives).

### Trade-off 2: profiles are not portable

`.caddie.yaml` is added to the project's `.gitignore` by `caddie` itself (see `ensureProjectGitignore` in `cmd/caddie/main.go`), alongside the generated `.agents/skills/` and `.claude/skills` symlink trees. This is consistent with treating it as local, machine-specific configuration rather than project source, but it means cloning the repo, or onboarding a teammate, does not bring the skill selection with it. Each checkout needs its own `caddie init`.

The old model was not meaningfully more portable in practice (the central environments directory was never checked into the project repo either), but the file's location invited the assumption that it could be. The new model makes the non-portability explicit: there is no code path that reads a profile from anywhere but the local filesystem.

### Trade-off 3: hard break, no migration path

There is no migration code for old `environment:`-style `.caddie.yaml` files. A project still carrying `environment: "backend-api"` and no `skills:` key resolves zero skills under the new code, silently, because `ReadList` finds no `skills:` section and `resolveMatchedWithSummary` has nothing to match. `caddie activate` will report an empty activation rather than an error pointing at the old format.

This was accepted rather than fixed because the population of affected projects is small and known (this repo's own project bindings), and because writing a one-time converter for a format being deleted outright was judged not worth the maintenance surface. Anyone hitting this reruns `caddie init`.

## Alternatives considered

### `extends:` key pointing at a shared base profile
A `.caddie.yaml` could declare `extends: ~/some/shared-profile.yaml` and merge its own `skills` on top. Rejected for this iteration: it reintroduces exactly the two-file indirection this change removes, just with a user-chosen path instead of a name looked up in a fixed registry, and reopens the question of what happens when the extended file moves or is deleted.

### Templates (`caddie init --template <name>`)
Ship a small set of built-in skill-list templates that `caddie init` can copy from. Rejected for now: it solves the "same archetype 9 times" duplication at creation time but does nothing for a template that changes later, drifting profiles apart again on the next edit. Worth revisiting if a small number of stable archetypes stabilize.

### Folder registry (map of directory to profile)
Keep a central file mapping directories to profiles, but store the profile content there too, in effect the old model with the pointer removed. Rejected: it does not remove the two-file problem, it only removes the two-file's second name.

## Consequences

- `caddie activate`, `caddie export`, `caddie edit`, and `caddie which` all resolve the nearest `.caddie.yaml` by walking up from the current directory; there is no other resolution path and no fallback profile.
- Skill-list duplication across folders with similar purposes is now a known, accepted cost until a reuse mechanism (templates or otherwise) is revisited.
- `docs/sharing-session.html`, the ~28KB slide deck used to introduce the old repos/index/environments/bindings model, is now superseded by this document and by the rewritten `README.md` and `docs/OVERVIEW.md`. It has not been rewritten and should not be treated as current; it is kept only as a historical artifact of the earlier design.
- Old `environment:`-style `.caddie.yaml` files resolve to zero active skills under the current code, with no warning distinguishing that case from a deliberately empty `skills:` list.
