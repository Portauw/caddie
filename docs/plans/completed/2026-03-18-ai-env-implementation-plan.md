# ai-env Skill Profile Manager Implementation Plan

> **Status:** COMPLETED (2026-03-18)

**Goal:** Rewrite ai-env from a Claude Code launcher into an agent-agnostic skill profile manager that discovers skills from multiple sources, manages a canonical store at `~/.agents/skills/`, and rebuilds `~/.claude/skills/` with filtered symlinks based on environment configs.

**Architecture:** ai-env scans registered plugin sources and `~/.agents/skills/` to build a complete skill inventory. Each environment YAML config declares include-only glob patterns (e.g., `"gws:gmail*"`, `"superpowers:*"`). On activate, ai-env resolves patterns against the inventory, wipes `~/.claude/skills/`, and creates symlinks for matched skills only. No agent is launched -- ai-env is configure-only.

**Tech Stack:** Bash (primary), Python 3 (for version sorting, glob matching, YAML-to-inventory logic). No new dependencies beyond what macOS provides.

---

## Existing Codebase Reference

- Current script: `/Users/pieter/ai-env/ai-env` (730 lines, will be fully rewritten)
- Design doc: `/Users/pieter/ai-env/docs/plans/2026-03-17-skill-profile-manager-design.md`
- Canonical store: `~/.agents/skills/` (92 skills, all directories)
- Claude skills: `~/.claude/skills/` (92 symlinks + 2 bare .md files + 3 bare directories)
- Plugin cache: `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/skills/`
- Some plugins also have `.claude/skills/` (e.g., `inthepocket/itp-engineering-springer-nature/1.0.1/.claude/skills/`)

## Key Data Structures

**Skill identifier:** `<prefix>:<name>` where:
- For `~/.agents/skills/`: prefix = first segment before `-` (e.g., `gws-drive` -> `gws:drive`). No dash = `local` prefix.
- For plugin sources: prefix = registered source name.

**sources.yaml format:**
```yaml
sources:
  - name: superpowers
    type: plugin
    marketplace: superpowers-dev
    plugin: superpowers
  - name: itp
    type: plugin
    marketplace: inthepocket
    plugin: itp-engineering-backend
  - name: compound
    type: plugin
    marketplace: every-marketplace
    plugin: compound-engineering
```

**Environment YAML format:**
```yaml
name: "Springer Nature"
description: "Backend Kotlin services"
directory: "~/Dev/ITP-springer-nature-repos"
skills:
  - "superpowers:*"
  - "gws:gmail*"
  - "local:*"
agents:
  claude:
    model: "claude-sonnet-4-6"
    permission_mode: "plan"
```

---

## Task 1: Script Skeleton and Constants

**Files:**
- Rewrite: `/Users/pieter/ai-env/ai-env`

**Step 1: Write the skeleton with updated constants and usage**

Replace the entire script with the new skeleton. Keep `set -euo pipefail`, color codes, and basic helpers (`info`, `success`, `warn`, `error`, `die`, `require_cmd`). Update:

```bash
#!/usr/bin/env bash
set -euo pipefail

VERSION="2.0.0"
CONFIG_DIR="${AI_ENV_DIR:-$HOME/.config/ai-env}"
ENV_DIR="$CONFIG_DIR/environments"
SOURCES_FILE="$CONFIG_DIR/sources.yaml"
ACTIVE_FILE="$CONFIG_DIR/.active"
CANONICAL_STORE="$HOME/.agents/skills"
CLAUDE_SKILLS="$HOME/.claude/skills"
EDITOR="${EDITOR:-vim}"
```

Add the new command dispatch in `main()`:

```bash
main() {
  local command="${1:-}"
  case "$command" in
    init)                    shift; cmd_init "$@" ;;
    scan)                    shift; cmd_scan "$@" ;;
    inventory)               shift; cmd_inventory "$@" ;;
    create|new)              shift; cmd_create "$@" ;;
    list|ls)                 shift; cmd_list "$@" ;;
    show|info)               shift; cmd_show "$@" ;;
    edit)                    shift; cmd_edit "$@" ;;
    activate|use)            shift; cmd_activate "$@" ;;
    clone|cp)                shift; cmd_clone "$@" ;;
    delete|rm)               shift; cmd_delete "$@" ;;
    which|active)            shift; cmd_which "$@" ;;
    source)                  shift; cmd_source "$@" ;;
    -h|--help|help)          usage ;;
    -v|--version)            echo "ai-env v${VERSION}" ;;
    "")                      usage ;;
    *)                       die "Unknown command: ${command}\nRun 'ai-env --help' for usage." ;;
  esac
}
```

**Step 2: Verify the skeleton runs**

```bash
chmod +x ~/ai-env/ai-env
~/ai-env/ai-env --version
```
Expected: `ai-env v2.0.0`

```bash
~/ai-env/ai-env --help
```
Expected: Help text with all new commands listed.

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "refactor: rewrite ai-env skeleton for skill profile manager v2"
```

---

## Task 2: YAML Helpers (reuse and extend)

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env` (helpers section)

**Step 1: Copy over existing YAML helpers**

Keep `yaml_get`, `yaml_list`, `yaml_map` from the current script unchanged. They work fine for the new YAML format too.

Add a new helper `yaml_nested_get` for reading nested keys like `agents.claude.model`:

```bash
# Read a deeply nested YAML value (up to 3 levels: a.b.c)
yaml_nested_get() {
  local file="$1" key="$2"
  local depth
  IFS='.' read -ra parts <<< "$key"
  depth=${#parts[@]}
  
  if [[ $depth -eq 1 ]]; then
    yaml_get "$file" "${parts[0]}"
  elif [[ $depth -eq 2 ]]; then
    yaml_get "$file" "${parts[0]}.${parts[1]}"
  elif [[ $depth -eq 3 ]]; then
    # Three-level nesting: agents.claude.model
    python3 -c "
import sys
lines = open('$file').readlines()
level = 0
found = [False, False, False]
keys = '${parts[0]}', '${parts[1]}', '${parts[2]}'
for line in lines:
    stripped = line.rstrip()
    if not stripped or stripped.startswith('#'):
        continue
    indent = len(line) - len(line.lstrip())
    content = stripped.lstrip()
    if indent == 0 and content.startswith(keys[0] + ':'):
        found[0] = True
        continue
    if found[0] and indent == 2 and content.startswith(keys[1] + ':'):
        found[1] = True
        continue
    if found[1] and indent == 4 and content.startswith(keys[2] + ':'):
        val = content.split(':', 1)[1].strip().strip('\"').strip(\"'\")
        print(val)
        sys.exit(0)
    if found[1] and indent <= 2:
        break
    if found[0] and indent == 0:
        break
"
  fi
}
```

Add environment file path helpers:

```bash
env_file() {
  echo "$ENV_DIR/${1}.yaml"
}

env_exists() {
  [[ -f "$(env_file "$1")" ]]
}

list_envs() {
  find "$ENV_DIR" -maxdepth 1 -name '*.yaml' -exec basename {} .yaml \; 2>/dev/null | sort
}

get_active() {
  [[ -f "$ACTIVE_FILE" ]] && cat "$ACTIVE_FILE" || echo ""
}
```

**Step 2: Verify helpers work**

Create a temporary test YAML and run the helpers against it (use a subshell that sources the script's functions):

```bash
# Quick manual test - create test yaml, source functions, test them
cat > /tmp/test-ai-env.yaml << 'EOF'
name: "Test"
description: "A test"
skills:
  - "gws:*"
  - "local:*"
agents:
  claude:
    model: "claude-sonnet-4-6"
    permission_mode: "plan"
EOF

bash -c 'source <(sed -n "/^yaml_get/,/^cmd_/p" ~/ai-env/ai-env | head -n -1); yaml_get /tmp/test-ai-env.yaml name'
```

Expected: `Test`

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: add YAML helpers for nested keys and env file paths"
```

---

## Task 3: Source Management (`source list/add/remove`)

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env` (add `cmd_source` function)

**Step 1: Implement `cmd_source`**

```bash
cmd_source() {
  local subcmd="${1:-}"
  shift || true
  case "$subcmd" in
    list|ls)   cmd_source_list "$@" ;;
    add)       cmd_source_add "$@" ;;
    remove|rm) cmd_source_remove "$@" ;;
    "")        die "Usage: ai-env source <list|add|remove>" ;;
    *)         die "Unknown source command: $subcmd" ;;
  esac
}
```

**`cmd_source_list`:** Read `sources.yaml` and display registered sources.

```bash
cmd_source_list() {
  if [[ ! -f "$SOURCES_FILE" ]]; then
    warn "No sources registered. Run ${CYAN}ai-env init${RESET} or ${CYAN}ai-env source add${RESET}."
    return
  fi
  echo -e "${BOLD}Registered plugin sources:${RESET}\n"
  echo -e "  ${DIM}(~/.agents/skills/ is always scanned implicitly)${RESET}\n"
  
  # Parse sources from YAML
  python3 -c "
import sys
in_sources = False
current = {}
for line in open('$SOURCES_FILE'):
    line = line.rstrip()
    if line.startswith('sources:'):
        in_sources = True
        continue
    if in_sources and line.startswith('  - '):
        if current:
            print(f\"  {current.get('name','?'):20s} {current.get('marketplace','?')}/{current.get('plugin','?')}\")
        current = {}
        # Might be inline: - name: foo
        rest = line[4:].strip()
        if rest.startswith('name:'):
            current['name'] = rest.split(':',1)[1].strip().strip('\"').strip(\"'\")
    elif in_sources and line.startswith('    '):
        parts = line.strip().split(':', 1)
        if len(parts) == 2:
            k = parts[0].strip()
            v = parts[1].strip().strip('\"').strip(\"'\")
            current[k] = v
    elif in_sources and not line.strip().startswith('#') and not line.strip() == '' and not line.startswith(' '):
        break
if current:
    print(f\"  {current.get('name','?'):20s} {current.get('marketplace','?')}/{current.get('plugin','?')}\")
"
}
```

**`cmd_source_add`:** Append a source to `sources.yaml`. Takes `<name> <marketplace> <plugin>`.

```bash
cmd_source_add() {
  local name="${1:-}" marketplace="${2:-}" plugin="${3:-}"
  [[ -z "$name" || -z "$marketplace" || -z "$plugin" ]] && \
    die "Usage: ai-env source add <name> <marketplace> <plugin>\n  Example: ai-env source add superpowers superpowers-dev superpowers"
  
  # Validate plugin cache exists
  local cache_path="$HOME/.claude/plugins/cache/${marketplace}/${plugin}"
  if [[ ! -d "$cache_path" ]]; then
    die "Plugin cache not found: ${cache_path}\n  Available marketplaces: $(ls ~/.claude/plugins/cache/ 2>/dev/null | tr '\n' ' ')"
  fi
  
  # Ensure sources file exists with header
  if [[ ! -f "$SOURCES_FILE" ]]; then
    mkdir -p "$(dirname "$SOURCES_FILE")"
    echo "sources:" > "$SOURCES_FILE"
  fi
  
  # Check for duplicate name
  if grep -q "name: ${name}" "$SOURCES_FILE" 2>/dev/null; then
    die "Source '${name}' already registered. Remove it first with: ai-env source remove ${name}"
  fi
  
  # Append
  cat >> "$SOURCES_FILE" << EOF
  - name: "${name}"
    marketplace: "${marketplace}"
    plugin: "${plugin}"
EOF
  
  success "Registered source: ${BOLD}${name}${RESET} (${marketplace}/${plugin})"
}
```

**`cmd_source_remove`:** Remove a source by name from `sources.yaml`.

```bash
cmd_source_remove() {
  local name="${1:-}"
  [[ -z "$name" ]] && die "Usage: ai-env source remove <name>"
  
  if ! grep -q "name: .*${name}" "$SOURCES_FILE" 2>/dev/null; then
    die "Source '${name}' not found."
  fi
  
  # Use python to remove the block
  python3 -c "
import re
with open('$SOURCES_FILE') as f:
    content = f.read()

# Remove the block starting with '  - name: \"$name\"' until next '  - ' or end of sources
lines = content.split('\n')
result = []
skip = False
for line in lines:
    if '  - name:' in line and '$name' in line:
        skip = True
        continue
    if skip and (line.startswith('  - ') or (line and not line.startswith('    ') and not line.startswith('  - '))):
        skip = False
    if skip and line.startswith('    '):
        continue
    result.append(line)

with open('$SOURCES_FILE', 'w') as f:
    f.write('\n'.join(result))
"
  success "Removed source: ${BOLD}${name}${RESET}"
}
```

**Step 2: Verify source commands**

```bash
~/ai-env/ai-env source list
```
Expected: Warning about no sources or list of registered sources.

```bash
~/ai-env/ai-env source add superpowers superpowers-dev superpowers
~/ai-env/ai-env source list
```
Expected: Shows `superpowers  superpowers-dev/superpowers`.

```bash
~/ai-env/ai-env source remove superpowers
~/ai-env/ai-env source list
```
Expected: No sources (or warning).

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: add source management commands (list, add, remove)"
```

---

## Task 4: Plugin Version Resolution Helper

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Implement `resolve_latest_version`**

Given a path `~/.claude/plugins/cache/<marketplace>/<plugin>/`, return the latest version directory. For semver directories, sort by version. For commit-hash directories, sort by modification time.

```bash
# resolve_latest_version <cache_path>
# Prints the path to the latest version directory
resolve_latest_version() {
  local cache_path="$1"
  [[ ! -d "$cache_path" ]] && return 1
  
  python3 -c "
import os, re, sys
from pathlib import Path

cache = Path('$cache_path')
versions = [d for d in cache.iterdir() if d.is_dir()]
if not versions:
    sys.exit(1)

# Check if versions look like semver
semver_re = re.compile(r'^\d+\.\d+\.\d+$')
semver_versions = [v for v in versions if semver_re.match(v.name)]

if semver_versions:
    # Sort by semver (major, minor, patch)
    def semver_key(v):
        parts = v.name.split('.')
        return tuple(int(p) for p in parts)
    latest = max(semver_versions, key=semver_key)
else:
    # Hash-based: use modification time
    latest = max(versions, key=lambda v: v.stat().st_mtime)

print(latest)
"
}
```

**Step 2: Implement `find_skills_in_version`**

Given a version directory, find its skills (check both `skills/` and `.claude/skills/`).

```bash
# find_skills_in_version <version_dir>
# Prints skill directory paths, one per line
find_skills_in_version() {
  local version_dir="$1"
  
  # Check skills/ first (primary location)
  if [[ -d "${version_dir}/skills" ]]; then
    for skill_dir in "${version_dir}/skills"/*/; do
      [[ -d "$skill_dir" ]] && echo "$skill_dir"
    done
  fi
  
  # Also check .claude/skills/ (some plugins put skills here)
  if [[ -d "${version_dir}/.claude/skills" ]]; then
    for skill_dir in "${version_dir}/.claude/skills"/*/; do
      [[ -d "$skill_dir" ]] && echo "$skill_dir"
    done
  fi
}
```

**Step 3: Verify**

```bash
# Test with superpowers (semver)
bash -c 'source ~/ai-env/ai-env 2>/dev/null; resolve_latest_version ~/.claude/plugins/cache/superpowers-dev/superpowers'
```
Expected: `/Users/pieter/.claude/plugins/cache/superpowers-dev/superpowers/4.7.0`

```bash
# Test with frontend-design (hash-based)
bash -c 'source ~/ai-env/ai-env 2>/dev/null; resolve_latest_version ~/.claude/plugins/cache/claude-plugins-official/frontend-design'
```
Expected: Path to the most recently modified hash directory.

**Step 4: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: add plugin version resolution and skill discovery helpers"
```

---

## Task 5: Scan Command

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Implement `cmd_scan`**

Scan discovers skills from all registered sources and syncs them into `~/.agents/skills/` as symlinks. Skills already directly in `~/.agents/skills/` (not symlinks) are treated as local and left alone.

```bash
cmd_scan() {
  local verbose=false
  [[ "${1:-}" == "-v" || "${1:-}" == "--verbose" ]] && verbose=true
  
  [[ ! -d "$CANONICAL_STORE" ]] && mkdir -p "$CANONICAL_STORE"
  
  info "Scanning skill sources..."
  
  local total_synced=0
  local total_skipped=0
  
  # 1. Scan registered plugin sources
  if [[ -f "$SOURCES_FILE" ]]; then
    # Parse sources and scan each
    while IFS='|' read -r src_name src_marketplace src_plugin; do
      local cache_path="$HOME/.claude/plugins/cache/${src_marketplace}/${src_plugin}"
      
      if [[ ! -d "$cache_path" ]]; then
        warn "Source '${src_name}': cache not found at ${cache_path}"
        continue
      fi
      
      local version_dir
      version_dir=$(resolve_latest_version "$cache_path")
      if [[ -z "$version_dir" ]]; then
        warn "Source '${src_name}': no versions found"
        continue
      fi
      
      local src_count=0
      while IFS= read -r skill_path; do
        local skill_name
        skill_name=$(basename "$skill_path")
        local target="$CANONICAL_STORE/${skill_name}"
        
        if [[ -d "$target" && ! -L "$target" ]]; then
          # Local skill takes precedence -- don't overwrite
          [[ "$verbose" == true ]] && echo -e "  ${DIM}skip: ${skill_name} (local override)${RESET}"
          (( total_skipped++ )) || true
        elif [[ -L "$target" ]]; then
          # Already a symlink -- update if pointing elsewhere
          local current_target
          current_target=$(readlink "$target")
          local new_target="${skill_path%/}"
          if [[ "$current_target" != "$new_target" ]]; then
            rm "$target"
            ln -s "$new_target" "$target"
            [[ "$verbose" == true ]] && echo -e "  ${DIM}update: ${skill_name} -> ${new_target}${RESET}"
          fi
          (( src_count++ )) || true
        else
          # New skill -- create symlink
          ln -s "${skill_path%/}" "$target"
          [[ "$verbose" == true ]] && echo -e "  ${GREEN}+${RESET} ${skill_name} -> ${skill_path%/}"
          (( src_count++ )) || true
        fi
      done < <(find_skills_in_version "$version_dir")
      
      (( total_synced += src_count )) || true
      info "Source '${src_name}': ${src_count} skills from ${version_dir##*/}"
      
    done < <(parse_sources)
  fi
  
  # 2. Count local skills (direct dirs in canonical store, not symlinks)
  local local_count=0
  for item in "$CANONICAL_STORE"/*/; do
    [[ -d "$item" && ! -L "$item" ]] && (( local_count++ )) || true
  done
  
  # 3. Count total
  local total=0
  for item in "$CANONICAL_STORE"/*/; do
    [[ -d "$item" ]] && (( total++ )) || true
  done
  
  echo ""
  success "Scan complete: ${BOLD}${total}${RESET} skills in canonical store (${local_count} local, ${total_synced} from plugins)"
}
```

**Step 2: Implement `parse_sources` helper**

This reads `sources.yaml` and outputs `name|marketplace|plugin` lines:

```bash
parse_sources() {
  [[ ! -f "$SOURCES_FILE" ]] && return
  python3 -c "
in_sources = False
current = {}
for line in open('$SOURCES_FILE'):
    line = line.rstrip()
    if line.startswith('sources:'):
        in_sources = True
        continue
    if not in_sources:
        continue
    if line.startswith('  - '):
        if current.get('name'):
            print(f\"{current['name']}|{current.get('marketplace','')}|{current.get('plugin','')}\")
        current = {}
        rest = line[4:].strip()
        if rest.startswith('name:'):
            current['name'] = rest.split(':',1)[1].strip().strip('\"').strip(\"'\")
    elif line.startswith('    ') and ':' in line:
        k, v = line.strip().split(':', 1)
        current[k.strip()] = v.strip().strip('\"').strip(\"'\")
    elif line and not line.startswith(' ') and not line.startswith('#'):
        break
if current.get('name'):
    print(f\"{current['name']}|{current.get('marketplace','')}|{current.get('plugin','')}\")
"
}
```

**Step 3: Verify scan**

```bash
# First register a source, then scan
~/ai-env/ai-env source add superpowers superpowers-dev superpowers
~/ai-env/ai-env scan -v
```
Expected: Lists skills synced from superpowers, shows total count.

```bash
# Verify symlinks created
ls -la ~/.agents/skills/brainstorming
```
Expected: Symlink pointing to superpowers plugin cache.

**Step 4: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: implement scan command to sync plugin skills to canonical store"
```

---

## Task 6: Inventory Command

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Implement skill identifier derivation**

```bash
# derive_skill_id <dirname>
# For canonical store items: prefix = first segment before '-', name = rest
# No dash = local:<dirname>
derive_skill_id() {
  local dirname="$1"
  if [[ "$dirname" == *-* ]]; then
    local prefix="${dirname%%-*}"
    local name="${dirname#*-}"
    echo "${prefix}:${name}"
  else
    echo "local:${dirname}"
  fi
}
```

**Step 2: Implement `cmd_inventory`**

```bash
cmd_inventory() {
  local filter="${1:-}"
  
  if [[ ! -d "$CANONICAL_STORE" ]]; then
    die "Canonical store not found. Run ${CYAN}ai-env init${RESET} first."
  fi
  
  echo -e "${BOLD}Skill Inventory${RESET} (${DIM}~/.agents/skills/${RESET})\n"
  
  # Collect all skills with their IDs, grouped by prefix
  local -A prefix_counts
  local total=0
  
  # Build inventory using python for clean sorting/grouping
  python3 -c "
import os
from pathlib import Path
from collections import defaultdict

store = Path(os.path.expanduser('~/.agents/skills'))
skills = []
for item in sorted(store.iterdir()):
    if not item.is_dir():
        continue
    name = item.name
    if '-' in name:
        prefix = name.split('-', 1)[0]
        remainder = name.split('-', 1)[1]
    else:
        prefix = 'local'
        remainder = name
    
    source_type = 'symlink' if item.is_symlink() else 'local'
    target = str(item.resolve()) if item.is_symlink() else ''
    skills.append((prefix, remainder, name, source_type, target))

# Group by prefix
groups = defaultdict(list)
for prefix, remainder, dirname, stype, target in skills:
    groups[prefix].append((remainder, dirname, stype, target))

filter_pattern = '$filter'

for prefix in sorted(groups.keys()):
    items = groups[prefix]
    print(f'  \033[1m{prefix}\033[0m ({len(items)} skills)')
    for remainder, dirname, stype, target in items:
        skill_id = f'{prefix}:{remainder}'
        if filter_pattern and filter_pattern not in skill_id:
            continue
        marker = '  \033[2m(plugin)\033[0m' if stype == 'symlink' else ''
        print(f'    {skill_id}{marker}')
    print()

total = len(skills)
print(f'Total: {total} skills')
"
}
```

**Step 3: Verify**

```bash
~/ai-env/ai-env inventory
```
Expected: Grouped listing like:
```
  gws (41 skills)
    gws:admin-reports  (plugin)
    gws:calendar  (plugin)
    ...
  
  local (3 skills)
    local:feature-analyst
    ...
  
  Total: 92 skills
```

**Step 4: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: implement inventory command with prefix grouping"
```

---

## Task 7: Skill Pattern Matching Engine

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Implement `resolve_skills`**

This function takes a list of glob patterns and returns matching skill directory names from the canonical store.

```bash
# resolve_skills <env_file>
# Reads skill patterns from environment YAML, matches against canonical store
# Prints matching skill directory names (one per line)
resolve_skills() {
  local env_file="$1"
  
  python3 -c "
import os, fnmatch
from pathlib import Path

store = Path(os.path.expanduser('~/.agents/skills'))

# Read skill patterns from env file
patterns = []
in_skills = False
for line in open('$env_file'):
    line = line.rstrip()
    if line.startswith('skills:'):
        in_skills = True
        continue
    if in_skills and line.startswith('  - '):
        pattern = line[4:].strip().strip('\"').strip(\"'\")
        patterns.append(pattern)
    elif in_skills and line and not line.startswith(' ') and not line.startswith('#'):
        break

if not patterns:
    # No skills section = no skills activated
    exit(0)

# Build skill ID -> dirname mapping
skill_map = {}  # 'prefix:name' -> 'dirname'
for item in sorted(store.iterdir()):
    if not item.is_dir():
        continue
    dirname = item.name
    if '-' in dirname:
        prefix = dirname.split('-', 1)[0]
        remainder = dirname.split('-', 1)[1]
    else:
        prefix = 'local'
        remainder = dirname
    skill_id = f'{prefix}:{remainder}'
    skill_map[skill_id] = dirname

# Match patterns against skill IDs
matched = set()
for pattern in patterns:
    for skill_id, dirname in skill_map.items():
        if fnmatch.fnmatch(skill_id, pattern):
            matched.add(dirname)

# Print sorted matches
for dirname in sorted(matched):
    print(dirname)
"
}
```

**Step 2: Verify pattern matching**

Create a temporary test environment file and run resolution:

```bash
cat > /tmp/test-env.yaml << 'EOF'
name: "Test"
skills:
  - "gws:gmail*"
  - "local:*"
EOF
bash -c 'source ~/ai-env/ai-env 2>/dev/null; resolve_skills /tmp/test-env.yaml'
```

Expected output:
```
feature-analyst
gws-gmail
gws-gmail-forward
gws-gmail-reply
gws-gmail-reply-all
gws-gmail-send
gws-gmail-triage
gws-gmail-watch
typescript-javascript-interop
update-repos
```

Test wildcard-all pattern:
```bash
cat > /tmp/test-env2.yaml << 'EOF'
name: "All"
skills:
  - "*"
EOF
bash -c 'source ~/ai-env/ai-env 2>/dev/null; resolve_skills /tmp/test-env2.yaml | wc -l'
```
Expected: 92 (total skill count).

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: implement skill pattern matching with glob wildcards"
```

---

## Task 8: Activate Command

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Implement `cmd_activate`**

This is the core command. It: (1) runs scan, (2) resolves skill patterns, (3) rebuilds `~/.claude/skills/`, (4) prints summary.

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
  
  [[ -z "$name" ]] && die "Usage: ai-env activate <name> [--dry-run]"
  env_exists "$name" || die "Environment '${name}' not found. Run 'ai-env list' to see available environments."
  
  local file
  file="$(env_file "$name")"
  local display_name
  display_name=$(yaml_get "$file" "name")
  local directory
  directory=$(yaml_get "$file" "directory")
  [[ -n "$directory" ]] && directory="${directory/#\~/$HOME}"
  
  # Step 1: Scan sources (silent unless verbose)
  info "Scanning sources..."
  cmd_scan > /dev/null 2>&1 || true
  
  # Step 2: Resolve skills
  local matched_skills
  matched_skills=$(resolve_skills "$file")
  local skill_count
  skill_count=$(echo "$matched_skills" | grep -c . || echo 0)
  
  # Step 3: Count by prefix for summary
  local prefix_summary
  prefix_summary=$(echo "$matched_skills" | python3 -c "
import sys
from collections import Counter
counts = Counter()
for line in sys.stdin:
    dirname = line.strip()
    if not dirname:
        continue
    if '-' in dirname:
        prefix = dirname.split('-', 1)[0]
    else:
        prefix = 'local'
    counts[prefix] += 1
parts = [f'{prefix}: {count}' for prefix, count in sorted(counts.items())]
print(', '.join(parts))
")
  
  if [[ "$dry_run" == true ]]; then
    echo -e "\n${YELLOW}Dry run — would activate:${RESET}\n"
    echo -e "  Environment: ${BOLD}${display_name:-$name}${RESET}"
    [[ -n "$directory" ]] && echo -e "  Directory:   ${directory}"
    echo -e "  Skills:      ${skill_count} (${prefix_summary})"
    echo ""
    echo -e "  ${DIM}Would rebuild ~/.claude/skills/ with ${skill_count} symlinks${RESET}"
    echo ""
    echo -e "  Matched skills:"
    echo "$matched_skills" | while IFS= read -r s; do
      [[ -n "$s" ]] && echo -e "    ${s}"
    done
    return
  fi
  
  # Step 4: Rebuild ~/.claude/skills/
  # Remove all existing symlinks (but NOT bare files/dirs -- those should have been migrated by init)
  for item in "$CLAUDE_SKILLS"/*; do
    if [[ -L "$item" ]]; then
      rm "$item"
    fi
  done
  
  # Create new symlinks for matched skills
  local linked=0
  while IFS= read -r skill_dir; do
    [[ -z "$skill_dir" ]] && continue
    local source_path="$CANONICAL_STORE/${skill_dir}"
    local target_path="$CLAUDE_SKILLS/${skill_dir}"
    
    if [[ -d "$source_path" || -L "$source_path" ]]; then
      # Use relative symlink for cleanliness
      ln -s "../../.agents/skills/${skill_dir}" "$target_path"
      (( linked++ )) || true
    else
      warn "Skill directory not found: ${source_path}"
    fi
  done <<< "$matched_skills"
  
  # Step 5: Write active marker
  echo "$name" > "$ACTIVE_FILE"
  
  # Step 6: Read agent config for display
  local model permission_mode
  model=$(yaml_nested_get "$file" "agents.claude.model")
  permission_mode=$(yaml_nested_get "$file" "agents.claude.permission_mode")
  
  # Step 7: Print summary
  echo ""
  echo -e "${GREEN}✓${RESET} ${BOLD}Activated: ${display_name:-$name}${RESET}"
  [[ -n "$directory" ]] && echo -e "  Directory: ${directory}"
  echo -e "  Skills: ${linked} active (${prefix_summary})"
  if [[ -n "$model" || -n "$permission_mode" ]]; then
    local agent_info="claude"
    [[ -n "$model" ]] && agent_info="${agent_info} (${model}"
    [[ -n "$permission_mode" ]] && agent_info="${agent_info}, ${permission_mode} mode"
    [[ -n "$model" || -n "$permission_mode" ]] && agent_info="${agent_info})"
    echo -e "  Agent: ${agent_info}"
  fi
  echo ""
  if [[ -n "$directory" ]]; then
    echo -e "  ${DIM}cd ${directory} && claude${RESET}"
  else
    echo -e "  ${DIM}claude${RESET}"
  fi
  echo ""
}
```

**Step 2: Verify activate**

```bash
# Create a test environment first
mkdir -p ~/.config/ai-env/environments
cat > ~/.config/ai-env/environments/test-profile.yaml << 'EOF'
name: "Test Profile"
description: "Testing skill profile activation"
directory: "~/Dev"
skills:
  - "gws:gmail*"
  - "local:*"
agents:
  claude:
    model: "claude-sonnet-4-6"
    permission_mode: "plan"
EOF

~/ai-env/ai-env activate test-profile --dry-run
```

Expected:
```
Dry run -- would activate:

  Environment: Test Profile
  Directory:   /Users/pieter/Dev
  Skills:      10 (gws: 7, local: 3)
  
  Would rebuild ~/.claude/skills/ with 10 symlinks
  
  Matched skills:
    feature-analyst
    gws-gmail
    gws-gmail-forward
    ...
```

Then test actual activation:
```bash
~/ai-env/ai-env activate test-profile
ls -la ~/.claude/skills/ | head -20
```
Expected: Only matched symlinks present, old non-matching symlinks removed.

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: implement activate command with scan, resolve, and symlink rebuild"
```

---

## Task 9: Init Command (Migration)

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Implement the new `cmd_init`**

This performs one-time setup: creates directories, migrates bare files from `~/.claude/skills/`, auto-detects plugin sources.

```bash
cmd_init() {
  echo -e "${BOLD}Initializing ai-env skill profile manager...${RESET}\n"
  
  # 1. Create config directories
  mkdir -p "$CONFIG_DIR"
  mkdir -p "$ENV_DIR"
  mkdir -p "$CANONICAL_STORE"
  success "Config directory: ${CONFIG_DIR}"
  success "Environments: ${ENV_DIR}"
  success "Canonical store: ${CANONICAL_STORE}"
  
  # 2. Backup ~/.claude/skills/ if it exists and has content
  if [[ -d "$CLAUDE_SKILLS" ]] && [[ -n "$(ls -A "$CLAUDE_SKILLS" 2>/dev/null)" ]]; then
    local backup="$HOME/.claude/skills.bak.$(date +%Y-%m-%d)"
    if [[ ! -d "$backup" ]]; then
      cp -a "$CLAUDE_SKILLS" "$backup"
      success "Backed up ~/.claude/skills/ to ${backup}"
    else
      info "Backup already exists: ${backup}"
    fi
  fi
  
  # 3. Migrate bare files/dirs from ~/.claude/skills/ to ~/.agents/skills/
  local migrated=0
  if [[ -d "$CLAUDE_SKILLS" ]]; then
    for item in "$CLAUDE_SKILLS"/*; do
      [[ ! -e "$item" ]] && continue
      
      local basename
      basename=$(basename "$item")
      
      # Skip if it's already a symlink (already managed)
      [[ -L "$item" ]] && continue
      
      local target="$CANONICAL_STORE/${basename}"
      
      if [[ -f "$item" && "$item" == *.md ]]; then
        # Bare .md file -- wrap in a directory with SKILL.md naming
        local skill_name="${basename%.md}"
        local skill_dir="$CANONICAL_STORE/${skill_name}"
        if [[ ! -d "$skill_dir" ]]; then
          mkdir -p "$skill_dir"
          mv "$item" "$skill_dir/SKILL.md"
          # Replace original with symlink
          ln -s "../../.agents/skills/${skill_name}" "$CLAUDE_SKILLS/${skill_name}"
          info "Migrated file: ${basename} -> ${skill_name}/SKILL.md"
          (( migrated++ )) || true
        else
          warn "Skill '${skill_name}' already exists in canonical store, skipping ${basename}"
        fi
      elif [[ -d "$item" ]]; then
        # Bare directory -- move to canonical store, replace with symlink
        if [[ ! -d "$target" ]]; then
          mv "$item" "$target"
          ln -s "../../.agents/skills/${basename}" "$item"
          info "Migrated dir: ${basename}"
          (( migrated++ )) || true
        else
          warn "Skill '${basename}' already exists in canonical store, skipping"
        fi
      fi
    done
    
    if [[ $migrated -gt 0 ]]; then
      success "Migrated ${migrated} items to canonical store"
    else
      info "No bare files/dirs to migrate"
    fi
  fi
  
  # 4. Auto-detect plugin sources
  if [[ ! -f "$SOURCES_FILE" ]]; then
    echo "sources:" > "$SOURCES_FILE"
    
    local detected=0
    if [[ -d "$HOME/.claude/plugins/cache" ]]; then
      for marketplace_dir in "$HOME/.claude/plugins/cache"/*/; do
        local marketplace
        marketplace=$(basename "$marketplace_dir")
        for plugin_dir in "$marketplace_dir"/*/; do
          local plugin
          plugin=$(basename "$plugin_dir")
          
          # Check if any version has a skills/ directory
          local has_skills=false
          for version_dir in "$plugin_dir"/*/; do
            if [[ -d "${version_dir}skills" || -d "${version_dir}.claude/skills" ]]; then
              has_skills=true
              break
            fi
          done
          
          if [[ "$has_skills" == true ]]; then
            # Derive a short name: use plugin name, simplified
            local source_name="${plugin}"
            # Shorten common patterns
            source_name="${source_name/itp-engineering-/itp-eng-}"
            
            cat >> "$SOURCES_FILE" << EOF
  - name: "${source_name}"
    marketplace: "${marketplace}"
    plugin: "${plugin}"
EOF
            (( detected++ )) || true
            info "Detected source: ${source_name} (${marketplace}/${plugin})"
          fi
        done
      done
    fi
    
    if [[ $detected -gt 0 ]]; then
      success "Auto-registered ${detected} plugin sources"
    fi
    info "Edit sources with: ${CYAN}ai-env source list${RESET} / ${CYAN}ai-env source add${RESET}"
  else
    info "Sources file already exists: ${SOURCES_FILE}"
  fi
  
  # 5. Create example environment if none exist
  if [[ -z "$(ls -A "$ENV_DIR" 2>/dev/null)" ]]; then
    cat > "$ENV_DIR/example.yaml" << 'YAML'
name: "Example"
description: "An example environment -- edit or delete me"
directory: "~/projects/example"

skills:
  - "*"

# agents:
#   claude:
#     model: "claude-sonnet-4-6"
#     permission_mode: "plan"
YAML
    info "Created example environment: ${CYAN}ai-env show example${RESET}"
  fi
  
  # 6. Run initial scan
  echo ""
  cmd_scan
  
  echo ""
  info "Next steps:"
  echo -e "  ${CYAN}ai-env source list${RESET}           Review detected sources"
  echo -e "  ${CYAN}ai-env inventory${RESET}             See all discovered skills"
  echo -e "  ${CYAN}ai-env create my-project${RESET}     Create your first environment"
  echo -e "  ${CYAN}ai-env activate my-project${RESET}   Activate it"
}
```

**Step 2: Verify init**

IMPORTANT: Back up current state before testing. This modifies real files.

```bash
# Verify what would be migrated
for item in ~/.claude/skills/*; do
  if [ ! -L "$item" ]; then
    echo "WOULD MIGRATE: $(basename "$item")"
  fi
done
```

Expected:
```
WOULD MIGRATE: commit-push.md
WOULD MIGRATE: feature-analyst
WOULD MIGRATE: javascript-engineer.md
WOULD MIGRATE: typescript-javascript-interop
WOULD MIGRATE: update-repos
```

Then run init:
```bash
~/ai-env/ai-env init
```

Expected:
- Creates `~/.config/ai-env/`, `~/.config/ai-env/environments/`
- Backs up `~/.claude/skills/` to `~/.claude/skills.bak.2026-03-18/`
- Migrates 5 bare items to `~/.agents/skills/`
- Auto-detects ~7 plugin sources
- Runs scan

Verify migration results:
```bash
ls -la ~/.agents/skills/commit-push/SKILL.md
ls -la ~/.agents/skills/feature-analyst/SKILL.md
ls -la ~/.claude/skills/commit-push
```
Expected: `commit-push` is a symlink to `../../.agents/skills/commit-push`

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: implement init command with migration, source auto-detection, and initial scan"
```

---

## Task 10: Create Command (Updated)

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Rewrite `cmd_create` for new YAML format**

The create command now generates environment files in `~/.config/ai-env/environments/` with the new `skills:` and `agents:` sections instead of the old flat format.

```bash
cmd_create() {
  local name="${1:-}"
  [[ -z "$name" ]] && die "Usage: ai-env create <name>"
  
  env_exists "$name" && die "Environment '${name}' already exists. Use 'ai-env edit ${name}' to modify it."
  
  local file
  file="$(env_file "$name")"
  
  echo -e "${BOLD}Creating environment: ${CYAN}${name}${RESET}\n"
  
  read -rp "Display name [${name}]: " display_name
  display_name="${display_name:-$name}"
  
  read -rp "Description: " description
  
  read -rp "Working directory [~/Dev/${name}]: " directory
  directory="${directory:-~/Dev/${name}}"
  
  echo ""
  echo "Skill patterns (enter one per line, empty line to finish):"
  echo "  Examples: \"gws:*\", \"superpowers:*\", \"local:*\", \"*\" (all)"
  local skill_patterns=()
  while true; do
    read -rp "  - " pattern
    [[ -z "$pattern" ]] && break
    skill_patterns+=("$pattern")
  done
  
  # Default to all if none specified
  if [[ ${#skill_patterns[@]} -eq 0 ]]; then
    skill_patterns=("*")
    info "Defaulting to all skills (\"*\")"
  fi
  
  cat > "$file" << YAML
name: "${display_name}"
description: "${description}"
directory: "${directory}"

skills:
YAML
  
  for pattern in "${skill_patterns[@]}"; do
    echo "  - \"${pattern}\"" >> "$file"
  done
  
  cat >> "$file" << 'YAML'

# agents:
#   claude:
#     model: "claude-sonnet-4-6"
#     permission_mode: "plan"
#     system_prompt_file: "./CLAUDE.md"
YAML
  
  echo ""
  success "Created environment: ${BOLD}${name}${RESET}"
  info "Config: ${DIM}${file}${RESET}"
  echo -e "  Edit:     ${CYAN}ai-env edit ${name}${RESET}"
  echo -e "  Preview:  ${CYAN}ai-env activate ${name} --dry-run${RESET}"
  echo -e "  Activate: ${CYAN}ai-env activate ${name}${RESET}"
}
```

**Step 2: Verify**

```bash
# Run create with test input (or non-interactively via echo)
echo -e "Test Project\nA test project\n~/Dev/test\ngws:gmail*\nlocal:*\n" | ~/ai-env/ai-env create test-project
cat ~/.config/ai-env/environments/test-project.yaml
```

Expected: Valid YAML with skills section containing the two patterns.

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: update create command for new environment YAML format with skills patterns"
```

---

## Task 11: List, Show, Clone, Delete, Which Commands

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Update `cmd_list` to show skill count**

Add skill pattern count and active marker. The existing logic is mostly fine but needs the new env path and to show skill info.

```bash
cmd_list() {
  local envs active
  envs=$(list_envs)
  active=$(get_active)
  
  if [[ -z "$envs" ]]; then
    warn "No environments found."
    info "Run ${CYAN}ai-env create <name>${RESET} to get started."
    return
  fi
  
  echo -e "${BOLD}Environments:${RESET}\n"
  
  while IFS= read -r env_name; do
    local file
    file="$(env_file "$env_name")"
    local display_name description skill_count
    display_name=$(yaml_get "$file" "name")
    description=$(yaml_get "$file" "description")
    
    # Count skill patterns
    skill_count=$(yaml_list "$file" "skills" | wc -l | xargs)
    
    local marker=""
    [[ "$env_name" == "$active" ]] && marker=" ${GREEN}● active${RESET}"
    
    echo -e "  ${BOLD}${CYAN}${env_name}${RESET}${marker}"
    [[ -n "$display_name" && "$display_name" != "$env_name" ]] && echo -e "    ${display_name}" || true
    [[ -n "$description" ]] && echo -e "    ${DIM}${description}${RESET}" || true
    [[ "$skill_count" -gt 0 ]] && echo -e "    ${DIM}${skill_count} skill pattern(s)${RESET}" || true
    echo ""
  done <<< "$envs"
}
```

**Step 2: Update `cmd_show` to resolve and display skills**

```bash
cmd_show() {
  local name="${1:-}"
  [[ -z "$name" ]] && die "Usage: ai-env show <name>"
  env_exists "$name" || die "Environment '${name}' not found."
  
  local file
  file="$(env_file "$name")"
  local display_name directory
  display_name=$(yaml_get "$file" "name")
  directory=$(yaml_get "$file" "directory")
  
  echo -e "${BOLD}Environment: ${CYAN}${display_name:-$name}${RESET}\n"
  
  # Show config
  cat "$file"
  
  # Show resolved skills
  echo ""
  echo -e "${BOLD}Resolved skills:${RESET}"
  local resolved
  resolved=$(resolve_skills "$file")
  local count
  count=$(echo "$resolved" | grep -c . || echo 0)
  echo -e "  ${count} skills matched\n"
  
  echo "$resolved" | while IFS= read -r s; do
    [[ -n "$s" ]] && echo -e "  $(derive_skill_id "$s")"
  done
}
```

**Step 3: Keep `cmd_clone`, `cmd_delete`, `cmd_edit`, `cmd_which` mostly unchanged**

These just need the path updated to use `$ENV_DIR` instead of `$ENV_DIR` directly (which was the old flat `~/.config/ai-env/`).

The existing `cmd_clone`, `cmd_delete`, `cmd_edit`, `cmd_which` logic is fine -- just ensure `env_file()` returns the path under `environments/`.

**Step 4: Verify**

```bash
~/ai-env/ai-env list
~/ai-env/ai-env show test-profile
~/ai-env/ai-env which
~/ai-env/ai-env clone test-profile test-profile-2
~/ai-env/ai-env list
~/ai-env/ai-env delete test-profile-2
```

Expected: Each command works with the new directory structure.

**Step 5: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "feat: update list, show, clone, delete, which for new env structure"
```

---

## Task 12: Updated Usage/Help Text

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Rewrite `usage()` function**

```bash
usage() {
  cat << EOF
${BOLD}ai-env${RESET} v${VERSION} — Skill Profile Manager for AI Coding Agents

${BOLD}USAGE${RESET}
  ai-env <command> [arguments] [flags]

${BOLD}SETUP${RESET}
  ${CYAN}init${RESET}                        Initialize: migrate skills, detect sources, first scan
  ${CYAN}source${RESET} list                  Show registered plugin sources
  ${CYAN}source${RESET} add <name> <mkt> <p>  Register a plugin source
  ${CYAN}source${RESET} remove <name>         Unregister a plugin source

${BOLD}DISCOVERY${RESET}
  ${CYAN}scan${RESET}    [-v]                 Scan sources, sync to ~/.agents/skills/
  ${CYAN}inventory${RESET}                    List all skills with prefix grouping

${BOLD}ENVIRONMENTS${RESET}
  ${CYAN}create${RESET}  <name>               Create a new environment interactively
  ${CYAN}list${RESET}    (ls)                 List all environments
  ${CYAN}show${RESET}    <name>               Show config + resolved skills
  ${CYAN}edit${RESET}    <name>               Open environment config in \$EDITOR
  ${CYAN}activate${RESET} <name> [-n]         Scan + rebuild symlinks + print summary
  ${CYAN}clone${RESET}   <src> <dest>         Clone an environment config
  ${CYAN}delete${RESET}  <name>               Delete an environment
  ${CYAN}which${RESET}                        Show currently active environment

${BOLD}FLAGS${RESET}
  -n, --dry-run             Show what would happen without executing
  -v, --verbose             Show detailed output
  -h, --help                Show this help message

${BOLD}SKILL PATTERNS${RESET}
  Patterns use prefix:name format with glob wildcards:
    "gws:*"            all gws skills
    "gws:gmail*"       gws-gmail, gws-gmail-send, etc.
    "local:*"          all hand-written skills (no prefix dash)
    "superpowers:*"    all superpowers plugin skills
    "*"                everything

${BOLD}EXAMPLES${RESET}
  ai-env init                              # First-time setup
  ai-env create my-project                 # Create environment
  ai-env activate my-project               # Activate (sets up symlinks)
  ai-env activate my-project --dry-run     # Preview what would change
  ai-env source add sp superpowers-dev superpowers  # Register source
  ai-env inventory                         # See all available skills

${BOLD}DIRECTORIES${RESET}
  ${DIM}~/.agents/skills/${RESET}              Canonical skill store
  ${DIM}~/.claude/skills/${RESET}              Agent view (managed symlinks)
  ${DIM}~/.config/ai-env/${RESET}              Configuration
  ${DIM}~/.config/ai-env/environments/${RESET} Environment YAML files
  ${DIM}~/.config/ai-env/sources.yaml${RESET}  Plugin source registry

EOF
}
```

**Step 2: Verify**

```bash
~/ai-env/ai-env --help
```
Expected: Complete help text with all sections.

**Step 3: Commit**

```bash
cd ~/ai-env && git add ai-env
git commit -m "docs: update help text for skill profile manager commands"
```

---

## Task 13: Update INSTALL.md

**Files:**
- Modify: `/Users/pieter/ai-env/INSTALL.md`

**Step 1: Rewrite INSTALL.md for the new workflow**

Update to reflect the new commands and skill profile concept. Key sections:
- Quick install (same: copy to PATH)
- First-time setup: `ai-env init`
- Creating environments with skill patterns
- Source management
- Example YAML with new format

**Step 2: Verify**

Read through the file and confirm all commands and paths are accurate.

**Step 3: Commit**

```bash
cd ~/ai-env && git add INSTALL.md
git commit -m "docs: update INSTALL.md for skill profile manager v2"
```

---

## Task 14: End-to-End Verification

**Files:**
- No new files. Manual verification only.

**Step 1: Clean slate test**

```bash
# Backup current state
cp -a ~/.config/ai-env ~/.config/ai-env.bak.test 2>/dev/null || true

# Remove config to test fresh init
rm -rf ~/.config/ai-env

# Run init
~/ai-env/ai-env init
```

Expected:
- Creates all directories
- Backs up `~/.claude/skills/`
- Migrates bare files (commit-push.md, javascript-engineer.md -> directories with SKILL.md)
- Migrates bare dirs (feature-analyst, typescript-javascript-interop, update-repos)
- Auto-detects ~7 plugin sources
- Runs scan showing total skills

**Step 2: Verify inventory**

```bash
~/ai-env/ai-env inventory
```
Expected: All skills grouped by prefix (gws, persona, recipe, local, plus any from registered plugin sources like superpowers, itp, compound, etc.)

**Step 3: Create and activate a test environment**

```bash
~/ai-env/ai-env activate example --dry-run
```
Expected: Shows all skills since example uses `"*"`.

```bash
# Create a restricted environment manually
cat > ~/.config/ai-env/environments/minimal.yaml << 'EOF'
name: "Minimal"
description: "Just email skills"
skills:
  - "gws:gmail*"
  - "recipe:*email*"
EOF

~/ai-env/ai-env activate minimal --dry-run
```
Expected: Shows ~10-15 skills (gmail variants + email recipes).

```bash
~/ai-env/ai-env activate minimal
ls ~/.claude/skills/ | wc -l
```
Expected: Only matched skills present as symlinks.

**Step 4: Verify symlink integrity**

```bash
# Every item in ~/.claude/skills/ should be a valid symlink
for item in ~/.claude/skills/*; do
  if [[ -L "$item" ]]; then
    if [[ ! -e "$item" ]]; then
      echo "BROKEN: $item -> $(readlink "$item")"
    fi
  else
    echo "NOT A SYMLINK: $item"
  fi
done
```
Expected: No broken symlinks, no non-symlink items.

**Step 5: Switch back to full profile**

```bash
~/ai-env/ai-env activate example
ls ~/.claude/skills/ | wc -l
```
Expected: All skills restored.

**Step 6: Commit**

```bash
cd ~/ai-env && git add -A
git commit -m "feat: ai-env v2.0.0 — skill profile manager complete"
```

---

## Edge Cases and Gotchas

### Bare .md files in `~/.claude/skills/`
The current system has `commit-push.md` and `javascript-engineer.md` as bare files (not in directories). Claude Code expects skills in directories with `SKILL.md`. During init migration, these must be wrapped: `commit-push.md` -> `~/.agents/skills/commit-push/SKILL.md`.

### Plugin skills with same name as canonical store skills
If `superpowers` has `ai-self-reflecting` and `itp-springer-nature` also has `ai-self-reflecting`, the scan should not overwrite. The first source to create the symlink wins. Consider logging a warning when conflicts are detected.

### Multiple `.claude/skills/` AND `skills/` in same plugin
e.g., `inthepocket/itp-engineering-springer-nature/1.0.1/` has both `skills/` and `.claude/skills/`. The `find_skills_in_version` function handles this by scanning both, but skill name collisions within a single source should be handled (last one wins, or first one wins with warning).

### Hash-based versions (claude-plugins-official)
`frontend-design` uses commit hashes. `resolve_latest_version` uses mtime for non-semver directories. This is the best we can do without querying the plugin system.

### `~/.claude/skills/` might contain items NOT in canonical store after activation
After activate, only matched skills get symlinks. If someone manually added something to `~/.claude/skills/`, it will be removed (if symlink) or left (if bare). The init migration handles the initial bare items, but post-init, `~/.claude/skills/` should be treated as fully managed.

---

### Critical Files for Implementation
- `/Users/pieter/ai-env/ai-env` - The entire script to rewrite; all 14 tasks modify this file
- `/Users/pieter/ai-env/docs/plans/2026-03-17-skill-profile-manager-design.md` - Design spec to reference throughout implementation
- `/Users/pieter/ai-env/INSTALL.md` - Documentation to update for new commands and workflow
- `/Users/pieter/.claude/plugins/cache/superpowers-dev/superpowers/4.7.0/skills/writing-plans/SKILL.md` - Plan format reference to follow
- `/Users/pieter/.claude/skills/` - Target directory whose current state (92 symlinks + 5 bare items) determines migration logic

