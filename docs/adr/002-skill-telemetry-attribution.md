| **Status**  | DECIDED (no change)                    |
|-------------|----------------------------------------|
| **Date**    | 2026-08-31                             |
| **Owner**   | Pieter Portauw                         |
| **Driver**  | Plugin attribution in skill telemetry  |

## Context and Problem Statement

Claude Code exports OpenTelemetry logs.

Skills reach an engineer two ways. Either as a marketplace plugin from the `mac-soft` marketplace, or as a caddie symlink under `.claude/skills/`. Both deliver the same skills from the same git repos, but they do not produce the same telemetry. 

## What the telemetry actually contains

Measured against a local OTLP receiver on Claude Code 2.1.251, with a probe skill loaded four different ways.

Each activation arrives as one OTLP log record, `body = "claude_code.skill_activated"`, with a flat attribute list:

| Attribute | Marketplace plugin | caddie symlink |
|---|---|---|
| `skill.name` | `engineering-backend:code-review` | `code-review` |
| `skill.source` | `plugin` | `projectSettings` (or `userSettings`) |
| `plugin.name` | `engineering-backend` | key absent from the payload |
| `marketplace.name` | `mac-soft` | key absent from the payload |

The plugin attributes are not sent as null. The keys are missing entirely, so nothing downstream can recover them.

Four further findings that constrain any fix:

1. `skill.name` is taken from the **store folder name**, not from the `name:` field in `SKILL.md`. This confirms the naming layer described in ADR 001 is the layer telemetry observes.
2. All of these attributes are gated behind `OTEL_LOG_TOOL_DETAILS`. Unset (the default) every non-Anthropic skill collapses to the literal `custom_skill` and the plugin keys disappear. 
3. On the **metrics** side (`claude_code.cost.usage`, `claude_code.token.usage`) the behaviour inverts. Non-Anthropic plugin names are redacted to the literal `third-party`, while a caddie symlink reports its real folder name. Loading skills as plugins would make cost and token attribution worse, not better.
4. A colon is a legal character in a skill folder name. A folder named `engineering-backend:probe-skill` loads correctly and reports that exact string as `skill.name`.

## How the report consumes it

`qall.sql` in the `skill-usage-report` repo:

```sql
IFNULL(plugin_name,'(no plugin)')            AS plugin_name,
REGEXP_REPLACE(skill_name, r'^[^:]+:', '')   AS skill,
```

Plugin identity comes from its own column and is never parsed out of the skill name. The skill name has everything before the first colon stripped, which means the **skill** dimension already matches across both delivery paths today. Only the **plugin** split is broken.

## Decision

No change to caddie.

The only mechanism that makes Claude Code emit `plugin.name` is loading the skill as a plugin. For caddie that means generating a plugin manifest per source repo and wrapping the store symlinks in it. That work is not justified by the reporting benefit it buys, so it is not being done, and no partial alternative is being adopted in its place.

## Alternatives considered

### Synthetic per-repo plugin wrappers
Generate `<repo>/.claude-plugin/plugin.json` per source repo, with `skills/` holding the existing store symlinks, and load them with repeatable `--plugin-dir`.

Verified working. It reports `skill.source: plugin`, the correct `plugin.name`, keeps caddie's per-skill filtering (unlike a real plugin install, which is all-or-nothing per plugin), and adds a `plugin_loaded` event carrying `skill_path_count`, which would give the report a denominator for "available but never used".

Rejected. The reporting gain does not justify wrapping caddie's model in Anthropic's plugin packaging, and it drags launch-side changes along with it since `--plugin-dir` is a CLI flag.

### Colon naming: `<plugin-name>:<skill>` folder names
Name store folders after the plugin, joined with a colon, so `skill.name` arrives as `engineering-backend:code-review`, byte-identical to what a plugin user sends. The existing `REGEXP_REPLACE` then yields the same `skill` value as today, and one extra expression recovers the plugin:

```sql
IFNULL(plugin_name, REGEXP_EXTRACT(skill_name, r'^([^:]+):')) AS plugin_name,
```

caddie could derive the prefix automatically: every cloned repo already carries `.claude-plugin/plugin.json` with the canonical plugin name, so no mapping table would be needed in `sources.yaml`.

Not taken. It works, but it only pays off with a matching change on the ingestion side, and colons in folder names are a portability liability outside macOS and Linux. Left on the table rather than rejected outright.

### Dash prefix on every repo (extending ADR 001 to all repos)
Set `prefix: "true"` everywhere so folders become `eng-backend-code-review`.

Rejected, and worth recording as actively harmful. `qall.sql` only strips a **colon** prefix. A dash-prefixed name survives the regex intact and would stop matching the plugin users' `code-review` rows, breaking the one dimension that currently lines up.

### Fix it downstream only, leave caddie alone
Not possible for bare folder names. `code-review` is shipped by both `engineering` and `engineering-backend`, so the name alone cannot be attributed.

## Consequences

- caddie-delivered skills stay in the report's `(no plugin)` bucket. Per-skill totals remain correct across both delivery paths; the per-plugin and per-practice split stays wrong for the caddie half.
- The gap is a property of how Claude Code classifies anything under `.claude/skills/`, not of caddie specifically. Any tool that symlinks skills into that directory inherits it.
