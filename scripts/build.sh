#!/usr/bin/env bash
# Build the Go caddie binary.
#
# Compiles cmd/caddie into dist/caddie. The frozen bash at the repo root
# (ai-env-frozen) is retained as the parity oracle for contract tests but
# is no longer embedded into the Go binary.
set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p dist
go build -o dist/caddie ./cmd/caddie

echo "Built dist/caddie"
