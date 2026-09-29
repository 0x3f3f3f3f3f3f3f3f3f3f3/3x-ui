#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
    echo "usage: $0 BUNDLE_DIRECTORY FULL_COMMIT TAG linux-PLATFORM" >&2
    exit 2
fi
commit=$2
tag=$3
platform=$4
case "$commit" in
    ''|*[!0-9a-f]*) echo "expected a full lowercase source commit" >&2; exit 2 ;;
esac
[ "${#commit}" -eq 40 ] || { echo "expected a full lowercase source commit" >&2; exit 2; }
case "$tag" in
    ''|[!A-Za-z0-9]*|*[!A-Za-z0-9._+-]*) echo "invalid release tag" >&2; exit 2 ;;
esac
[ "${#tag}" -le 128 ] || { echo "invalid release tag" >&2; exit 2; }
target_arm=
case "$platform" in
    linux-amd64|linux-arm64|linux-386|linux-s390x)
        target_arch=${platform#linux-}
        core_arch=$target_arch
        ;;
    linux-armv5|linux-armv6|linux-armv7)
        target_arch=arm
        target_arm=${platform#linux-armv}
        core_arch=arm32
        ;;
    *) echo "unsupported managed release platform: $platform" >&2; exit 2 ;;
esac
tool_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$tool_dir/../.." && pwd)
bundle=$(CDPATH= cd -- "$1" && pwd)
if [ ! -f "$bundle/x-ui" ] || [ ! -s "$bundle/x-ui" ] || [ ! -x "$bundle/x-ui" ] || [ -L "$bundle/x-ui" ]; then
    echo "bundle requires a compiled executable panel" >&2
    exit 2
fi
for name in update-stage update.sh install.sh x-ui.sh x-ui.rc \
    x-ui.service.debian x-ui.service.arch x-ui.service.rhel release.json \
    xray-managed-source.tar.gz xray-managed-LICENSE "bin/xray-linux-$core_arch"; do
    if [ -e "$bundle/$name" ] || [ -L "$bundle/$name" ]; then
        echo "refusing to overwrite bundle member: $name" >&2
        exit 2
    fi
done
if [ -L "$bundle/bin" ] || { [ -e "$bundle/bin" ] && [ ! -d "$bundle/bin" ]; }; then
    echo "bundle bin must be an ordinary directory" >&2
    exit 2
fi
stage=$(mktemp -d "${TMPDIR:-/tmp}/3x-ui-package-build.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
cd "$repo_dir"
mkdir -p "$bundle/bin"
CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" GOARM="$target_arm" \
    sh tools/managed-xray/build.sh "$bundle/bin/xray-linux-$core_arch" "$bundle/xray-managed-source.tar.gz"
CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" GOARM="$target_arm" \
    go build -trimpath -buildvcs=false -o "$bundle/update-stage" ./tools/update-stage
for name in update.sh install.sh x-ui.sh x-ui.rc; do
    cp "$repo_dir/$name" "$bundle/$name"
    chmod 755 "$bundle/$name"
done
for name in x-ui.service.debian x-ui.service.arch x-ui.service.rhel; do
    cp "$repo_dir/$name" "$bundle/$name"
    chmod 644 "$bundle/$name"
done
cp tools/managed-xray/LICENSE "$bundle/xray-managed-LICENSE"
chmod 644 "$bundle/xray-managed-LICENSE" "$bundle/xray-managed-source.tar.gz"
host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)
CGO_ENABLED=0 GOOS="$host_os" GOARCH="$host_arch" GOARM= \
    go build -trimpath -buildvcs=false -o "$stage/release-manifest" ./tools/release-manifest
"$stage/release-manifest" --directory "$bundle" --commit "$commit" --tag "$tag" --platform "$platform"
