#!/usr/bin/env bash
# Build a panel and managed core from one clean source. No official-core fallback.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:?usage: build-paired-package.sh NEW_OUTPUT_DIRECTORY}"
[[ $# == 1 ]] || { echo "one new output directory is required" >&2; exit 1; }
[[ ! -e "$output" && ! -L "$output" ]] || { echo "refusing to replace an existing package directory" >&2; exit 1; }
mkdir -p -- "$(dirname -- "$output")"
output="$(cd -- "$(dirname -- "$output")" && pwd)/$(basename -- "$output")"
cd -- "$repo_root"
case "$output" in "$repo_root"/build/*) ;; "$repo_root"/*) echo "in-tree package output must be under ignored build/" >&2; exit 1 ;; esac

if [[ -e .git ]]; then
  revision="$(git rev-parse HEAD)"
  [[ -z "$(git status --porcelain --untracked-files=normal)" ]] || { echo "paired packages require a clean source tree" >&2; exit 1; }
  [[ -z "${SOURCE_REVISION:-}" || "$SOURCE_REVISION" == "$revision" ]] || { echo "SOURCE_REVISION differs from checked-out source" >&2; exit 1; }
else
  revision="${SOURCE_REVISION:?source archives require an explicitly verified SOURCE_REVISION}"
fi
[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || { echo "a full clean source revision is required" >&2; exit 1; }
export GOTOOLCHAIN=go1.27.1
[[ "$(go env GOVERSION)" == go1.27.1 ]] || { echo "Go 1.27.1 is required" >&2; exit 1; }
if [[ -n "${PAIRED_FRONTEND_DIR:-}" ]]; then
  [[ "${PAIRED_NODE_VERSION:-}" == v26.10.0 && -d "$PAIRED_FRONTEND_DIR" && ! -L "$PAIRED_FRONTEND_DIR" ]] || { echo "prebuilt frontend requires Node 26.10.0 and a directory without a link" >&2; exit 1; }
  [[ -s "$PAIRED_FRONTEND_DIR/index.html" && -d "$PAIRED_FRONTEND_DIR/assets" ]] || { echo "prebuilt frontend is incomplete" >&2; exit 1; }
  [[ -z "$(find "$PAIRED_FRONTEND_DIR" -mindepth 1 ! -type f ! -type d -print -quit)" ]] || { echo "prebuilt frontend contains a link or special file" >&2; exit 1; }
else
  [[ "$(node --version)" == v26.10.0 ]] || { echo "Node 26.10.0 is required" >&2; exit 1; }
fi
os_name="${GOOS:-$(go env GOHOSTOS)}"
arch="${GOARCH:-$(go env GOHOSTARCH)}"
arm="${GOARM:-}"
[[ "$os_name" == linux ]] || { echo "paired distribution currently requires Linux" >&2; exit 1; }
case "$arch" in
  amd64|arm64|386|s390x) [[ -z "$arm" ]] || { echo "GOARM supplied for non-ARM target" >&2; exit 1; }; binary_arch="$arch" ;;
  arm) [[ "$arm" =~ ^[567]$ ]] || { echo "ARM targets require GOARM=5, 6 or 7" >&2; exit 1; }; binary_arch=arm32 ;;
  *) echo "unsupported package architecture: $arch" >&2; exit 1 ;;
esac

# Verify the locked module inputs before compiling. This never edits cached source.
go mod verify
(cd core/xray && go mod verify)
if [[ -n "${PAIRED_FRONTEND_DIR:-}" ]]; then
  # Docker builds these assets in its native BUILDPLATFORM Node stage from the
  # same source context; target Go stages need no emulated Node executable.
  rm -rf -- internal/web/dist
  mkdir -p -- internal/web/dist
  cp -a -- "$PAIRED_FRONTEND_DIR/." internal/web/dist/
else
  (cd frontend && npm ci && npm run build)
fi
mkdir -- "$output"
mkdir -- "$output/bin" "$output/licenses"
compiler="${CC:-$(go env CC)}"
command -v "$compiler" > "$output/licenses/c-compiler.txt"
"$compiler" --version >> "$output/licenses/c-compiler.txt"
export GOOS="$os_name" GOARCH="$arch"
[[ -z "$arm" ]] || export GOARM="$arm"
panel_flags="-s -w -X github.com/mhsanaei/3x-ui/v3/internal/distribution.PanelSourceRevision=$revision"
# Keep stable/dev display semantics separate from package provenance.
if [[ "${PAIRED_DEV_BUILD:-0}" == 1 ]]; then
  panel_flags="$panel_flags -X github.com/mhsanaei/3x-ui/v3/internal/config.buildCommit=$revision"
fi
[[ -z "${PAIRED_PANEL_LDFLAGS:-}" ]] || panel_flags="$panel_flags $PAIRED_PANEL_LDFLAGS"
CGO_ENABLED=1 go build -trimpath -buildvcs=false -ldflags "$panel_flags" -o "$output/x-ui" .
(cd core/xray && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w -X github.com/xtls/xray-core/core.build=$revision" -o "$output/bin/xray-linux-$binary_arch" ./main)
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w -X github.com/mhsanaei/3x-ui/v3/internal/distribution.PanelSourceRevision=$revision" -o "$output/x-ui-package" ./tools/packageverify
cp -- x-ui.sh install.sh update.sh x-ui.rc x-ui.service.debian x-ui.service.arch x-ui.service.rhel DockerEntrypoint.sh "$output/"
cp -- tools/install-paired-package.sh "$output/"
cp -- LICENSE "$output/licenses/panel-GPL-3.0.txt"
cp -- core/xray/LICENSE "$output/licenses/xray-MPL-2.0.txt"
cp -- core/THIRD_PARTY_NOTICES.md "$output/licenses/THIRD_PARTY_NOTICES.md"
cp -- core/licenses/* "$output/licenses/"
cp -- core/deps/sing-snell/LICENSE "$output/licenses/sing-snell-GPL-3.0.txt"
cp -- core/deps/opensnell/LICENSE.md "$output/licenses/opensnell-GPL-3.0.txt"
cp -- core/deps/amneziawg-go/LICENSE "$output/licenses/amneziawg-MIT.txt"
cp -- core/deps/*.UPSTREAM.json "$output/licenses/"
mkdir -p -- "$output/internal/web"
cp -a -- internal/web/translation "$output/internal/web/"
if [[ -n "${PAIRED_RESOURCE_DIR:-}" ]]; then
  [[ -d "$PAIRED_RESOURCE_DIR" && ! -L "$PAIRED_RESOURCE_DIR" ]] || { echo "resource directory must be a directory without a link" >&2; exit 1; }
  # Resource assembly can only add data/legacy helpers. It must never replace
  # either member of the newly compiled pair or inject runtime state.
  while IFS= read -r -d '' resource; do
    relative="${resource#"$PAIRED_RESOURCE_DIR"/}"
    case "$relative" in bin/geo*.dat|bin/mtg-linux-*|bin/tuic-server|licenses/build-toolchains.json) ;;
      *) echo "unsupported package resource: $relative" >&2; exit 1 ;;
    esac
    [[ -f "$resource" && ! -L "$resource" && ! -e "$output/$relative" ]] || { echo "unsafe or conflicting package resource: $relative" >&2; exit 1; }
    cp -- "$resource" "$output/$relative"
  done < <(find "$PAIRED_RESOURCE_DIR" -mindepth 1 ! -type d -print0)
fi
if [[ -e .git ]]; then
  git archive --format=tar.gz HEAD > "$output/licenses/corresponding-source.tar.gz"
else
  # Docker/source-archive contexts already exclude Git, builds, caches and state.
  tar --exclude='./build' --exclude='./frontend/node_modules' --exclude='./internal/web/dist' \
    --exclude='./.git' -czf "$output/licenses/corresponding-source.tar.gz" \
    ./*.go go.mod go.sum LICENSE Makefile .nvmrc ./*.sh ./*.service.* x-ui.rc Dockerfile .dockerignore .github internal frontend core tools docs
fi
printf '{"sourceRevision":"%s","sourceRepository":"https://github.com/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui","compatibility":"traffic-control-v1"}\n' "$revision" > "$output/licenses/package-origin.json"
# Generate metadata on the build host, including when the pair is cross-built.
host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
GOOS="$host_os" GOARCH="$host_arch" GOARM= CGO_ENABLED=0 \
  go run ./tools/packagedeps "$output/licenses" "$output/x-ui" "$output/bin/xray-linux-$binary_arch"
GOOS="$host_os" GOARCH="$host_arch" GOARM= CGO_ENABLED=0 \
  go run ./tools/packagegen --root "$output" --os "$os_name" --arch "$arch" --arm "$arm" --revision "$revision" --go-version go1.27.1 --node-version v26.10.0
if [[ "$os_name" == "$host_os" && "$arch" == "$host_arch" ]]; then
  "$output/x-ui" package verify "$output" > "$output.verification.json"
fi
printf 'Source-matched panel/Custom Xray package: %s (%s)\n' "$output" "$revision"
