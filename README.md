<p align="center">
  <img src="docs/images/caddie.png" alt="caddie mascot — a small character carrying a golf bag full of clubs" width="200">
</p>

<h1 align="center">caddie</h1>
<p align="center"><em>Skill Profile Manager for Claude Code & other coding agents</em></p>

caddie manages skills from registered git repos and creates project-specific skill profiles for Claude Code and other agents. It maintains a canonical skill store and configures agent-specific skill directories via symlinks.

**Platform support:** macOS ✓ · Linux ✓ · Windows 10/11 ✓

## Quick Install

### macOS / Linux

Requires Go 1.22+ (`brew install go` on macOS, or your distro's package manager).

```bash
./scripts/build.sh
cp dist/caddie /usr/local/bin/caddie

# Initialize (creates config directories, skill store, ~/.claude/skills symlink)
caddie init
```

### Windows

Requires Go 1.22+ (`winget install GoLang.Go`) and Git for Windows.

```powershell
pwsh ./scripts/build.ps1
# Copy dist\caddie.exe to a directory on your %PATH%, e.g.:
Copy-Item dist\caddie.exe "$env:USERPROFILE\bin\caddie.exe"

# Initialize
caddie init
```

**Symlink note:** caddie uses directory symlinks to manage skill profiles. On Windows, either:
- Enable **Developer Mode** in *Settings → System → For developers* (recommended — gives real symlinks, best performance), or
- Leave it off — caddie automatically falls back to NTFS directory junctions, which require no elevation and work for all standard use cases. A one-time notice is printed the first time a junction is created.

The repo also keeps `ai-env-frozen` — the original bash implementation, retained as the parity oracle for the contract test suite. It is not installed by default.

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

### 1. Register & update skill repos
```bash
caddie repo add lenny https://github.com/RefoundAI/lenny-skills    # register + clone
caddie repo add my-team git@host:org/skills.git skills             # custom skills_path
caddie repo list                                                   # show registered repos + status
caddie repo update                                                 # git pull every repo
caddie repo update lenny                                           # pull a specific one
caddie repo remove lenny                                           # unregister + clean store links
```

### 2. Scan & discover skills
```bash
caddie scan                # rebuild the canonical store from registered repos
caddie scan --force        # bypass the 60s scan cache
caddie scan -v             # verbose (show each symlink decision)
caddie inventory           # list all available skills with source prefixes
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
caddie activate my-project             # resolve, rebuild symlinks, print summary
caddie activate                        # auto-detect from cwd (.caddie.yaml or directory: match)
caddie activate my-project --dry-run   # preview what would happen
caddie activate --force                # force-pull all repos now (bypass hourly cache)
caddie which                           # show currently active environment
```

`activate` auto-pulls registered repos at most once per hour; pass `--force` to pull immediately. Stale store symlinks (e.g. left over from a config-dir rename) are detected on every activate and silently re-pointed at the current store.

### 5. Reset
```bash
caddie reset                           # interactive: clean symlinks + rebuild instructions
caddie reset --force                   # skip the confirmation prompt
```

## Skill Namespacing

Skills are identified by `prefix:name`. The prefix comes from one of two places:

1. **Repo `prefix:` setting in `sources.yaml`** — explicit, recommended. Skills land in the store as `<prefix>-<name>` and resolve as `<prefix>:<name>`. Set `prefix: "true"` to use the repo's own name as the prefix.
2. **Repo with no `prefix:`** — skills land in the store under their bare directory name. The prefix is derived from the directory name's first hyphen-separated segment (e.g. `gws-gmail-send` → `gws:gmail-send`); a name with no hyphen falls under `local:*`.

| Directory in store | Prefix | Full ID |
|---|---|---|
| `superpowers-brainstorming/` | `superpowers` | `superpowers:brainstorming` |
| `gws-gmail-send/` | `gws` | `gws:gmail-send` |
| `recipe-save-email/` | `recipe` | `recipe:save-email` |
| `my-skill/` (no source repo match) | `my` | `my:skill` |
| `brainstorming/` (no dash, no repo) | `local` | `local:brainstorming` |

> ⚠️ Repos without a `prefix:` share the global store namespace. If two repos contain a skill with the same directory name, **one will silently overwrite the other** at scan time. Setting `prefix: "true"` (or a literal prefix string) on every repo eliminates this risk.

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
  sources.yaml                 # registered git repos + their prefixes
  environments/                # your project profiles
    my-project.yaml
  repos/                       # caddie-managed git checkouts
    superpowers/
    sterling-skills/
  skills/                      # canonical store (symlinks into repos/)
    superpowers-brainstorming -> repos/superpowers/skills/brainstorming
    sterling-write-as-pieter  -> repos/sterling-skills/skills/write-as-pieter
  .last-scan                   # 60s scan cache
  .last-pull                   # hourly auto-pull cache

<project>/.agents/skills/      # filtered symlinks for this project
  superpowers-brainstorming -> ~/.config/caddie/skills/superpowers-brainstorming
  ...

<project>/.claude/skills       # symlink → ../.agents/skills
<project>/.claude/.caddie-fingerprint   # short-circuits unchanged activates

~/.agents/skills/              # global symlinks (used when no project dir is set)
~/.claude/skills/              # global symlink → ~/.agents/skills

~/.claude/skills.bak.YYYY-MM-DD/   # backup created by init
```

The `~/.config/caddie/skills/` layer is the **canonical store** — every project's `.agents/skills/` symlink points there, not directly into the repo checkouts. This makes per-project filtering cheap and lets multiple projects share one cached source repo.

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
caddie init                        # first-time setup
caddie reset [--force|-f]          # remove managed symlinks + state files

# Repo registry
caddie repo list                   # show registered repos + clone status
caddie repo add <name> <url> [path]   # register + shallow-clone a repo
caddie repo remove <name>          # unregister + clean store links + rm checkout
caddie repo update [name]          # git pull (all, or one)

# Discovery
caddie scan [--force|-f] [-v]      # rebuild canonical store
caddie inventory [filter]          # list all skills, grouped by prefix

# Environment management
caddie create <name>               # create new environment interactively
caddie list                        # list environments
caddie show <name>                 # show config + resolved skills
caddie edit <name>                 # open env file in $EDITOR
caddie clone <src> <dest>          # duplicate config
caddie delete <name>               # remove environment

# Activation
caddie activate [name] [-n|-f]     # scan + rebuild project symlinks
                                   #   no name → auto-detect from cwd
                                   #   -n / --dry-run → preview only
                                   #   -f / --force → force-pull all repos
caddie which                       # show active environment for cwd

# Export (resolves symlinks → copies real files)
caddie export <name> --to <dir>    # local directory
caddie export <name> --to s3://b/  # S3 prefix
caddie export --all --to <target>  # export every skill (no env filter)
                                   # extras: --clean, --dry-run / -n
```

## Shell Integration

caddie configures skills but does not launch Claude. `caddie activate` creates
**persistent symlinks on disk** — Claude Code (VS Code extension, any terminal,
any launcher) reads `~/.claude/skills/` directly, so the wrapper below is
optional convenience, not a requirement.

The wrapper auto-switches to the right skill profile whenever you open a new
terminal in a different project. Without it, run `caddie activate` once per
project (or add it to your VS Code workspace `tasks.json`).

**macOS / Linux** — add to `~/.zshrc` or `~/.bashrc`:
```bash
claude() {
  caddie activate && command claude "$@"
}
```

**Windows PowerShell** — add to your `$PROFILE`:
```powershell
function claude { caddie activate; & claude.cmd $args }
```

`activate` short-circuits via a fingerprint cache when nothing changed, so the overhead is sub-100ms on the hot path.
