#!/usr/bin/env bash
# Build the Go ai-env binary.
#
# Refreshes internal/legacy/ai-env-legacy.sh from the frozen bash script at
# the repo root (so //go:embed picks up the current content), then compiles
# cmd/ai-env into dist/ai-env.
set -euo pipefail

cd "$(dirname "$0")/.."

cp ai-env internal/legacy/ai-env-legacy.sh

mkdir -p dist
go build -o dist/ai-env ./cmd/ai-env

echo "Built dist/ai-env"
