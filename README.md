# caddie — Skill Profile Manager

caddie manages skills from registered git repos and creates project-specific skill profiles for Claude Code and other agents. It maintains a canonical skill store and configures agent-specific skill directories via symlinks.

## Quick Install

Requires Go 1.22+ (`brew install go`).

```bash
./scripts/build.sh
cp dist/caddie /usr/local/bin/caddie

# Initialize (creates config directories, skill store, ~/.claude/skills symlink)
caddie init
```

The repo also keeps `ai-env-frozen` — the original bash implementation, used as an escape hatch and as the parity oracle for contract tests. It is not installed by default.

## First-time Setup

`caddie init` does the heavy lifting:

1. Creates `~/.config/caddie/` with default configuration
2. Creates `~/.config/caddie/environments/` for your project profiles
3. Creates an empty `~/.config/caddie/sources.yaml` repo registry
4. Backs up your existing `~/.claude/skills/` to `~/.claude/skills.bak.YYYY-MM-DD`
5. Migrates any hand-written skills to `~/.agents/skills/` (canonical store)
6. Replaces `~/.claude/skills/` with symlinks managed by caddie

After init, register git repos with `caddie repo add <name> <url>` and run `caddie scan`.

## Usage Flow

### 1. Scan & discover skills
```bash
caddie scan        # discover all skills from registered sources
caddie inventory   # list all available skills with source prefixes
```

### 2. Manage sources (optional)
```bash
caddie source list                     # show registered sources
caddie source add mylib ~/my/skills    # register new skill source
caddie source remove mylib             # unregister source
```

### 3. Create & manage environments
```bash
caddie create my-project               # create new environment interactively
caddie list                            # list all environments
caddie show my-project                 # view environment config + resolved skills
caddie edit my-project                 # edit config in $EDITOR
caddie clone my-project staging        # duplicate config
caddie delete my-project               # remove environment
```

### 4. Activate environment & skills
```bash
caddie activate my-project             # scan + rebuild symlinks + print summary
caddie activate my-project --dry-run   # preview what would happen
caddie which                           # show currently active environment
```

## Skill Namespacing

Skills are identified by `prefix:name` format, automatically derived from directory structure:

| Directory | Prefix | Full ID |
|---|---|---|
| `gws-drive/` | `gws` | `gws:drive` |
| `gws-gmail-send/` | `gws` | `gws:gmail-send` |
| `recipe-save-email/` | `recipe` | `recipe:save-email` |
| `local/` or no dash | `local` | `local:*` |

For plugin sources, the source name becomes the prefix (e.g., `superpowers`, `itp`).

## Environment Config Format

Environments live as YAML files in `~/.config/caddie/environments/<name>.yaml`:

```yaml
name: "My Project"
description: "Backend services"

# Skill patterns (include-only, supports wildcards)
skills:
  - "superpowers:*"           # all superpowers skills
  - "gws:gmail*"              # gws-gmail, gws-gmail-send, etc.
  - "gws:calendar*"           # gws-calendar and variants
  - "local:*"                 # hand-written skills
  - "recipe:*-email-*"        # email-related recipes

# Agent-specific settings (currently Claude, extensible for Codex, etc.)
agents:
  claude:
    model: "claude-sonnet-4-6"
    permission_mode: "plan"
    system_prompt_file: "./CLAUDE.md"
```

**Skill patterns:**
- Use glob wildcards: `*` matches any characters
- Include-only model — no exclude mechanism
- Empty `skills:` section means no skills activated
- Patterns union (order doesn't matter)

## Binding a Directory to a Profile

Drop a `.caddie.yaml` file in any project directory to bind it to a profile:

```yaml
environment: "my-project"
```

When you run `caddie activate` from that directory (or any subdirectory), it auto-detects the profile and sets up skills locally in `.agents/skills/` and `.claude/skills/`.

If no `.caddie.yaml` exists, `activate` prompts you to pick a profile and creates the file automatically.

Profiles are reusable — the same profile can be bound to multiple directories via separate `.caddie.yaml` files.

## Key Directories

```
~/.config/caddie/
  sources.yaml                 # registered plugin sources
  environments/                # your project profiles
    my-project.yaml
    another-project.yaml

~/.agents/skills/              # canonical skill store (single source of truth)
  gws-drive/
  gws-gmail/
  recipe-save-email/
  brainstorming/
  ...

~/.claude/skills/              # managed by caddie (symlinks only)
  gws-drive -> ../../.agents/skills/gws-drive
  brainstorming -> ../../.agents/skills/brainstorming
  ...

~/.claude/skills.bak.2026-03-17/  # backup created by init
```

## Exporting Skills

Export resolves symlink chains and copies the real SKILL.md files to a target directory or S3 bucket. This is useful for Docker containers, cloud runtimes (Lambda, ECS), and CI/CD pipelines where symlinks don't work.

```bash
# Export an environment's skills to a local directory
caddie export my-project --to ./exported-skills/

# Export directly to S3
caddie export my-project --to s3://my-bucket/skills/

# Export all skills (no environment filter)
caddie export --all --to /tmp/all-skills/

# Preview without copying
caddie export my-project --to ./skills/ --dry-run

# Remove stale skills from target that are no longer in the export set
caddie export my-project --to ./skills/ --clean
```

**Flags:**
- `--dry-run` / `-n` — preview what would be copied without writing anything
- `--clean` — remove skill directories in the target that aren't in the export set

**S3 environment variables:**
- `AWS_PROFILE` — passed through to `aws s3 cp`
- `AWS_ENDPOINT_URL` — for LocalStack or custom S3-compatible endpoints

The output directory structure mirrors the skill store:
```
exported-skills/
  gws-calendar/SKILL.md
  gws-gmail-send/SKILL.md
  brainstorming/SKILL.md
  ...
```

## Shell Aliases

```bash
alias ae="caddie"
alias aea="caddie activate"
```

Quick usage:
```bash
ae create my-project
ae list
aea my-project
```

## All Commands

```bash
# Initialization
caddie init                        # first-time setup + auto-detect sources

# Discovery
caddie scan                        # discover skills from all sources
caddie inventory                   # list all discovered skills

# Environment management
caddie create <name>               # create new environment
caddie list                        # list environments
caddie show <name>                 # show config + resolved skills
caddie edit <name>                 # edit in $EDITOR
caddie clone <src> <dest>          # duplicate config
caddie delete <name>               # remove environment

# Activation
caddie activate <name>             # scan + rebuild symlinks + summary
caddie activate <name> --dry-run   # preview changes
caddie which                       # show active environment

# Export
caddie export <name> --to <dir>    # copy resolved skills to directory
caddie export <name> --to s3://b/  # upload resolved skills to S3
caddie export --all --to <target>  # export all skills
caddie export <name> --to <t> --clean     # remove stale skills from target
caddie export <name> --to <t> --dry-run   # preview only

# Source management
caddie source list                 # show registered sources
caddie source add <name> <glob>    # register new source
caddie source remove <name>        # unregister source
```

## Activation Output

```
$ caddie activate my-project

✓ Activated: My Project
  Directory: ~/Dev/my-project
  Skills: 47 active (superpowers: 22, gws: 12, recipe: 8, local: 5)
  Agent: claude (sonnet-4-6, plan mode)

  cd ~/Dev/my-project && claude
```

caddie configures your skills but does not launch Claude Code — you launch it manually.
