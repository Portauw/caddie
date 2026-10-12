# Repo Prefix Editing Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Support `caddie repo edit <name> --prefix <prefix>` and the same prefix flag during registration.

**Architecture:** CLI handlers validate arguments, the repos package updates only the selected configuration field, and the skills package reconciles canonical symlinks for the selected checkout. Prefix changes use existing `true` and `false` semantics. Project profiles receive a selector and activation hint.

**Tech Stack:** Go standard library, existing line-oriented YAML helpers, filesystem symlinks, local Git contract fixtures.

## Task 1: Pin the observable CLI behavior

Create `/Users/pieter/Dev/caddie/repo/tests/contract/repo_edit_test.go` covering prefix registration, changing absent and literal prefixes, `true`, `false`, repeated edits, unchanged unrelated configuration, local overrides, foreign symlinks, invalid arguments, missing checkouts, and project activation with the new selector.

Run `go test ./tests/contract -run 'TestRepo(Edit|AddPrefix)' -count=1` in `/Users/pieter/Dev/caddie/repo`. Expect failures because the edit command and prefix flag are missing.

## Task 2: Add registry updates and CLI flags

Modify `/Users/pieter/Dev/caddie/repo/internal/repos/repos.go` to persist optional prefixes on append and update a single repo prefix without rewriting unrelated blocks, comments, or fields. Validate nonempty prefix values as safe namespace identifiers, allowing `true` and `false`.

Modify `/Users/pieter/Dev/caddie/repo/cmd/caddie/main.go` to dispatch `repo edit`, accept `--prefix value` and `--prefix=value`, preserve positional registration paths, and support `--skills-path` on registration. Reject unknown, missing, duplicate, and extra arguments before writing configuration.

## Task 3: Reconcile the selected repo's store links

Modify `/Users/pieter/Dev/caddie/repo/internal/skills/sync.go` to expose a local-only single-repo sync and remove obsolete links using exact checkout ownership. Preserve real directories and unrelated symlinks. Skip cleanup when the checkout cannot be walked. Report sync failures and destination collisions instead of silently claiming a successful migration. Invalidate the scan cache after configuration changes.

Run targeted contract tests plus `go test ./internal/repos ./internal/skills ./cmd/caddie -count=1`. Verify all prefix transitions and existing registration, scan, inventory, activation, and export callers.

## Task 4: Document and verify

Update `/Users/pieter/Dev/caddie/repo/README.md` and CLI help with registration and editing examples, sentinel values, automatic store cleanup, and project selector/activation instructions. Correct the adjacent repo-without-prefix description to match actual repo classification.

Build using `go build ./...`, run `go test ./... -count=1` once after integration, run `go vet ./...`, and inspect `git diff --check` and the final diff. Keep live Caddie configuration isolated from tests. Record results here.

## Completion

All four tasks completed on 2026-10-12. Prefix edits use a strict variant of the existing walker so unreadable directories fail reconciliation before old links are pruned. Cleanup resolves existing ancestors of broken targets to support filesystem aliases and relative links while retaining foreign links.

Verification passed:
- Initial contract tests failed for the missing edit command and registration flag.
- Missing-root, unreadable-walk, and broken-link regression cases failed before their fixes and passed afterward.
- Targeted repo command, scan, inventory, and activation contract checks passed.
- `go test ./internal/repos ./internal/skills ./cmd/caddie -count=1` passed.
- `go build ./...` passed.
- `go test ./... -count=1` passed across all six tested packages, including export and CLI contracts.
- `go vet ./...` passed.
- `gofmt -d` reported no formatting differences for changed Go files.
- `git diff --check` passed.

Contract fixtures used isolated configuration directories and local Git repositories.

Registration also prints a ready-to-copy `skills` entry and the project activation command. Contract coverage checks the selector for literal prefixes, `true`, `false`, and omitted prefixes.
