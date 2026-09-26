#!/usr/bin/env bash
set -euo pipefail
# With an argument, scan the exact artifacts that will be published.
artifact_dir="${1:-$(mktemp -d)}"
if [ "$#" -eq 0 ]; then
  trap 'rm -rf "$artifact_dir"' EXIT
fi
for target_os in linux darwin; do
  for target_arch in amd64 arm64; do
    artifact="$artifact_dir/gonner-$target_os-$target_arch"
    if [ "$#" -eq 0 ]; then
      GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED=0 go build -ldflags="-s -w" -o "$artifact" ./cmd/gonner
    fi
    govulncheck -mode=binary "$artifact"
  done
done
