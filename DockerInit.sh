#!/bin/sh
# Assemble legacy resources, then build the source-matched panel/managed core.
set -eu

print_only=false
if [ "${1:-}" = --print-target ]; then
    print_only=true
    shift
fi
[ "$#" -ge 2 ] || { echo "usage: DockerInit.sh [--print-target] TARGETOS TARGETARCH TARGETVARIANT [NEW_PACKAGE_DIRECTORY]" >&2; exit 1; }
GOOS=$1
GOARCH=$2
variant=${3:-}
GOARM=
TUIC_ARCH=
[ "$GOOS" = linux ] || { echo "DockerInit: only Linux targets are supported" >&2; exit 1; }
case "$GOARCH/$variant" in
    amd64/) CORE_ARCH=amd64; MTG_ARCH=amd64; TUIC_ARCH=x86_64-unknown-linux-musl ;;
    386/) CORE_ARCH=386; MTG_ARCH=386; TUIC_ARCH=i686-unknown-linux-musl ;;
    arm64/|arm64/v8) CORE_ARCH=arm64; MTG_ARCH=arm64; TUIC_ARCH=aarch64-unknown-linux-musl ;;
    arm/v5) GOARM=5; CORE_ARCH=arm32; MTG_ARCH=armv5 ;;
    arm/v6) GOARM=6; CORE_ARCH=arm32; MTG_ARCH=armv6 ;;
    arm/v7) GOARM=7; CORE_ARCH=arm32; MTG_ARCH=armv7; TUIC_ARCH=armv7-unknown-linux-musleabihf ;;
    s390x/) CORE_ARCH=s390x; MTG_ARCH=s390x ;;
    *) echo "DockerInit: unsupported target $GOOS/$GOARCH/$variant" >&2; exit 1 ;;
esac
if [ "$print_only" = true ]; then
    [ "$#" -le 3 ] || { echo "DockerInit: unexpected target arguments" >&2; exit 1; }
    printf 'GOOS=%s GOARCH=%s GOARM=%s CORE_ARCH=%s MTG_ARCH=%s TUIC_ARCH=%s\n' "$GOOS" "$GOARCH" "$GOARM" "$CORE_ARCH" "$MTG_ARCH" "$TUIC_ARCH"
    exit 0
fi
[ "$#" -eq 4 ] || { echo "DockerInit: a new package directory is required" >&2; exit 1; }
output=$4
[ ! -e "$output" ] && [ ! -L "$output" ] || { echo "DockerInit: refusing to replace an existing package" >&2; exit 1; }
: "${SOURCE_REVISION:?Docker source contexts require an explicitly verified SOURCE_REVISION}"
[ "${#SOURCE_REVISION}" -eq 40 ] || { echo "DockerInit: a full source revision is required" >&2; exit 1; }
case "$SOURCE_REVISION" in *[!0-9a-f]*) echo "DockerInit: invalid source revision" >&2; exit 1 ;; esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$repo_root"
resources="$repo_root/build/docker-resources"
downloads="$repo_root/build/docker-downloads"
[ ! -e "$resources" ] && [ ! -L "$resources" ] && [ ! -e "$downloads" ] && [ ! -L "$downloads" ] || {
    echo "DockerInit: refusing to reuse an earlier resource assembly" >&2; exit 1;
}
mkdir -p "$resources/bin" "$downloads"
download() {
    curl --fail --show-error --silent --location --retry 3 --connect-timeout 20 --max-time 300 "$1" -o "$2"
    [ -s "$2" ] || { echo "DockerInit: empty resource $2" >&2; exit 1; }
}

# Legacy MTProto and TUIC remain sidecars. Their native migration is separate.
MTG_MULTI_VER=v1.15.0
MTG_PKG="mtg-multi-${MTG_MULTI_VER#v}-linux-$MTG_ARCH"
MTG_BASE="https://github.com/mhsanaei/mtg-multi/releases/download/$MTG_MULTI_VER"
download "$MTG_BASE/$MTG_PKG.tar.gz" "$downloads/$MTG_PKG.tar.gz"
download "$MTG_BASE/mtg-multi-${MTG_MULTI_VER#v}-checksums.txt" "$downloads/mtg-checksums.txt"
checksum=$(awk -v file="$MTG_PKG.tar.gz" '$2 == file { print $1 }' "$downloads/mtg-checksums.txt")
[ "${#checksum}" -eq 64 ] || { echo "DockerInit: missing MTProto resource checksum" >&2; exit 1; }
(cd "$downloads" && printf '%s  %s\n' "$checksum" "$MTG_PKG.tar.gz" | sha256sum -c -)
# Read only the expected executable; never extract arbitrary archive paths.
tar -xOf "$downloads/$MTG_PKG.tar.gz" "$MTG_PKG/mtg-multi" > "$resources/bin/mtg-linux-$CORE_ARCH"
[ -s "$resources/bin/mtg-linux-$CORE_ARCH" ] || { echo "DockerInit: empty MTProto helper" >&2; exit 1; }
chmod 755 "$resources/bin/mtg-linux-$CORE_ARCH"
if [ "$GOARCH" = arm ]; then
    cp "$resources/bin/mtg-linux-$CORE_ARCH" "$resources/bin/mtg-linux-arm"
    cp "$resources/bin/mtg-linux-$CORE_ARCH" "$resources/bin/mtg-linux-armv$GOARM"
fi
if [ -n "$TUIC_ARCH" ]; then
    download "https://github.com/EAimTY/tuic/releases/download/tuic-server-1.0.0/tuic-server-1.0.0-$TUIC_ARCH" "$resources/bin/tuic-server"
    chmod 755 "$resources/bin/tuic-server"
fi
download https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat "$resources/bin/geoip.dat"
download https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat "$resources/bin/geosite.dat"
download https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat "$resources/bin/geoip_IR.dat"
download https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat "$resources/bin/geosite_IR.dat"
download https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat "$resources/bin/geoip_RU.dat"
download https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat "$resources/bin/geosite_RU.dat"

export GOOS GOARCH GOARM
PAIRED_RESOURCE_DIR="$resources" ./tools/build-paired-package.sh "$output"
[ -x "$output/bin/xray-linux-$CORE_ARCH" ] || { echo "DockerInit: managed core missing from paired package" >&2; exit 1; }
