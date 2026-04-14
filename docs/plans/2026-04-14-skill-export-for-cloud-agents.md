# `ai-env export` — Implementation Plan

> **Status:** Implemented 2026-04-14

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an `ai-env export` command that resolves skills (following symlink chains) and copies the real SKILL.md files to a target directory or S3 bucket.

**Architecture:** Reuses ai-env's existing pattern-matching (`PY_PREFIX_HELPER`) and environment configs. Resolves each matched skill's symlink chain to the real file via `Path.resolve()`, then copies it to the target. Two target types: local directory (`cp`) and S3 (`aws s3 cp`).

**Tech Stack:** Bash (extends existing ai-env script), Python 3 (reuses existing inline helpers), AWS CLI (for S3 target)

---

## Context

ai-env manages skills via multi-level symlink chains:

```
~/.config/ai-env/skills/gws-calendar
  → ~/.config/ai-env/repos/gws/skills/gws-calendar/SKILL.md  (real file)
```

This works for local agents (Claude Code, etc.) but breaks for:
- Docker containers (symlink targets don't exist inside the container)
- Cloud runtimes (Lambda, ECS) that need skills in S3 or on disk
- CI/CD pipelines that need reproducible skill sets

`ai-env export` resolves skills to real files and copies them to any target.

### Usage

```bash
# Export an environment's skills to a local directory
ai-env export my-project --to ./exported-skills/

# Export directly to S3
ai-env export my-project --to s3://my-bucket/skills/

# Export all skills (no environment filter)
ai-env export --all --to /tmp/all-skills/

# Preview without copying
ai-env export my-project --to s3://my-bucket/skills/ --dry-run

# Remove stale skills from target that are no longer in the export set
ai-env export my-project --to ./skills/ --clean
```

### Files involved

- **Modify:** `/Users/pieter/ai-env/ai-env` — add `cmd_export`, `_export_to_dir`, `_export_to_s3`, wire into main dispatch and help text

---

### Task 1: Add `cmd_export` with argument parsing and skill resolution

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env`

**Step 1: Add the export command function**

Add before the `usage()` function. This handles argument parsing and resolves skills to `(dirname, real_path)` pairs using the existing `PY_PREFIX_HELPER`.

```bash
cmd_export() {
  local name=""
  local target=""
  local dry_run=false
  local clean=false
  local all=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --to)         target="$2"; shift 2 ;;
      --dry-run|-n) dry_run=true; shift ;;
      --clean)      clean=true; shift ;;
      --all)        all=true; shift ;;
      -*) die "Unknown flag: $1" ;;
      *)  name="$1"; shift ;;
    esac
  done

  [[ -z "$target" ]] && die "Usage: ai-env export [<env>|--all] --to <dir-or-s3> [--clean] [--dry-run]"

  if [[ "$all" == false && -z "$name" ]]; then
    die "Specify an environment name or use --all.\nUsage: ai-env export [<env>|--all] --to <target> [--clean] [--dry-run]"
  fi

  if [[ "$all" == false ]]; then
    env_exists "$name" || die "Environment '${name}' not found."
  fi

  # Resolve skills to (dirname \t real_path) pairs
  local env_path=""
  [[ "$all" == false ]] && env_path="$(env_file "$name")"

  local resolve_output
  resolve_output=$(python3 -c "
$PY_PREFIX_HELPER
import fnmatch

store = Path(os.path.expanduser('$SKILL_STORE'))
source_map, repo_map = _build_maps(os.path.expanduser('$SOURCES_FILE'))

use_all = $( [[ "$all" == true ]] && echo "True" || echo "False" )
env_file = '$env_path'

# Read patterns from environment config, or match everything
patterns = ['*:*'] if use_all else []
if not use_all and env_file:
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
    exit(0)

# Resolve each skill to its real SKILL.md path
for item in sorted(store.iterdir()):
    if not item.is_dir() and not item.is_symlink():
        continue
    skill_md = item / 'SKILL.md'
    try:
        real_path = skill_md.resolve()
    except OSError:
        continue
    if not real_path.exists():
        continue
    prefix, remainder = _get_prefix(item, source_map, repo_map)
    skill_id = f'{prefix}:{remainder}'
    if any(fnmatch.fnmatch(skill_id, p) for p in patterns):
        print(f'{item.name}\t{real_path}')
")

  if [[ -z "$resolve_output" ]]; then
    warn "No skills matched."
    return
  fi

  local skill_count
  skill_count=$(echo "$resolve_output" | grep -c . || echo 0)

  # Route to target-specific export
  if [[ "$target" == s3://* ]]; then
    _export_to_s3 "$target" "$resolve_output" "$clean" "$dry_run" "$skill_count"
  else
    _export_to_dir "$target" "$resolve_output" "$clean" "$dry_run" "$skill_count"
  fi
}
```

**Step 2: Wire into main dispatch**

Add to the `main()` case statement (around line 2110):

```bash
    export)                  shift || true; cmd_export "$@" ;;
```

**Step 3: Test argument parsing**

Run: `ai-env export` — should show usage error.
Run: `ai-env export nonexistent --to /tmp/test` — should show "not found".
Run: `ai-env export --all --to /tmp/test --dry-run` — should resolve skills (no output yet since export helpers aren't implemented).

**Step 4: Commit**

```bash
cd ~/ai-env
git add ai-env
git commit -m "feat: add export command with argument parsing and skill resolution"
```

---

### Task 2: Implement local directory export

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env` (add `_export_to_dir` helper before `cmd_export`)

**Step 1: Add the helper function**

```bash
_export_to_dir() {
  local target="$1" resolve_output="$2" clean="$3" dry_run="$4" skill_count="$5"
  local exported=0 removed=0

  if [[ "$clean" == true && -d "$target" ]]; then
    local matched_names
    matched_names=$(echo "$resolve_output" | cut -f1)
    for existing in "$target"/*/; do
      [[ ! -d "$existing" ]] && continue
      local dir_name
      dir_name=$(basename "$existing")
      if ! echo "$matched_names" | grep -qx "$dir_name"; then
        if [[ "$dry_run" == true ]]; then
          echo -e "  ${RED}remove${RESET} ${dir_name}/"
        else
          rm -rf "$existing"
        fi
        (( removed++ )) || true
      fi
    done
  fi

  while IFS=$'\t' read -r skill_name real_path; do
    [[ -z "$skill_name" ]] && continue
    local dest="$target/${skill_name}"
    if [[ "$dry_run" == true ]]; then
      echo -e "  ${GREEN}copy${RESET}   ${skill_name}/SKILL.md  ${DIM}← ${real_path}${RESET}"
    else
      mkdir -p "$dest"
      cp "$real_path" "$dest/SKILL.md"
    fi
    (( exported++ )) || true
  done <<< "$resolve_output"

  if [[ "$dry_run" == true ]]; then
    echo ""
    info "Dry run: would export ${exported} skills, remove ${removed}"
  else
    success "Exported ${exported} skills to ${target}/"
    [[ "$removed" -gt 0 ]] && info "Removed ${removed} stale skills"
  fi
}
```

**Step 2: Test local export**

```bash
ai-env export --all --to /tmp/skill-export --dry-run
# Expected: lists skills with "copy" prefix

ai-env export --all --to /tmp/skill-export
# Expected: creates /tmp/skill-export/<skill>/SKILL.md for each skill

ls /tmp/skill-export/gws-calendar/SKILL.md
# Expected: file exists with real content

head -3 /tmp/skill-export/gws-calendar/SKILL.md
# Expected: YAML frontmatter, not a symlink error
```

**Step 3: Test --clean**

```bash
mkdir -p /tmp/skill-export/fake-stale-skill
ai-env export --all --to /tmp/skill-export --clean
ls /tmp/skill-export/fake-stale-skill
# Expected: directory removed
```

**Step 4: Commit**

```bash
cd ~/ai-env
git add ai-env
git commit -m "feat: implement local directory export with --clean support"
```

---

### Task 3: Implement S3 export

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env` (add `_export_to_s3` helper before `cmd_export`)

**Step 1: Add the helper function**

```bash
_export_to_s3() {
  local target="$1" resolve_output="$2" clean="$3" dry_run="$4" skill_count="$5"
  local exported=0 removed=0

  local bucket prefix
  bucket=$(echo "$target" | sed 's|s3://||' | cut -d/ -f1)
  prefix=$(echo "$target" | sed "s|s3://${bucket}/||" | sed 's|/$||')

  local aws_flags=()
  [[ -n "${AWS_PROFILE:-}" ]] && aws_flags+=(--profile "$AWS_PROFILE")
  [[ -n "${AWS_ENDPOINT_URL:-}" ]] && aws_flags+=(--endpoint-url "$AWS_ENDPOINT_URL")

  if [[ "$clean" == true ]]; then
    local matched_names
    matched_names=$(echo "$resolve_output" | cut -f1)
    local existing_skills
    existing_skills=$(aws "${aws_flags[@]}" s3 ls "s3://${bucket}/${prefix}/" 2>/dev/null \
      | awk '/PRE/ {gsub(/\/$/, "", $2); print $2}') || true

    while IFS= read -r existing; do
      [[ -z "$existing" ]] && continue
      if ! echo "$matched_names" | grep -qx "$existing"; then
        if [[ "$dry_run" == true ]]; then
          echo -e "  ${RED}remove${RESET} s3://${bucket}/${prefix}/${existing}/"
        else
          aws "${aws_flags[@]}" s3 rm "s3://${bucket}/${prefix}/${existing}/" --recursive > /dev/null 2>&1
        fi
        (( removed++ )) || true
      fi
    done <<< "$existing_skills"
  fi

  while IFS=$'\t' read -r skill_name real_path; do
    [[ -z "$skill_name" ]] && continue
    local s3_key="${prefix}/${skill_name}/SKILL.md"
    if [[ "$dry_run" == true ]]; then
      echo -e "  ${GREEN}upload${RESET} s3://${bucket}/${s3_key}  ${DIM}← ${real_path}${RESET}"
    else
      aws "${aws_flags[@]}" s3 cp "$real_path" "s3://${bucket}/${s3_key}" > /dev/null 2>&1
    fi
    (( exported++ )) || true
  done <<< "$resolve_output"

  if [[ "$dry_run" == true ]]; then
    echo ""
    info "Dry run: would upload ${exported} skills, remove ${removed}"
  else
    success "Exported ${exported} skills to s3://${bucket}/${prefix}/"
    [[ "$removed" -gt 0 ]] && info "Removed ${removed} stale skills"
  fi
}
```

**Step 2: Test with LocalStack**

```bash
AWS_ENDPOINT_URL=http://localhost:4566 ai-env export --all --to s3://sterling-config/skills/ --dry-run
# Expected: shows "upload" lines for each skill

AWS_ENDPOINT_URL=http://localhost:4566 ai-env export --all --to s3://sterling-config/skills/
# Expected: uploads resolved files

aws --endpoint-url=http://localhost:4566 s3 ls s3://sterling-config/skills/gws-calendar/
# Expected: SKILL.md file exists
```

**Step 3: Commit**

```bash
cd ~/ai-env
git add ai-env
git commit -m "feat: implement S3 export target with --clean support"
```

---

### Task 4: Add help text

**Files:**
- Modify: `/Users/pieter/ai-env/ai-env` (inside `usage()` function)

**Step 1: Add export to the COMMANDS and details sections**

In the commands list:
```
    export               Export resolved skills to a directory or S3 bucket
```

Add a details section:
```
${BOLD}EXPORT${RESET}
  ai-env export <env> --to <dir>            Copy resolved skills to local directory
  ai-env export <env> --to s3://bucket/p/   Upload resolved skills to S3
  ai-env export --all --to <target>         Export all skills (no env filter)
  Flags: --clean (remove stale), --dry-run (preview)
  Env:   AWS_PROFILE, AWS_ENDPOINT_URL (for S3 targets)
```

**Step 2: Commit**

```bash
cd ~/ai-env
git add ai-env
git commit -m "docs: add export command to help text"
```

---

## Verification

1. **Local export:** `ai-env export --all --to /tmp/test && head -3 /tmp/test/gws-calendar/SKILL.md` — shows real YAML frontmatter
2. **S3 export:** `AWS_ENDPOINT_URL=http://localhost:4566 ai-env export --all --to s3://sterling-config/skills/ && aws --endpoint-url=http://localhost:4566 s3 ls s3://sterling-config/skills/ | wc -l` — shows 190+ skill prefixes
3. **Environment filter:** `ai-env export sterling --to /tmp/test --dry-run` — shows only skills matching the sterling environment patterns
4. **Clean removes stale:** Create a fake skill dir in target, run with `--clean`, verify it's gone
5. **Dry run is safe:** `--dry-run` never creates/deletes files
6. **Respects AWS_PROFILE:** Works with production S3 when `AWS_PROFILE=portauw` is set
