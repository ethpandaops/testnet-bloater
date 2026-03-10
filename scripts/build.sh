#!/bin/bash
set -euo pipefail
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"
echo "Building bloater-tool..."
go build -o bloater-tool ./cmd/
echo "Done: ./bloater-tool"
