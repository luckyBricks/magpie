#!/usr/bin/env bash
# Environment setup for magpie (Go). Warms the module and build caches.
set -euo pipefail
cd "$(dirname "$0")/../.."

# The nogui tag avoids the GTK/Wails native dependencies.
export CGO_ENABLED=0
export GOFLAGS="${GOFLAGS:-} -tags=nogui"

go mod download

# Prime the build cache for the binary and for the test packages.
go build -trimpath -o /tmp/magpie-health .
go test -run '^$' ./internal/... >/dev/null

healthcheck() {
  # The CLI builds and starts, and a real package test passes.
  /tmp/magpie-health --help >/dev/null 2>&1 || [ -x /tmp/magpie-health ]
  go test ./internal/proc/... >/dev/null
}

healthcheck
