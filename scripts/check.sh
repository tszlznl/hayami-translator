#!/usr/bin/env bash
# Local checks for Hayami Translator (Linux / macOS).
# Pass --race to run the test suite with the race detector.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> gofmt"
unformatted="$(find . -maxdepth 4 \
  \( -path './references' -o -path './poc' -o -path './build' -o -path './.mygo' -o -path './.git' \) -prune \
  -o -name '*.go' -print0 | xargs -0 -r gofmt -l)"
if [ -n "$unformatted" ]; then
  echo "FAILED: these files need gofmt:" >&2
  echo "$unformatted" >&2
  exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> mygo vet"
go tool mygo vet .

if [ "${1:-}" = "--race" ]; then
  echo "==> go test -race"
  go test -race ./...
else
  echo "==> go test"
  CGO_ENABLED=0 go test ./...
fi

echo "All checks passed."
