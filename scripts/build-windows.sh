#!/usr/bin/env bash
# Build Windows amd64 desktop app via Wails.
set -euo pipefail
cd "$(dirname "$0")/.."
wails build -platform windows/amd64 -o media-dedupe-app.exe
echo "Output: build/bin/media-dedupe-app.exe"
