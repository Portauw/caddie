<p align="center">
  <img src="docs/images/caddie.png" alt="caddie mascot, a small character carrying a golf bag full of clubs" width="200">
</p>

<h1 align="center">caddie</h1>
<p align="center"><em>Skill Profile Manager for Claude Code & other coding agents</em></p>

caddie manages skills from registered git repos and gives every project folder its own skill profile for Claude Code and other agents. It maintains a canonical skill store and wires up agent-specific skill directories via symlinks.

## Quick Install

Requires Go 1.22+ (`brew install go`).

```bash
./scripts/build.sh
cp dist/caddie /usr/local/bin/caddie

# One-time setup: config dir, skill store, sources.yaml
caddie setup
```

The repo also keeps `ai-env-frozen`, the original bash implementation, retained as the parity oracle for the contract test suite. It is not installed by default.

## Concepts

There are two moving parts.

**The skill store.** `caddie setup` and `caddie scan` clone registered git repos into `~/.config/caddie/repos/` and build a flat, namespaced symlink layer at `~/.config/caddie/skills/` (`prefix:name`, see [Skill Namespacing](#skill-namespacing)). This is shared across every project on the machine.

**The profile.** A `.caddie.yaml` file in a project folder. It is not a pointer to something else, it IS the profile: a name, a description, and a list of skill patterns to pull from the store. `caddie activate` walks up from your current directory looking for the nearest `.caddie.yaml`, resolves its patterns against the store, and symlinks the matches into that folder's `.agents/skills/` (with `.claude/skills` symlinked to it in turn).

There is no central profile registry and no directory-matching lookup. If two folders want the same set of skills, each gets its own `.caddie.yaml`; there is currently no way to share or reuse one across folders other than copying it.

## First-time Setup

`caddie setup` does the machine-wide setup:

1. Creates `~/.config/caddie/` and the skill store directory
2. Creates `~/.config/caddie/sources.yaml` if it does not already exist
3. Runs `caddie scan`

Run it once per machine. After that, register git repos with `caddie repo add <name> <url>` and run `caddie scan --force`.

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

### 3. Create & edit a profile
```bash
cd ~/Dev/my-project
caddie init                # interactively create .caddie.yaml in the current folder
caddie edit                # open the nearest .caddie.yaml in $EDITOR
caddie which               # print the path to the profile that is active here
```

### 4. Activate skills for the current folder
```bash
caddie activate             # resolve the nearest .caddie.yaml, rebuild symlinks, print summary
caddie activate --dry-run   # preview what would happen
caddie activate --force     # force-pull all repos now (bypass the hourly cache)
```

`activate` auto-pulls registered repos at most once per hour; pass `--force` to pull immediately.

Every activate also checks the folder's `.agents/skills/` for drift. Symlinks pointing at the wrong place (e.g. left over from a config-dir rename) are re-pointed at the current store, and symlinks the profile no longer matches are removed, reported as `-N removed`. Real directories are never touched, so a hand-placed skill dropped into `.agents/skills/` survives.

### 5. Reset
```bash
caddie reset                           # interactive: clean symlinks + rebuild instructions
caddie reset --force                   # skip the confirmation prompt
```

## Skill Namespacing

Skills are identified by `prefix:name`. The prefix comes from one of two places:

1. **Repo `prefix:` setting in `sources.yaml`**: explicit, recommended. Skills land in the store as `<prefix>-<name>` and resolve as `<prefix>:<name>`. Set `prefix: "true"` to use the repo's own name as the prefix.
2. **Repo with no `prefix:`**: skills land in the store under their bare directory name. The prefix is derived from the directory name's first hyphen-separated segment (e.g. `gws-gmail-send` → `gws:gmail-send`); a name with no hyphen falls under `local:*`.

| Directory in store | Prefix | Full ID |
|---|---|---|
| `superpowers-brainstorming/` | `superpowers` | `superpowers:brainstorming` |
| `gws-gmail-send/` | `gws` | `gws:gmail-send` |
| `recipe-save-email/` | `recipe` | `recipe:save-email` |
| `my-skill/` (no source repo match) | `my` | `my:skill` |
| `brainstorming/` (no dash, no repo) | `local` | `local:brainstorming` |

> ⚠️ Repos without a `prefix:` share the global store namespace. If two repos contain a skill with the same directory name, **one will silently overwrite the other** at scan time. Setting `prefix: "true"` (or a literal prefix string) on every repo eliminates this risk.

### Single-skill repos ("one repo = one skill")

A repo whose `SKILL.md` lives at its **top level** (rather than under `skills/<name>/`) is treated as a single skill named after the repo. Register it with `skills_path: "."` (the repo root):

```bash
caddie repo add show-your-work https://github.com/diana-percy/show-your-work.git .
```

The skill resolves as `<repo>:<repo>` (e.g. `show-your-work:show-your-work`). Collection repos, many skills under `skills/`, are unaffected; only a `SKILL.md` at the scanned root triggers this behavior.

## The `.caddie.yaml` Profile

`caddie init` writes `.caddie.yaml` directly in the folder you're standing in:

```yaml
name: "My Project"
description: "Backend services"

# Skill patterns (include-only, supports wildcards)
skills:
  - "superpowers:*"           # all superpowers skills
  - "gws:gmail*"              # gws-gmail, gws-gmail-send, etc.
  - "local:*"                 # hand-written skills
```

**Skill patterns:**
- Use glob wildcards: `*` matches any characters
- Include-only model, no exclude mechanism
- Empty `skills:` section means no skills activated
- Patterns union (order doesn't matter)

There is no other schema. `name` and `description` are just labels; only `skills` is resolved against the store. See [`examples/profile.yaml`](examples/profile.yaml).

**Resolution.** `caddie activate`, `caddie edit`, `caddie which` and `caddie export` all resolve the *nearest* `.caddie.yaml` by walking up from the current directory to `/`. If none is found, they fail and tell you to run `caddie init`. There is no fallback profile and no interactive picker.

**Not portable.** `.caddie.yaml` is gitignored by default (caddie adds it to your repo's `.gitignore` the first time it touches the folder). Cloning the repo on another machine, or for a teammate, does not bring the profile with it; each checkout needs its own `caddie init`.

The entries are written to the enclosing git repo's root `.gitignore`, anchored
to the profile's own directory — activating in `apps/web` writes
`/apps/web/.caddie.yaml`, not a bare `.caddie.yaml` that would also hide a
teammate's profile elsewhere in the repo. caddie rewrites only the block under
its own `# caddie managed skill directories` header and leaves the rest of the
file alone.

## Key Directories

```
~/.config/caddie/
  sources.yaml                 # registered git repos + their prefixes
  repos/                       # caddie-managed git checkouts
    superpowers/
    sterling-skills/
  skills/                      # canonical store (symlinks into repos/)
    superpowers-brainstorming -> repos/superpowers/skills/brainstorming
    sterling-write-as-pieter  -> repos/sterling-skills/skills/write-as-pieter
  .last-scan                   # 60s scan cache
  .last-pull                   # hourly auto-pull cache

<project>/.caddie.yaml         # the profile itself (name, description, skills)
<project>/.agents/skills/      # filtered symlinks for this project
  superpowers-brainstorming -> ~/.config/caddie/skills/superpowers-brainstorming
  ...
<project>/.claude/skills       # symlink → ../.agents/skills
<project>/.claude/.caddie-fingerprint   # short-circuits unchanged activates
```

The `~/.config/caddie/skills/` layer is the **canonical store**, every project's `.agents/skills/` symlink points there, not directly into the repo checkouts. This makes per-project filtering cheap and lets multiple projects share one cached source repo.

## Security Model

A registered repo is untrusted input. caddie clones it and puts its files where
your coding agent will read them, so it treats repo contents the way a browser
treats a website, not the way a package manager treats a signed release.

What caddie enforces:

- **Skills stay inside their own checkout.** A symlink in a skill that resolves
  outside the repo — directly, through a chain, or while its target does not yet
  exist — means the skill is not adopted. The same check runs again at export
  time, scoped to the skill's own directory.
- **Git transports are restricted.** `GIT_ALLOW_PROTOCOL` limits clones and
  pulls to http, https, ssh, git and file, which blocks git's `ext::` helper
  from running a shell command out of a repo URL. It overrides a permissive
  `protocol.*.allow` in your own git config.
- **Repo names are plain directory names.** The name becomes a directory under
  `~/.config/caddie/repos/` that `caddie repo remove` deletes, so anything with
  a path separator, `..`, or a leading `-` or `~` is refused.
- **Destructive operations ask first.** `caddie export --clean` deletes
  directories caddie did not create; it lists them and prompts unless you pass
  `--force`. `caddie activate` never deletes anything while adopting an existing
  `.claude/skills` directory — if a name is already taken on the other side it
  moves nothing and tells you.

What it does **not** protect against: the skill content itself. A `SKILL.md` is
a set of instructions your agent will read and act on, and no amount of path
containment changes that. Review a repo before registering it, and prefer
pinned, known sources.

## Exporting Skills

Export resolves symlink chains and copies the real SKILL.md files to a target directory or S3 bucket. This is useful for Docker containers, cloud runtimes (Lambda, ECS), and CI/CD pipelines where symlinks don't work.

```bash
# Export the current folder's profile to a local directory
caddie export --to ./exported-skills/

# Export directly to S3
caddie export --to s3://my-bucket/skills/

# Export every skill in the store (no profile filter)
caddie export --all --to /tmp/all-skills/

# Preview without copying
caddie export --to ./skills/ --dry-run

# Remove stale skills from target that are no longer in the export set
caddie export --to ./skills/ --clean
```

**Flags:**
- `--all`: export every skill in the store instead of resolving the cwd profile
- `--dry-run` / `-n`: preview what would be copied without writing anything
- `--clean`: remove skill directories in the target that aren't in the export set. This deletes directories caddie did not create, so it lists them and asks before doing it.
- `--force` / `-f`: answer yes to that prompt (for scripts and CI)

A skill containing a symlink that points outside its own directory is skipped
and named in the output, rather than exported. Otherwise a local export ships
a link that resolves to nothing on the receiving machine, and the S3 upload —
which follows symlinks — would send whatever the link pointed at.

**S3 environment variables:**
- `AWS_PROFILE`: passed through to `aws s3 cp`
- `AWS_ENDPOINT_URL`: for LocalStack or custom S3-compatible endpoints

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
ae init
ae which
aea
```

## All Commands

```bash
# Setup
caddie setup                       # one-time machine setup: config dir, store, sources
caddie reset [--force|-f]          # remove managed symlinks + state files

# Repo registry
caddie repo list                   # show registered repos + clone status
caddie repo add <name> <url> [path]   # register + shallow-clone a repo (name must be a plain
                                   #   directory name — no "/", "..", leading "-" or "~")
caddie repo remove <name>          # unregister + clean store links + rm checkout
caddie repo update [name]          # git pull (all, or one)

# Discovery
caddie scan [--force|-f] [-v]      # rebuild canonical store
caddie inventory [filter]          # list all skills, grouped by prefix

# Profile
caddie init                        # create .caddie.yaml in the current folder
caddie edit                        # open the nearest .caddie.yaml in $EDITOR
caddie which                       # print the path to the active profile

# Activation
caddie activate [-n|-f]            # scan + rebuild project symlinks for the nearest profile
                                   #   -n / --dry-run → preview only
                                   #   -f / --force → force-pull all repos

# Export (resolves symlinks → copies real files)
caddie export --to <dir>           # local directory, cwd profile
caddie export --to s3://b/         # S3 prefix, cwd profile
caddie export --all --to <target>  # export every skill (no profile filter)
                                   # extras: --clean (prompts), --force / -f, --dry-run / -n
```

## Shell Integration

caddie configures skills but does not launch Claude. Wire it into your `claude` invocation so the right profile is always active:

```bash
# ~/.zshrc or ~/.bashrc
claude() {
  caddie activate && command claude "$@"
}
```

`activate` short-circuits via a fingerprint cache when nothing changed, so the overhead is sub-100ms on the hot path.
