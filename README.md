# ai-env — Skill Profile Manager

ai-env manages skills across multiple sources and creates project-specific skill profiles for Claude Code and other agents. It maintains a canonical skill store, auto-detects plugin sources, and configures agent-specific skill directories via symlinks.

## Quick Install

```bash
# Copy to your PATH
cp ai-env /usr/local/bin/ai-env
chmod +x /usr/local/bin/ai-env

# Initialize (backs up ~/.claude/skills/, creates config directories, auto-detects sources)
ai-env init
```

## First-time Setup

`ai-env init` does the heavy lifting:

1. Creates `~/.config/ai-env/` with default configuration
2. Creates `~/.config/ai-env/environments/` for your project profiles
3. Auto-detects installed plugin sources and registers them in `~/.config/ai-env/sources.yaml`
4. Backs up your existing `~/.claude/skills/` to `~/.claude/skills.bak.YYYY-MM-DD`
5. Migrates any hand-written skills to `~/.agents/skills/` (canonical store)
6. Replaces `~/.claude/skills/` with symlinks managed by ai-env

After init, ai-env fully manages the skill activation pipeline.

## Usage Flow

### 1. Scan & discover skills
```bash
ai-env scan        # discover all skills from registered sources
ai-env inventory   # list all available skills with source prefixes
```

### 2. Manage sources (optional)
```bash
ai-env source list                     # show registered sources
ai-env source add mylib ~/my/skills    # register new skill source
ai-env source remove mylib             # unregister source
```

### 3. Create & manage environments
```bash
ai-env create my-project               # create new environment interactively
ai-env list                            # list all environments
ai-env show my-project                 # view environment config + resolved skills
ai-env edit my-project                 # edit config in $EDITOR
ai-env clone my-project staging        # duplicate config
ai-env delete my-project               # remove environment
```

### 4. Activate environment & skills
```bash
ai-env activate my-project             # scan + rebuild symlinks + print summary
ai-env activate my-project --dry-run   # preview what would happen
ai-env which                           # show currently active environment
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

Environments live as YAML files in `~/.config/ai-env/environments/<name>.yaml`:

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

Drop a `.ai-env.yaml` file in any project directory to bind it to a profile:

```yaml
environment: "my-project"
```

When you run `ai-env activate` from that directory (or any subdirectory), it auto-detects the profile and sets up skills locally in `.agents/skills/` and `.claude/skills/`.

If no `.ai-env.yaml` exists, `activate` prompts you to pick a profile and creates the file automatically.

Profiles are reusable — the same profile can be bound to multiple directories via separate `.ai-env.yaml` files.

## Key Directories

```
~/.config/ai-env/
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

~/.claude/skills/              # managed by ai-env (symlinks only)
  gws-drive -> ../../.agents/skills/gws-drive
  brainstorming -> ../../.agents/skills/brainstorming
  ...

~/.claude/skills.bak.2026-03-17/  # backup created by init
```

## Exporting Skills

Export resolves symlink chains and copies the real SKILL.md files to a target directory or S3 bucket. This is useful for Docker containers, cloud runtimes (Lambda, ECS), and CI/CD pipelines where symlinks don't work.

```bash
# Export an environment's skills to a local directory
ai-env export my-project --to ./exported-skills/

# Export directly to S3
ai-env export my-project --to s3://my-bucket/skills/

# Export all skills (no environment filter)
ai-env export --all --to /tmp/all-skills/

# Preview without copying
ai-env export my-project --to ./skills/ --dry-run

# Remove stale skills from target that are no longer in the export set
ai-env export my-project --to ./skills/ --clean
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
alias ae="ai-env"
alias aea="ai-env activate"
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
ai-env init                        # first-time setup + auto-detect sources

# Discovery
ai-env scan                        # discover skills from all sources
ai-env inventory                   # list all discovered skills

# Environment management
ai-env create <name>               # create new environment
ai-env list                        # list environments
ai-env show <name>                 # show config + resolved skills
ai-env edit <name>                 # edit in $EDITOR
ai-env clone <src> <dest>          # duplicate config
ai-env delete <name>               # remove environment

# Activation
ai-env activate <name>             # scan + rebuild symlinks + summary
ai-env activate <name> --dry-run   # preview changes
ai-env which                       # show active environment

# Export
ai-env export <name> --to <dir>    # copy resolved skills to directory
ai-env export <name> --to s3://b/  # upload resolved skills to S3
ai-env export --all --to <target>  # export all skills
ai-env export <name> --to <t> --clean     # remove stale skills from target
ai-env export <name> --to <t> --dry-run   # preview only

# Source management
ai-env source list                 # show registered sources
ai-env source add <name> <glob>    # register new source
ai-env source remove <name>        # unregister source
```

## Activation Output

```
$ ai-env activate my-project

✓ Activated: My Project
  Directory: ~/Dev/my-project
  Skills: 47 active (superpowers: 22, gws: 12, recipe: 8, local: 5)
  Agent: claude (sonnet-4-6, plan mode)

  cd ~/Dev/my-project && claude
```

ai-env configures your skills but does not launch Claude Code — you launch it manually.
