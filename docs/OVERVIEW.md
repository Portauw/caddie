# caddie — Visual Guide

> **TL;DR** — caddie is a **skill profile manager** for Claude Code (and other coding agents).
> It gives every project its own curated toolbox of skills, pulled from git repos, wired up with symlinks.

---

## 🧩 The Problem

Claude Code reads skills from `.claude/skills/`. Without caddie you either have:

- **One global pile** in `~/.claude/skills/` — every project sees every skill, noisy and wrong-tool-for-the-job, or
- **Manual copies** per project — drift, stale forks, no shared source.

```
┌──────────────────────────────────────────────────────────────┐
│  ~/.claude/skills/   (before caddie)                         │
├──────────────────────────────────────────────────────────────┤
│  📦 brainstorming/         📦 gmail-send/                    │
│  📦 fastapi-review/        📦 figma-export/                  │
│  📦 jira-ticket/           📦 react-component/               │
│  📦 terraform-plan/        📦 slack-summary/                 │
│  📦 ...70 more...          📦 ...a mess...                   │
└──────────────────────────────────────────────────────────────┘
```

| ❌ Problem | 🤕 Consequence |
|---|---|
| All skills loaded everywhere | Claude picks the wrong skill for the job |
| Skills scattered across many git repos | Manual clone + copy, sync drift |
| No per-project config | Model, permissions, CLAUDE.md all global |
| Can't share a setup | "Works on my machine" |
| Symlinks break in Docker / Lambda | Can't ship skills to prod runtimes |

---

## ✨ What caddie Does

```
┌──────────────┐   ┌──────────────────┐   ┌───────────────────┐
│  Skill repos │   │  caddie index    │   │ Per-project skills│
│              │──▶│                  │──▶│                   │
│ git repos    │   │ ~/.config/       │   │ <project>/        │
│ (many)       │   │   caddie/skills/ │   │   .agents/skills/ │
│              │   │ (symlink layer)  │   │ (filtered symlinks)│
└──────────────┘   └──────────────────┘   └───────────────────┘
     clone+pull       namespace               activate
                      (prefix:name)           (pattern match)
```

**One sentence:** *Scan many skill repos → index them under one namespace → activate a curated subset per project directory as symlinks.*

---

## 🏗️ Architecture at a Glance

```mermaid
flowchart LR
    subgraph S["🔍 Skill repos (sources.yaml)"]
        S1["superpowers"]
        S2["sterling"]
        S3["gws"]
        S4["lenny"]
    end

    subgraph R["📥 Local clones"]
        RC["~/.config/caddie/repos/<br/>superpowers/<br/>sterling-skills/<br/>gws/<br/>lenny/"]
    end

    subgraph I["🗂️ caddie index (namespaced)"]
        IX["~/.config/caddie/skills/<br/>── symlinks, prefix:name ──<br/>superpowers-brainstorming → repo<br/>sterling-write-as-pieter → repo<br/>gws-gmail-send → repo"]
    end

    subgraph E["🎯 Environments (profiles)"]
        E1["backend-api.yaml<br/>skills: superpowers:*, gws:*"]
        E2["frontend.yaml<br/>skills: superpowers:ui-*, sterling:*"]
    end

    subgraph P["📁 Project directory"]
        P1[".caddie.yaml → backend-api"]
        PA[".agents/skills/<br/>(filtered symlinks into index)"]
        PC[".claude/skills → .agents/skills"]
    end

    S1 & S2 & S3 & S4 -->|git clone / pull| RC --> IX
    IX -->|pattern match| E1 & E2
    E1 --> P1
    P1 -->|caddie activate| PA
    PA --> PC
```

---

## 🔑 Four Core Concepts

### 1. **Skill repos** — where skills come from

Declared in `~/.config/caddie/sources.yaml`:

```yaml
repos:
  - name: superpowers
    url: https://github.com/Portauw/superpowers.git
    skills_path: skills
    prefix: superpowers
  - name: sterling-skills
    url: https://github.com/Portauw/sterling-skills.git
    skills_path: skills
    prefix: sterling
```

Repos are cloned to `~/.config/caddie/repos/<name>/` and auto-pulled on activate.

### 2. **caddie index** — one namespaced view of all skills

```
~/.config/caddie/skills/
  ├── superpowers-brainstorming   → ~/.config/caddie/repos/superpowers/skills/brainstorming
  ├── sterling-write-as-pieter    → ~/.config/caddie/repos/sterling-skills/skills/write-as-pieter
  └── gws-gmail-send              → ~/.config/caddie/repos/gws/skills/gmail-send
```

Everything is a **symlink**, prefixed by source. This is the layer profiles filter against.

### 3. **Environments** — named skill profiles

```yaml
# ~/.config/caddie/environments/backend-api.yaml
name: "Backend API"
skills:
  - "superpowers:*"        # all superpowers
  - "gws:gmail-*"          # just gmail skills
  - "sterling:*"           # all sterling skills
```

### 4. **Bindings** — glue a directory to a profile

```
~/Dev/my-backend/
  ├── .caddie.yaml         ← environment: "backend-api"
  ├── .agents/skills/      ← created by caddie activate
  ├── .claude/skills  ───▶ .agents/skills
  └── src/
```

Run `caddie activate` from anywhere inside that tree and the right profile lights up.

---

## ⚡ The Activation Pipeline

```
    ┌─────────────────────────────────────────────────────────┐
    │  $ cd ~/Dev/my-backend && caddie activate               │
    └──────────────────────────┬──────────────────────────────┘
                               │
                               ▼
          ┌────────────────────────────────────────┐
          │ 1. Read .caddie.yaml                   │
          │    → environment: "backend-api"        │
          └────────────────────────────────────────┘
                               │
                               ▼
          ┌────────────────────────────────────────┐
          │ 2. git pull every registered repo      │
          │    → ~/.config/caddie/repos/*          │
          └────────────────────────────────────────┘
                               │
                               ▼
          ┌────────────────────────────────────────┐
          │ 3. Rebuild the index                   │
          │    → ~/.config/caddie/skills/*         │
          │      (symlinks, namespaced)            │
          └────────────────────────────────────────┘
                               │
                               ▼
          ┌────────────────────────────────────────┐
          │ 4. Resolve profile patterns            │
          │    "superpowers:*" → 22 skills         │
          │    "gws:gmail-*"   →  3 skills         │
          │    "sterling:*"    →  5 skills         │
          │    ═══════════════════════════════     │
          │    Total: 30 active skills             │
          └────────────────────────────────────────┘
                               │
                               ▼
          ┌────────────────────────────────────────┐
          │ 5. Wire up project symlinks            │
          │    <proj>/.agents/skills/*  →  index   │
          │    <proj>/.claude/skills    →  .agents/│
          │                                skills  │
          └────────────────────────────────────────┘
                               │
                               ▼
                    ✅ Ready. Launch Claude.
```

Each project has its **own** `.agents/skills/` — a different filtered view of the same index.

---

## 🎯 Problems Solved

| Problem | How caddie solves it |
|---|---|
| 🔊 Skill noise across projects | Each project only symlinks its profile's skills |
| 🗂️ Skills scattered across many repos | Unified via `sources.yaml`, namespaced `prefix:name` |
| 🔁 Copy-paste skill sync | Repos auto-pull on activate |
| 👥 Team can't share setups | Profiles are YAML, check them into a repo |
| 🐳 Symlinks don't ship to Docker/Lambda | `caddie export` resolves links, copies real files (local or S3) |
| 🤷 "Which skills am I running?" | `caddie which` shows the active profile and resolved skills |

---

## 🚀 A Day in the Life

```
Morning                                      Afternoon
───────                                      ─────────
$ cd ~/Dev/backend-api                       $ cd ~/Dev/marketing-site
$ caddie activate                            $ caddie activate
  ✓ Backend API                                ✓ Frontend
  Skills: 30 active                            Skills: 18 active
  (superpowers:22, gws:3, sterling:5)          (superpowers:12, sterling:6)

$ claude                                     $ claude
  → sees only backend-relevant skills          → sees only frontend-relevant skills
```

Same laptop. Same Claude. Two **completely different toolkits**, activated by `cd`.

---

## 📤 Shipping Skills to Production

Symlinks don't survive Docker builds or Lambda packaging. `caddie export` resolves every link and copies the real `SKILL.md` files:

```
┌────────────────────────┐       ┌──────────────────────────┐
│ <project>/.agents/     │       │  ./dist/skills/          │
│     skills/            │──────▶│   (flat, real files)     │
│  (symlink tree)        │ resolve│                          │
│                        │ +copy │  superpowers-*/SKILL.md  │
│                        │       │  gws-*/SKILL.md          │
└────────────────────────┘       └──────────────────────────┘
                                           │
                                           ▼
                              🐳 Docker  ☁️ Lambda  🔄 CI/CD
```

```bash
caddie export backend-api --to ./dist/skills/
caddie export backend-api --to s3://my-bucket/skills/
```

---

## 🧭 Mental Model (one picture)

```
┌──────────────┐   scan    ┌──────────────┐  activate  ┌────────────────┐
│  SKILL REPOS │  ───────▶ │  caddie      │  ────────▶ │  PROJECT       │
│              │           │  INDEX       │  (filter)  │                │
│  git repos   │           │              │            │  .agents/      │
│  cloned to   │           │  ~/.config/  │            │    skills/     │
│  ~/.config/  │           │  caddie/     │            │  .claude/      │
│  caddie/     │           │  skills/     │            │    skills →    │
│  repos/      │           │  (symlinks,  │            │    .agents/... │
│              │           │   namespaced)│            │                │
└──────────────┘           └──────────────┘            └────────────────┘
       ▲                          ▲                           ▲
       │                          │                           │
   sources.yaml              prefix:name                 .caddie.yaml
                          namespacing rules          (binds dir → profile)
```

The index is not a "source of truth" — it's a **unified namespaced view**. The truth lives in the source repos.

---

## 📚 Where to go next

- **Quick start:** [README.md](../README.md#quick-install)
- **Profile examples:** [`examples/`](../examples/)
- **Architecture decisions:** [`docs/adr/`](./adr/)
- **Commands cheat sheet:** [README → All Commands](../README.md#all-commands)
