# Build the Go caddie binary for Windows.
#
# Compiles cmd/caddie into dist\caddie.exe.
# Requires Go 1.22+ (winget install GoLang.Go).
#
# Usage:
#   pwsh ./scripts/build.ps1
#   pwsh ./scripts/build.ps1 -Cross   # also cross-compile linux/mac binaries

param (
    [switch]$Cross
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot)

New-Item -ItemType Directory -Force -Path dist | Out-Null

Write-Host "Building dist\caddie.exe ..."
go build -o dist\caddie.exe .\cmd\caddie
Write-Host "Built dist\caddie.exe"

if ($Cross) {
    Write-Host "Cross-compiling for Linux (amd64) ..."
    $env:GOOS = "linux"; $env:GOARCH = "amd64"
    go build -o dist\caddie-linux-amd64 .\cmd\caddie
    Write-Host "Built dist\caddie-linux-amd64"

    Write-Host "Cross-compiling for macOS (amd64) ..."
    $env:GOOS = "darwin"; $env:GOARCH = "amd64"
    go build -o dist\caddie-darwin-amd64 .\cmd\caddie
    Write-Host "Built dist\caddie-darwin-amd64"

    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
}
