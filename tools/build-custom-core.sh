#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-${repo_root}/build/custom-xray}"
mkdir -p -- "$(dirname -- "$output")"
output="$(cd -- "$(dirname -- "$output")" && pwd)/$(basename -- "$output")"
revision="$(git -C "$repo_root" rev-parse HEAD)"
if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=normal -- core/xray)" ]]; then
  revision="${revision}-dirty"
fi
export GOTOOLCHAIN=go1.27.1
cd -- "$repo_root/core/xray"
go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X github.com/xtls/xray-core/core.build=${revision}" \
  -o "$output" ./main
sha256sum -- "$output" > "${output}.sha256"
printf 'Custom Xray-core built: %s\n' "$output"
