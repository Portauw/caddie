# Per-Directory Skill Profiles — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `ai-env activate` auto-detect the correct environment from the current working directory, manage project-local skill symlinks (`.claude/skills/` and `.agents/skills/` inside the project), and use a fingerprint+reconcile strategy so activation is fast and incremental.

**Architecture:** When `ai-env activate` is called without arguments, it resolves the environment by (1) looking for `.ai-env.yaml` in the cwd or parent dirs, (2) matching cwd against `directory:` fields in all environments, (3) falling back to a configurable default. Skills are symlinked into the project directory's `.claude/skills/` and `.agents/skills/` (not the global home dirs). A SHA-256 fingerprint of the resolved skill set is cached per-project to skip rebuilds when nothing changed. When something changed, a diff-based reconcile adds/removes only the delta. Global `~/.claude/skills/` and `~/.agents/skills/` are emptied when a project profile is active (populated with fallback default when no profile matches). Users wire activation into their shell via a simple function: `claude() { ai-env activate && command claude "$@"; }`.

**Tech Stack:** Bash (extends existing `ai-env` script), Python 3 (reuses existing inline helpers), SHA-256 via `shasum`

---

## Context

ai-env currently manages skills globally: `activate` takes an explicit name, rebuilds symlinks in `~/.claude/skills/` and `~/.agents/skills/`, and writes a global `.active` marker. This has two problems:

1. **Manual profile switching** — Users must remember which profile to activate for each project
2. **No concurrent terminals** — Two terminals in different projects share the same global skill dirs, so the last `activate` wins

The fix: activate by directory, manage project-local skill dirs, and make activation fast enough to run on every `claude` invocation via a shell wrapper.

### Design decisions (from conversation)

| Decision | Choice |
|----------|--------|
| Root-level skills when profile active | Empty (fallback default when no profile matches) |
| Profile binding | `.ai-env.yaml` in project root referencing centralized env config |
| Trigger mechanism | Shell function `claude() { ai-env activate && command claude "$@"; }` — no `ai-env run` command |
| Nested directories | Nearest-match wins (walk up to find `.ai-env.yaml`) |
| Skill dirs managed | `.claude/skills/` + `.agents/skills/`, both global and project-local |
| Rebuild strategy | Fingerprint check + diff-based reconcile |

### Files involved

- **Modify:** `/Users/pieter/ai-env/ai-env` — all changes are in this single file

---

### Task 1: Add `config.yaml` support with default environment setting

This introduces a global config file at `~/.config/ai-env/config.yaml` for settings that aren't source- or environment-specific. The first setting is `default_environment` — the fallback when no `.ai-env.yaml` is found and no `directory:` field matches cwd.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Add config file path constant**

Add after line 24 (`REPOS_DIR` definition) in the constants section:

```bash
CONFIG_FILE="$CONFIG_DIR/config.yaml"
```

**Step 2: Add `config_get` helper function**

Add after the `get_active()` function (after line 166):

```bash
# Read a value from config.yaml
config_get() {
  local key="$1"
  [[ -f "$CONFIG_FILE" ]] && yaml_get "$CONFIG_FILE" "$key" || echo ""
}
```

**Step 3: Test manually**

Run:
```bash
echo 'default_environment: "all"' > ~/.config/ai-env/config.yaml
source /Users/pieter/ai-env/ai-env  # just to check syntax
```
Expected: no errors

**Step 4: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "feat: add config.yaml support with config_get helper"
```

---

### Task 2: Add `.ai-env.yaml` lookup — walk parents to find project profile

This adds a function that walks from cwd up to `/` looking for a `.ai-env.yaml` file. The file contains `environment: "<name>"` referencing a centralized environment config.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Add `find_project_config` function**

Add after the `config_get` function:

```bash
# Walk from a directory up to / looking for .ai-env.yaml
# Prints the path to the first one found, or empty string
find_project_config() {
  local dir="${1:-$PWD}"
  dir="${dir/#\~/$HOME}"
  while [[ "$dir" != "/" ]]; do
    if [[ -f "$dir/.ai-env.yaml" ]]; then
      echo "$dir/.ai-env.yaml"
      return 0
    fi
    dir=$(dirname "$dir")
  done
  return 1
}
```

**Step 2: Add `resolve_env_from_cwd` function**

This is the main auto-detection logic. It tries three strategies in order:

```bash
# Resolve environment name from current working directory
# Strategy 1: .ai-env.yaml in cwd or parent (nearest match)
# Strategy 2: match cwd against directory: fields in all environments
# Strategy 3: fall back to default_environment from config.yaml
# Prints: environment name, or exits non-zero
resolve_env_from_cwd() {
  local cwd="${1:-$PWD}"

  # Strategy 1: .ai-env.yaml file
  local project_config
  if project_config=$(find_project_config "$cwd"); then
    local env_ref
    env_ref=$(yaml_get "$project_config" "environment")
    if [[ -n "$env_ref" ]]; then
      if env_exists "$env_ref"; then
        echo "$env_ref"
        return 0
      else
        warn ".ai-env.yaml references unknown environment: ${env_ref}"
      fi
    fi
  fi

  # Strategy 2: match cwd against directory: fields
  local best_match="" best_length=0
  for env_file in "$ENV_DIR"/*.yaml; do
    [[ ! -f "$env_file" ]] && continue
    local env_dir
    env_dir=$(yaml_get "$env_file" "directory")
    [[ -z "$env_dir" ]] && continue
    env_dir="${env_dir/#\~/$HOME}"
    # Normalize: remove trailing slash
    env_dir="${env_dir%/}"
    local check_dir="${cwd%/}"
    # Check if cwd starts with env_dir (is inside or equal)
    if [[ "$check_dir" == "$env_dir" || "$check_dir" == "$env_dir"/* ]]; then
      local dir_length=${#env_dir}
      if [[ $dir_length -gt $best_length ]]; then
        best_length=$dir_length
        best_match=$(basename "$env_file" .yaml)
      fi
    fi
  done

  if [[ -n "$best_match" ]]; then
    echo "$best_match"
    return 0
  fi

  # Strategy 3: default environment
  local default_env
  default_env=$(config_get "default_environment")
  if [[ -n "$default_env" ]] && env_exists "$default_env"; then
    echo "$default_env"
    return 0
  fi

  return 1
}
```

**Step 3: Test manually**

```bash
cd ~/Dev/whizzgolf
# Should match whizzgolf.yaml via directory: field
source <(grep -A999 'resolve_env_from_cwd' /Users/pieter/ai-env/ai-env | head -50)
```

Verify with temporary test:
```bash
# Add at end of file temporarily:
# echo "Resolved: $(resolve_env_from_cwd)"
cd ~/Dev/whizzgolf && /Users/pieter/ai-env/ai-env resolve-test
```

**Step 4: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "feat: add resolve_env_from_cwd with 3-strategy lookup"
```

---

### Task 3: Add fingerprint and reconcile functions

These two functions power the fast-path: compute a fingerprint of the expected skill set, and reconcile current symlinks with the expected set by adding/removing only the delta.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Add `compute_fingerprint` function**

Add after `resolve_env_from_cwd`:

```bash
# Compute a fingerprint for a skill set
# Input: sorted skill directory names (one per line) on stdin
# Output: SHA-256 hash string
compute_fingerprint() {
  local env_name="$1"
  {
    echo "$env_name"
    cat  # stdin: sorted skill names
  } | shasum -a 256 | cut -d' ' -f1
}
```

**Step 2: Add `reconcile_skill_dir` function**

This is the diff-based reconciler. It compares current symlinks in a target directory against the expected set, and only adds/removes the difference.

```bash
# Reconcile symlinks in a skill directory to match expected set
# Args: <target_dir> <expected_skills (newline-separated)> <skill_store_path>
# Returns: added|removed|unchanged counts via global vars
_RECONCILE_ADDED=0
_RECONCILE_REMOVED=0

reconcile_skill_dir() {
  local target_dir="$1"
  local expected="$2"
  local store="$3"

  mkdir -p "$target_dir"

  _RECONCILE_ADDED=0
  _RECONCILE_REMOVED=0

  # Build expected set
  local -A expected_set
  while IFS= read -r skill; do
    [[ -n "$skill" ]] && expected_set["$skill"]=1
  done <<< "$expected"

  # Remove symlinks not in expected set
  for item in "$target_dir"/*; do
    [[ ! -L "$item" ]] && continue
    local name
    name=$(basename "$item")
    if [[ -z "${expected_set[$name]+_}" ]]; then
      rm "$item"
      (( _RECONCILE_REMOVED++ )) || true
    fi
  done

  # Add missing symlinks
  while IFS= read -r skill; do
    [[ -z "$skill" ]] && continue
    local target_path="$target_dir/${skill}"
    if [[ ! -L "$target_path" ]]; then
      local source_path="$store/${skill}"
      if [[ -d "$source_path" || -L "$source_path" ]]; then
        ln -s "$source_path" "$target_path"
        (( _RECONCILE_ADDED++ )) || true
      fi
    fi
  done <<< "$expected"
}
```

**Step 3: Verify syntax**

```bash
bash -n /Users/pieter/ai-env/ai-env
```
Expected: no output (clean parse)

**Step 4: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "feat: add fingerprint computation and reconcile_skill_dir"
```

---

### Task 4: Refactor `cmd_activate` — no-args auto-detect + project-local skill dirs

This is the main change. Refactor `cmd_activate` to:
- Accept no arguments (auto-detect from cwd)
- Manage project-local `.claude/skills/` and `.agents/skills/` instead of global dirs
- Use fingerprint+reconcile for fast incremental updates
- Remove the `-a`/`--agent` flag (launching is the user's responsibility)

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Replace `cmd_activate` (lines 1320-1541)**

Replace the entire `cmd_activate` function with:

```bash
cmd_activate() {
  local name=""
  local dry_run=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run|-n) dry_run=true; shift ;;
      -*) die "Unknown flag: $1" ;;
      *)  name="$1"; shift ;;
    esac
  done

  # Auto-detect environment if no name provided
  if [[ -z "$name" ]]; then
    if ! name=$(resolve_env_from_cwd); then
      die "No profile found for ${PWD}\n  Create one: ${CYAN}ai-env create <name>${RESET}\n  Or add .ai-env.yaml: ${CYAN}echo 'environment: \"<name>\"' > .ai-env.yaml${RESET}"
    fi
  fi

  env_exists "$name" || die "Environment '${name}' not found. Run 'ai-env list' to see available environments."

  local file
  file="$(env_file "$name")"
  local display_name
  display_name=$(yaml_get "$file" "name")
  local directory
  directory=$(yaml_get "$file" "directory")
  [[ -n "$directory" ]] && directory="${directory/#\~/$HOME}"

  # Determine project directory (from env config or cwd)
  local project_dir="${directory:-$PWD}"

  # Determine target skill directories
  local target_claude="$project_dir/.claude/skills"
  local target_agents="$project_dir/.agents/skills"

  # Step 1: Scan sources (silent)
  cmd_scan > /dev/null 2>&1 || true

  # Step 2: Resolve skills + count by prefix
  local resolve_output
  resolve_output=$(python3 -c "
$PY_PREFIX_HELPER
import fnmatch
from collections import Counter

store = Path(os.path.expanduser('$SKILL_STORE'))
source_map, repo_map = _build_maps(os.path.expanduser('$SOURCES_FILE'))
env_file = '$file'

# Read patterns
patterns = []
in_skills = False
for line in open(env_file):
    line = line.rstrip()
    if line.startswith('skills:'):
        in_skills = True
        continue
    if in_skills and line.startswith('  - '):
        patterns.append(line[4:].strip().strip('\"').strip(\"'\"))
    elif in_skills and line and not line.startswith(' ') and not line.startswith('#'):
        break

if not patterns:
    print('SUMMARY:')
    exit(0)

# Build skill map and match
skill_map = {}
prefix_map = {}
for item in sorted(store.iterdir()):
    if not item.is_dir():
        continue
    prefix, remainder = _get_prefix(item, source_map, repo_map)
    skill_id = f'{prefix}:{remainder}'
    skill_map[skill_id] = item.name
    prefix_map[item.name] = prefix

matched = set()
for pattern in patterns:
    for skill_id, dirname in skill_map.items():
        if fnmatch.fnmatch(skill_id, pattern):
            matched.add(dirname)

# Output matched skills
for dirname in sorted(matched):
    print(dirname)

# Output summary line
counts = Counter()
for dirname in matched:
    counts[prefix_map.get(dirname, 'unknown')] += 1
parts = [f'{p}: {c}' for p, c in sorted(counts.items())]
summary = ', '.join(parts)
print(f'SUMMARY:{summary}')
")

  # Parse output
  local matched_skills prefix_summary skill_count
  prefix_summary=$(echo "$resolve_output" | grep '^SUMMARY:' | sed 's/^SUMMARY://')
  matched_skills=$(echo "$resolve_output" | grep -v '^SUMMARY:')
  skill_count=$(echo "$matched_skills" | grep -c . || echo 0)

  if [[ "$dry_run" == true ]]; then
    echo -e "\n${YELLOW}Dry run — would activate:${RESET}\n"
    echo -e "  Environment: ${BOLD}${display_name:-$name}${RESET}"
    echo -e "  Project dir: ${project_dir}"
    echo -e "  Skills:      ${skill_count} (${prefix_summary})"
    echo ""
    echo -e "  ${DIM}Would manage:${RESET}"
    echo -e "    ${DIM}${target_claude}/${RESET}"
    echo -e "    ${DIM}${target_agents}/${RESET}"
    echo ""
    echo -e "  Matched skills:"
    echo "$matched_skills" | while IFS= read -r s; do
      [[ -n "$s" ]] && echo -e "    ${s}"
    done
    return
  fi

  # Step 3: Fingerprint check — skip rebuild if nothing changed
  local fingerprint_file="$project_dir/.claude/.ai-env-fingerprint"
  local new_fingerprint
  new_fingerprint=$(echo "$matched_skills" | compute_fingerprint "$name")

  local needs_rebuild=true
  if [[ -f "$fingerprint_file" ]]; then
    local old_fingerprint
    old_fingerprint=$(cat "$fingerprint_file" 2>/dev/null || echo "")
    if [[ "$old_fingerprint" == "$new_fingerprint" ]]; then
      needs_rebuild=false
    fi
  fi

  if [[ "$needs_rebuild" == true ]]; then
    # Step 4: Reconcile project-local skill directories
    reconcile_skill_dir "$target_claude" "$matched_skills" "$SKILL_STORE"
    local claude_added=$_RECONCILE_ADDED claude_removed=$_RECONCILE_REMOVED

    reconcile_skill_dir "$target_agents" "$matched_skills" "$SKILL_STORE"
    local agents_added=$_RECONCILE_ADDED agents_removed=$_RECONCILE_REMOVED

    local total_added=$(( claude_added + agents_added ))
    local total_removed=$(( claude_removed + agents_removed ))

    # Step 5: Clear global skill dirs (they should be empty when project profile is active)
    for global_dir in "$CLAUDE_SKILLS" "$AGENT_SKILLS"; do
      if [[ -d "$global_dir" ]]; then
        for item in "$global_dir"/*; do
          [[ -L "$item" ]] && rm "$item"
        done
      fi
    done

    # Step 6: Ensure .gitignore entries exist
    _ensure_gitignore "$project_dir"

    # Step 7: Write fingerprint
    mkdir -p "$(dirname "$fingerprint_file")"
    echo "$new_fingerprint" > "$fingerprint_file"

    # Step 8: Write active marker
    echo "$name" > "$ACTIVE_FILE"

    # Build change description
    local change_desc=""
    if [[ $total_added -gt 0 && $total_removed -gt 0 ]]; then
      change_desc=" (+${total_added} added, -${total_removed} removed)"
    elif [[ $total_added -gt 0 ]]; then
      change_desc=" (+${total_added} added)"
    elif [[ $total_removed -gt 0 ]]; then
      change_desc=" (-${total_removed} removed)"
    fi

    echo -e "${GREEN}✓${RESET} ${BOLD}${display_name:-$name}${RESET}: ${skill_count} skills${change_desc}"
  else
    # Fingerprint matched — no rebuild needed
    echo -e "${GREEN}✓${RESET} ${BOLD}${display_name:-$name}${RESET}: ${skill_count} skills (unchanged)"
  fi
}
```

**Step 2: Add `_ensure_gitignore` helper**

Add before `cmd_activate`:

```bash
# Ensure project .gitignore has entries for ai-env managed dirs
_ensure_gitignore() {
  local project_dir="$1"
  local gitignore="$project_dir/.gitignore"

  # Only modify if project is a git repo
  [[ ! -d "$project_dir/.git" ]] && return

  local entries=(".claude/skills/" ".agents/skills/" ".claude/.ai-env-fingerprint")
  local needs_update=false

  for entry in "${entries[@]}"; do
    if [[ ! -f "$gitignore" ]] || ! grep -qxF "$entry" "$gitignore" 2>/dev/null; then
      needs_update=true
      break
    fi
  done

  if [[ "$needs_update" == true ]]; then
    # Append missing entries
    [[ -f "$gitignore" ]] && [[ -n "$(tail -c 1 "$gitignore")" ]] && echo "" >> "$gitignore"
    echo "" >> "$gitignore"
    echo "# ai-env managed skill directories" >> "$gitignore"
    for entry in "${entries[@]}"; do
      if [[ ! -f "$gitignore" ]] || ! grep -qxF "$entry" "$gitignore" 2>/dev/null; then
        echo "$entry" >> "$gitignore"
      fi
    done
  fi
}
```

**Step 3: Test no-args auto-detect**

```bash
cd ~/Dev/whizzgolf
/Users/pieter/ai-env/ai-env activate --dry-run
```
Expected: resolves `whizzgolf` from `directory:` field, shows project-local paths

**Step 4: Test explicit name still works**

```bash
/Users/pieter/ai-env/ai-env activate whizzgolf --dry-run
```
Expected: same output

**Step 5: Test actual activation**

```bash
cd ~/Dev/whizzgolf
/Users/pieter/ai-env/ai-env activate
ls -la ~/Dev/whizzgolf/.claude/skills/ | head -5
ls -la ~/Dev/whizzgolf/.agents/skills/ | head -5
```
Expected: symlinks pointing to `~/.config/ai-env/skills/`

**Step 6: Test fingerprint skip (run twice)**

```bash
cd ~/Dev/whizzgolf
/Users/pieter/ai-env/ai-env activate
# Second run should say "unchanged"
/Users/pieter/ai-env/ai-env activate
```
Expected: second run outputs `(unchanged)`

**Step 7: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "feat: refactor activate for per-directory profiles with reconcile"
```

---

### Task 5: Update `cmd_reset` to clean project-local skill dirs

The reset command currently only cleans global dirs. It needs to also clean project-local dirs for the currently active environment.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Add project-local cleanup to `cmd_reset`**

In the `cmd_reset` function, after the section that cleans `~/.claude/skills/` and `~/.agents/skills/` symlinks (around line 1934-1941), add:

```bash
  # 5b. Clean project-local skill dirs for all known environments
  local project_cleaned=0
  for env_yaml in "$ENV_DIR"/*.yaml; do
    [[ ! -f "$env_yaml" ]] && continue
    local env_dir
    env_dir=$(yaml_get "$env_yaml" "directory")
    [[ -z "$env_dir" ]] && continue
    env_dir="${env_dir/#\~/$HOME}"
    for subdir in ".claude/skills" ".agents/skills"; do
      local pdir="$env_dir/$subdir"
      if [[ -d "$pdir" ]]; then
        for item in "$pdir"/*; do
          [[ -L "$item" ]] && rm "$item" && (( project_cleaned++ )) || true
        done
      fi
    done
    # Remove fingerprint
    rm -f "$env_dir/.claude/.ai-env-fingerprint"
  done
  if [[ $project_cleaned -gt 0 ]]; then
    info "Cleaned ${project_cleaned} project-local symlink(s)"
  fi
```

**Step 2: Update the summary display**

In the "Will remove" section of `cmd_reset`, add a line for project-local dirs:

```bash
  echo -e "  Project-local skill symlinks (for all environments with directory: set)"
```

**Step 3: Test**

```bash
# First activate to create project-local dirs
cd ~/Dev/whizzgolf && /Users/pieter/ai-env/ai-env activate
ls ~/Dev/whizzgolf/.claude/skills/  # should have symlinks
# Then reset
/Users/pieter/ai-env/ai-env reset
ls ~/Dev/whizzgolf/.claude/skills/  # should be empty
```

**Step 4: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "feat: extend reset to clean project-local skill dirs"
```

---

### Task 6: Update `cmd_show` and `cmd_list` to reflect new behavior

Update display commands to show project-local paths and auto-detect status.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Update `cmd_show` to display target directories**

In `cmd_show`, after displaying the directory (around line 1809), add:

```bash
  if [[ -n "$directory" ]]; then
    local expanded_dir="${directory/#\~/$HOME}"
    echo -e "  ${DIM}Skills managed in:${RESET}"
    echo -e "    ${DIM}${expanded_dir}/.claude/skills/${RESET}"
    echo -e "    ${DIM}${expanded_dir}/.agents/skills/${RESET}"
    if [[ -f "${expanded_dir}/.claude/.ai-env-fingerprint" ]]; then
      echo -e "    ${DIM}(fingerprint: active)${RESET}"
    fi
  fi
```

**Step 2: Update `cmd_list` to show auto-detect hint**

After the list of environments, add:

```bash
  # Show auto-detect hint
  echo -e "  ${DIM}Auto-detect: ${CYAN}ai-env activate${RESET}${DIM} (resolves from cwd)${RESET}"
```

**Step 3: Test**

```bash
/Users/pieter/ai-env/ai-env show whizzgolf
/Users/pieter/ai-env/ai-env list
```

**Step 4: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "feat: update show/list to display project-local skill paths"
```

---

### Task 7: Update help text and remove `-a`/`--agent` flag references

Clean up the usage/help text to reflect the new behavior: no `-a` flag, `activate` without args, `.ai-env.yaml` support, shell function recommendation.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Update the `usage` function**

Replace the ENVIRONMENTS section and relevant parts:

```bash
${BOLD}ENVIRONMENTS${RESET}
  ${CYAN}create${RESET}  <name>               Create a new environment interactively
  ${CYAN}list${RESET}    (ls)                 List all environments
  ${CYAN}show${RESET}    <name>               Show config + resolved skills
  ${CYAN}edit${RESET}    <name>               Open environment config in \$EDITOR
  ${CYAN}activate${RESET} [name]              Resolve skills for cwd (or explicit env)
  ${CYAN}clone${RESET}   <src> <dest>         Clone an environment config
  ${CYAN}delete${RESET}  <name>               Delete an environment
  ${CYAN}which${RESET}                        Show currently active environment
  ${CYAN}reset${RESET}   [-f]                 Remove symlinks, store & re-enable plugins
```

Update the FLAGS section — remove `-a, --agent`:

```bash
${BOLD}FLAGS${RESET}
  -n, --dry-run             Show what would happen without executing
  -v, --verbose             Show detailed output
  -h, --help                Show this help message
```

Update the EXAMPLES section:

```bash
${BOLD}EXAMPLES${RESET}
  ai-env init                              # First-time setup
  ai-env create my-project                 # Create environment
  ai-env activate                          # Auto-detect profile from cwd
  ai-env activate my-project               # Explicit profile activation
  ai-env activate --dry-run                # Preview what would change

${BOLD}SHELL INTEGRATION${RESET}
  Add to ~/.zshrc (or ~/.bashrc):

    claude() {
      ai-env activate && command claude "\$@"
    }

    opencode() {
      ai-env activate && command opencode "\$@"
    }

${BOLD}PROJECT CONFIG${RESET}
  Create .ai-env.yaml in your project root:

    environment: "my-project"

  Or let ai-env match the directory: field in your environment configs.
```

Update the DIRECTORIES section:

```bash
${BOLD}DIRECTORIES${RESET}
  ${DIM}~/.config/ai-env/skills/${RESET}            Skill store (source of truth)
  ${DIM}~/.config/ai-env/repos/${RESET}             Cloned git repos
  ${DIM}~/.config/ai-env/config.yaml${RESET}        Global settings (default_environment)
  ${DIM}<project>/.claude/skills/${RESET}            Project skill view (managed symlinks)
  ${DIM}<project>/.agents/skills/${RESET}            Project agent view (managed symlinks)
  ${DIM}<project>/.ai-env.yaml${RESET}              Project profile binding
  ${DIM}~/.config/ai-env/environments/${RESET}       Environment YAML files
  ${DIM}~/.config/ai-env/sources.yaml${RESET}        Source + repo registry
```

**Step 2: Test**

```bash
/Users/pieter/ai-env/ai-env --help
```

**Step 3: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "docs: update help text for per-directory profiles, remove -a flag"
```

---

### Task 8: Add `ai-env init` shell integration hint

Update `cmd_init` to suggest the shell function at the end.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Add shell integration suggestion to init output**

At the end of `cmd_init` (around line 1691), replace the "Next steps" block:

```bash
  echo ""
  info "Next steps:"
  echo -e "  ${CYAN}ai-env source list${RESET}           Review detected sources"
  echo -e "  ${CYAN}ai-env inventory${RESET}             See all discovered skills"
  echo -e "  ${CYAN}ai-env create my-project${RESET}     Create your first environment"
  echo ""
  info "Shell integration (add to ~/.zshrc):"
  echo ""
  echo -e "  ${DIM}claude() {"
  echo -e "    ai-env activate && command claude \"\\\$@\""
  echo -e "  }${RESET}"
```

**Step 2: Test**

```bash
/Users/pieter/ai-env/ai-env init 2>&1 | tail -10
```

**Step 3: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "docs: add shell integration hint to init output"
```

---

### Task 9: Handle edge case — no `directory:` in env config (global-only environments)

Some environments like `all.yaml` and `sterling-agent.yaml` don't have a `directory:` field. These are global-only profiles. When activated explicitly (`ai-env activate all`), they should still work by falling back to global `~/.claude/skills/` and `~/.agents/skills/`.

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Update `cmd_activate` to handle missing directory**

In the refactored `cmd_activate`, the line `local project_dir="${directory:-$PWD}"` already handles this — when there's no `directory:` field and the user activates explicitly, it uses cwd. But for global-only envs like `all`, we should use global dirs instead of writing symlinks into whatever cwd happens to be.

Add this logic after `project_dir` is set:

```bash
  # Determine if this is a project-local or global activation
  local use_project_local=true
  if [[ -z "$directory" ]]; then
    # No directory: field — this is a global-only environment
    # Use global skill dirs unless we're in a directory with .ai-env.yaml
    local project_config
    if project_config=$(find_project_config "$PWD"); then
      project_dir="$(dirname "$project_config")"
    else
      use_project_local=false
    fi
  fi

  local target_claude target_agents fingerprint_dir
  if [[ "$use_project_local" == true ]]; then
    target_claude="$project_dir/.claude/skills"
    target_agents="$project_dir/.agents/skills"
    fingerprint_dir="$project_dir/.claude"
  else
    target_claude="$CLAUDE_SKILLS"
    target_agents="$AGENT_SKILLS"
    fingerprint_dir="$CONFIG_DIR"
  fi
```

Update the fingerprint path to use `fingerprint_dir`:

```bash
  local fingerprint_file="$fingerprint_dir/.ai-env-fingerprint"
```

Update the global cleanup section to only clear globals when using project-local:

```bash
    # Clear global skill dirs only when using project-local dirs
    if [[ "$use_project_local" == true ]]; then
      for global_dir in "$CLAUDE_SKILLS" "$AGENT_SKILLS"; do
        if [[ -d "$global_dir" ]]; then
          for item in "$global_dir"/*; do
            [[ -L "$item" ]] && rm "$item"
          done
        fi
      done
    fi
```

And only call `_ensure_gitignore` for project-local:

```bash
    [[ "$use_project_local" == true ]] && _ensure_gitignore "$project_dir"
```

**Step 2: Test global-only env**

```bash
cd /tmp
/Users/pieter/ai-env/ai-env activate all --dry-run
```
Expected: shows `~/.claude/skills/` and `~/.agents/skills/` as targets (not `/tmp/.claude/skills/`)

**Step 3: Test project-local env**

```bash
cd ~/Dev/whizzgolf
/Users/pieter/ai-env/ai-env activate --dry-run
```
Expected: shows `~/Dev/whizzgolf/.claude/skills/` as target

**Step 4: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "feat: handle global-only envs (no directory: field) in activate"
```

---

### Task 10: Bump version and create `.ai-env.yaml` example

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env` (version bump)

**Step 1: Bump version**

Change line 18:

```bash
VERSION="3.0.0"
```

**Step 2: Test full workflow end-to-end**

```bash
# 1. Create a .ai-env.yaml in a project
echo 'environment: "whizzgolf"' > ~/Dev/whizzgolf/.ai-env.yaml

# 2. Auto-detect from cwd
cd ~/Dev/whizzgolf
/Users/pieter/ai-env/ai-env activate
# Expected: ✓ WhizzGolf: N skills (+N added)

# 3. Second run — fingerprint hit
/Users/pieter/ai-env/ai-env activate
# Expected: ✓ WhizzGolf: N skills (unchanged)

# 4. Verify project-local symlinks
ls -la ~/Dev/whizzgolf/.claude/skills/ | head -5
ls -la ~/Dev/whizzgolf/.agents/skills/ | head -5

# 5. Verify global dirs are empty
ls ~/.claude/skills/
# Expected: empty (no symlinks)

# 6. Verify .gitignore was updated
grep "ai-env" ~/Dev/whizzgolf/.gitignore

# 7. Test shell function pattern
claude() { /Users/pieter/ai-env/ai-env activate && echo "would launch claude"; }
cd ~/Dev/whizzgolf && claude
# Expected: ✓ WhizzGolf: N skills (unchanged) \n would launch claude

# 8. Test explicit name override
/Users/pieter/ai-env/ai-env activate all
# Expected: uses global dirs since all.yaml has no directory:

# 9. Test unknown directory
cd /tmp
/Users/pieter/ai-env/ai-env activate
# Expected: error "No profile found for /tmp"
# (unless default_environment is set)

# 10. Test with default_environment
echo 'default_environment: "all"' > ~/.config/ai-env/config.yaml
cd /tmp
/Users/pieter/ai-env/ai-env activate
# Expected: falls back to "all" environment

# 11. Reset and verify cleanup
/Users/pieter/ai-env/ai-env reset -f
ls ~/Dev/whizzgolf/.claude/skills/
# Expected: empty
```

**Step 3: Commit**

```bash
git add /Users/pieter/ai-env/ai-env
git commit -m "chore: bump version to 3.0.0 for per-directory profiles"
```

---

## Summary of changes

| Component | What changes |
|-----------|-------------|
| `config.yaml` | New file — global settings like `default_environment` |
| `.ai-env.yaml` | New file — project-level pointer to environment config |
| `find_project_config()` | New — walks parents to find `.ai-env.yaml` |
| `resolve_env_from_cwd()` | New — 3-strategy auto-detection (`.ai-env.yaml` > `directory:` match > default) |
| `compute_fingerprint()` | New — SHA-256 of env name + sorted skill list |
| `reconcile_skill_dir()` | New — diff-based symlink reconciliation |
| `_ensure_gitignore()` | New — adds ai-env entries to project `.gitignore` |
| `cmd_activate()` | Refactored — no-args auto-detect, project-local dirs, fingerprint+reconcile |
| `cmd_reset()` | Extended — cleans project-local dirs for all known envs |
| `cmd_show()` | Updated — shows project-local skill paths |
| `cmd_list()` | Updated — shows auto-detect hint |
| `usage()` | Updated — new examples, shell integration docs, no `-a` flag |
| `cmd_init()` | Updated — suggests shell function |
| `-a`/`--agent` flag | Removed — launching is user's responsibility via shell function |
