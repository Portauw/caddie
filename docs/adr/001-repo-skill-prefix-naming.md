| **Status**  | DECIDED                          |
|-------------|----------------------------------|
| **Date**    | 2026-04-14                       |
| **Owner**   | Pieter Portauw                   |
| **Driver**  | Collision avoidance across repos  |

## Context and Problem Statement

ai-env aggregates skills from multiple sources (marketplace plugins and git repos) into a flat canonical store (`~/.config/ai-env/skills/`). Both Claude Code and OpenCode discover skills by scanning folder names in this flat directory — nested subdirectories are not supported.

When two repos contain a skill with the same folder name (e.g. both `lenny` and `sterling-skills` could have a `brainstorming/` folder), the last one scanned silently overwrites the first. There is no collision warning or namespacing mechanism.

## Decision

Repos can declare an optional `prefix` field in `sources.yaml`. When set, ai-env prepends `{prefix}-` to each skill folder name during scan, unless the skill already starts with that prefix.

```yaml
repos:
  - name: "sterling-skills"
    url: "https://github.com/Portauw/sterling-skills.git"
    skills_path: "skills"
    prefix: "sterling"
```

A skill folder `prepare-meeting/` in the repo becomes `sterling-prepare-meeting/` in the store and agent dirs.

### Prefix rules

| Config | Behavior |
|---|---|
| No `prefix` field | No prefix — skill folder names used as-is |
| `prefix: "<value>"` | Skills prefixed with `<value>-` |
| Skill already starts with prefix | No double-prefix applied |

### Name resolution

| Layer | Uses |
|---|---|
| Folder name (after prefix) | Discovery, routing, invocation by agents |
| SKILL.md `name` frontmatter | Display metadata only — not used for discovery |

This means the folder name and the `name` field in SKILL.md will diverge when a prefix is applied. This is intentional and does not break functionality in Claude Code or OpenCode.

### Environment pattern matching

ai-env's `_get_prefix()` resolves the logical source by tracing the symlink target back to the repo path. Environment patterns use `{repo-name}:{folder-name}` format:

```yaml
skills:
  - "sterling-skills:*"   # matches sterling-prepare-meeting, sterling-add-person, etc.
  - "lenny:*"             # matches lenny-ai-evals, lenny-brainstorming, etc.
```

## Current configuration

```yaml
repos:
  - name: "lenny"
    prefix: "lenny"       # lenny-ai-evals, lenny-brainstorming, ...
  - name: "sterling-skills"
    prefix: "sterling"    # sterling-prepare-meeting, sterling-add-person, ...
  - name: "gws"
    # no prefix — skills already named gws-calendar, gws-docs, etc.
```

## Alternatives considered

### Nested folders per source
Organize skills in subdirectories: `~/.claude/skills/sterling/prepare-meeting/`. Rejected — Claude Code and OpenCode only scan top-level directories. Nested skills are not discovered.

### Mandatory prefix for all repos
Always prefix with repo name. Rejected — repos like `gws` already have prefixed folder names (`gws-calendar`), which would produce ugly double-prefixes (`gws-gws-calendar`). Opt-in is better.

### No prefix, rely on unique naming
Trust that repos won't collide. Rejected — not sustainable as the number of repos grows. Silent overwrites are a data loss risk.

## Consequences

- Skill invocation changes when a prefix is added: `/write-as-pieter` becomes `/sterling-write-as-pieter`
- The `name` field in SKILL.md stays unchanged in the source repo — the prefix is applied at the ai-env layer only
- The `scan --force` command must be paired with a clean of stale symlinks when prefix config changes, or counts inflate from orphaned entries
