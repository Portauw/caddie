# Changelog

## [Unreleased]

### Added

- **Cross-platform support** — caddie now runs on macOS, Linux, and
  Windows 10/11 in addition to the original macOS/Linux targets.
- `internal/platform` package — build-tagged abstractions for symlink
  creation (`Materialize`), link detection (`IsLinked`), link target
  reading (`ReadTarget`), filesystem root detection (`FilesystemRoot`),
  editor invocation (`RunEditor`), and recursive tree copy (`CopyTree`).
- **Windows NTFS junction fallback** — when `os.Symlink` is unavailable
  (Developer Mode not enabled), caddie automatically falls back to NTFS
  directory junctions via `mklink /J`. A one-time notice is printed. All
  standard skill management operations work correctly with junctions.
- `scripts/build.ps1` — PowerShell build script for Windows
  (`pwsh ./scripts/build.ps1`). Accepts `-Cross` to also cross-compile
  Linux and macOS binaries.
- `scripts/build.sh --cross` — cross-compiles `dist/caddie.exe`
  (Windows/amd64) from a Unix host.
- GitHub Actions CI matrix covering `ubuntu-latest`, `macos-latest`,
  `windows-latest` (Developer Mode on), and `windows-latest` (junction
  fallback path).

### Fixed

- `caddie activate` / `caddie scan` — `FindProjectConfig` walk-up no
  longer spins indefinitely on Windows; the loop now terminates at the
  volume root (`C:\` etc.) via `platform.FilesystemRoot`.
- `caddie edit` — replaced `sh -c "$EDITOR ..."` with `platform.RunEditor`
  which exec's the editor directly on Windows (defaulting to `notepad.exe`).
- `caddie init` — skill-directory backup replaced `cp -a` shell-out with
  `platform.CopyTree`, a pure-Go recursive copy.
- `caddie export` — `copyDir` falls back to copying real content when
  `os.Symlink` is unavailable on Windows.

### Manual verification checklist (all platforms)

- [ ] `caddie init` completes without errors
- [ ] `caddie repo add` + `caddie scan` + `caddie inventory` list skills
- [ ] `caddie activate` creates correct links in `.claude/skills/`
- [ ] `caddie edit <env>` opens `$EDITOR` / `%EDITOR%` correctly
- [ ] `caddie export <env> --to ./out` copies real SKILL.md files
- [ ] On Windows without Developer Mode: junction fallback notice
      appears once; skill links function correctly
