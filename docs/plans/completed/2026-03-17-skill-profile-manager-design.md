# ai-env — Skill Profile Manager Design

## Problem

AI coding agents (Claude Code, Codex, etc.) each have their own skill/plugin systems. Skills come from multiple sources — marketplace plugins, npm packages, GitHub repos, local files. There's no unified way to:

1. See all available skills across sources
2. Control which skills are active per project/context
3. Share the same skill library across agents

## Solution

Redesign ai-env from a Claude-specific launcher into an **agent-agnostic skill profile manager**. It manages a canonical skill store, discovers skills from multiple sources, and configures agent-specific skill directories via symlinks.

## Architecture

```
Sources (read-only, scanned)          Canonical Store              Agent Views (managed by ai-env)
─────────────────────────────         ──────────────               ──────────────────────────────
~/.claude/plugins/cache/*/skills/*    ~/.agents/skills/            ~/.claude/skills/ (symlinks)
~/.agents/skills/*                        gws-drive/               ~/.codex/ (future)
                                          gws-gmail/
                                          brainstorming/
                                          writing-plans/
                                          feature-analyst/
                                          ...
```

### Data flow on `ai-env activate`

1. Scan all registered sources + `~/.agents/skills/`
2. Sync discovered skills into `~/.agents/skills/` (canonical store)
3. Read environment config → resolve skill patterns (wildcards)
4. Rebuild `~/.claude/skills/` with filtered symlinks
5. Print summary

## Skill Namespacing

Skills are namespaced by source prefix, derived automatically:

| Directory name | Prefix | Full identifier |
|---|---|---|
| `gws-drive` | `gws` | `gws:drive` |
| `gws-gmail-send` | `gws` | `gws:gmail-send` |
| `recipe-save-email-attachments` | `recipe` | `recipe:save-email-attachments` |
| `persona-exec-assistant` | `persona` | `persona:exec-assistant` |
| `feature-analyst` | `local` | `local:feature-analyst` |

For `~/.agents/skills/`, prefix is auto-derived from the first segment before `-`. No dash = `local` prefix.

For registered plugin sources, the source `name` is the prefix (e.g., `superpowers`, `itp`).

## Source Registration

`~/.agents/skills/` is always scanned implicitly — never needs to be listed.

Plugin sources are registered explicitly in `~/.config/ai-env/sources.yaml`:

```yaml
sources:
  - name: superpowers
    path: "~/.claude/plugins/cache/superpowers-dev/superpowers/*/skills/*"
  - name: itp
    path: "~/.claude/plugins/cache/itp-engineering-backend/*/skills/*"
```

### Commands

```bash
ai-env source list              # show registered sources
ai-env source add <name> <glob> # register new source
ai-env source remove <name>     # unregister
```

## Skill Matching

Environment configs use **include-only** patterns with glob wildcards:

```yaml
skills:
  - "superpowers:*"              # all superpowers skills
  - "gws:gmail*"                 # gws-gmail, gws-gmail-send, gws-gmail-triage
  - "gws:calendar*"             # gws-calendar, gws-calendar-agenda
  - "local:*"                    # all hand-written skills
  - "recipe:*-email-*"           # email-related recipes
```

**Rules:**
- Include-only — no exclude mechanism
- `"*"` matches all discovered skills
- No skills section = no skills activated
- Patterns union (order doesn't matter)

## Environment Config

```yaml
# ~/.config/ai-env/environments/springer-nature.yaml

name: "Springer Nature"
description: "Backend Kotlin services for NRA platform"
directory: "~/Dev/ITP-springer-nature-repos"

skills:
  - "superpowers:*"
  - "itp:*"
  - "gws:gmail*"
  - "gws:calendar*"
  - "local:*"

agents:
  claude:
    model: "claude-sonnet-4-6"
    permission_mode: "plan"
    system_prompt_file: "./CLAUDE.md"
```

Agent-specific settings live under `agents:`. This makes it explicit that `model` and `permission_mode` are Claude concepts, not universal.

## Commands

```
ai-env init                        # backup ~/.claude/skills/, migrate to managed, register default sources
ai-env scan                        # discover all skills from sources, sync to ~/.agents/skills/
ai-env inventory                   # list all discovered skills with source prefix
ai-env create <name>               # create new environment config
ai-env list                        # list environments
ai-env show <name>                 # show environment config + resolved skill list
ai-env edit <name>                 # open config in $EDITOR
ai-env activate <name>             # scan + rebuild symlinks + print summary
ai-env activate <name> --dry-run   # show what would happen
ai-env clone <src> <dest>          # duplicate config
ai-env delete <name>               # remove config
ai-env which                       # show active environment
ai-env source list                 # show registered sources
ai-env source add <name> <glob>    # register new plugin source
ai-env source remove <name>        # unregister source
```

## Init & Migration

`ai-env init`:
1. Creates `~/.config/ai-env/` and `~/.config/ai-env/environments/`
2. Creates `~/.config/ai-env/sources.yaml` with auto-detected plugin sources
3. Backs up `~/.claude/skills/` to `~/.claude/skills.bak.YYYY-MM-DD`
4. Migrates direct files/directories from `~/.claude/skills/` into `~/.agents/skills/`
5. Replaces them with symlinks pointing to `~/.agents/skills/`

After init, `~/.claude/skills/` is fully managed by ai-env.

## Activate Output

```
$ ai-env activate springer-nature

✓ Activated: Springer Nature
  Directory: ~/Dev/ITP-springer-nature-repos
  Skills: 47 active (superpowers: 22, itp: 8, gws: 12, local: 5)
  Agent: claude (sonnet-4-6, plan mode)

  cd ~/Dev/ITP-springer-nature-repos && claude
```

ai-env does **not** launch the agent — it configures only. The user launches manually.

## Directory Structure

```
~/.config/ai-env/
  sources.yaml                     # registered plugin scan paths
  environments/
    springer-nature.yaml
    whizzgolf.yaml
    personal.yaml
  .active                          # currently active environment name

~/.agents/skills/                  # canonical skill store (single source of truth)
  gws-drive/
  gws-gmail/
  brainstorming/
  feature-analyst/
  ...

~/.claude/skills/                  # managed by ai-env (symlinks only)
  gws-drive -> ../../.agents/skills/gws-drive
  brainstorming -> ../../.agents/skills/brainstorming
  ...

~/.claude/skills.bak.2026-03-17/  # backup from init
```

## Future Extensions

- **Agent launch mode** — `ai-env activate <name> --launch` to also start the agent
- **Hooks** — pre/post-activate shell commands
- **Codex/other agents** — wire up additional agent skill directories
- **Env vars** — set environment variables as part of activation
