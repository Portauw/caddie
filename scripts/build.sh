#!/usr/bin/env bash
# Build the Go ai-env binary.
#
# Refreshes internal/legacy/ai-env-legacy.sh from ai-env-frozen at the repo
# root (for //go:embed), then compiles cmd/ai-env into dist/ai-env. The
# frozen bash is kept around as an escape hatch and as the parity oracle
# for contract tests — it is never shipped.
set -euo pipefail

cd "$(dirname "$0")/.."

cp ai-env-frozen internal/legacy/ai-env-legacy.sh

mkdir -p dist
go build -o dist/ai-env ./cmd/ai-env

echo "Built dist/ai-env"
