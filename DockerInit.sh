#!/bin/sh
set -eu
if [ "$#" -ne 4 ]; then
    echo "usage: $0 TARGETARCH TARGETVARIANT FULL_COMMIT TAG" >&2
    exit 2
fi
case "$1" in
    amd64|arm64|386|s390x) PLATFORM=$1; FNAME=$1 ;;
    arm)
        case "$2" in
            v5|v6|v7) PLATFORM="arm$2"; FNAME=arm ;;
            *) echo "DockerInit: unsupported ARM variant: $2" >&2; exit 2 ;;
        esac
        ;;
    *) echo "DockerInit: unsupported architecture: $1" >&2; exit 2 ;;
esac
source_commit=$3
release_tag=$4
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$repo_dir"
mkdir -p build/bin
cp DockerEntrypoint.sh build/DockerEntrypoint.sh
cd build/bin
# Preserve supported optional MTProto sidecars alongside the managed core.
case "$PLATFORM" in
    amd64|arm64|386|armv6|armv7)
        MTG_MULTI_VER=$(curl -sfL "https://api.github.com/repos/mhsanaei/mtg-multi/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
        if [ -z "$MTG_MULTI_VER" ]; then
            echo "DockerInit: could not resolve the latest mtg-multi release tag" >&2
            exit 1
        fi
        MTG_PKG="mtg-multi-${MTG_MULTI_VER#v}-linux-${PLATFORM}"
        curl -sfLRO "https://github.com/mhsanaei/mtg-multi/releases/download/${MTG_MULTI_VER}/${MTG_PKG}.tar.gz"
        tar -xzf "${MTG_PKG}.tar.gz"
        mv "${MTG_PKG}/mtg-multi" "mtg-linux-${FNAME}"
        rm -rf "${MTG_PKG}" "${MTG_PKG}.tar.gz"
        chmod +x "mtg-linux-${FNAME}"
        ;;
esac
case $PLATFORM in
    amd64)
        curl -sfLRo "tuic-server" "https://github.com/EAimTY/tuic/releases/download/tuic-server-1.0.0/tuic-server-1.0.0-x86_64-unknown-linux-musl"
        ;;
    arm64)
        curl -sfLRo "tuic-server" "https://github.com/EAimTY/tuic/releases/download/tuic-server-1.0.0/tuic-server-1.0.0-aarch64-unknown-linux-musl"
        ;;
    armv7)
        curl -sfLRo "tuic-server" "https://github.com/EAimTY/tuic/releases/download/tuic-server-1.0.0/tuic-server-1.0.0-armv7-unknown-linux-musleabihf"
        ;;
    386)
        curl -sfLRo "tuic-server" "https://github.com/EAimTY/tuic/releases/download/tuic-server-1.0.0/tuic-server-1.0.0-i686-unknown-linux-musl"
        ;;
esac
if [ -f "tuic-server" ]; then
    if [ ! -s "tuic-server" ]; then
        echo "DockerInit: tuic-server download was empty" >&2
        exit 1
    fi
    chmod +x "tuic-server"
fi
curl -sfLRO https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat
curl -sfLRO https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat
curl -sfLRo geoip_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat
curl -sfLRo geosite_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat
curl -sfLRo geoip_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat
curl -sfLRo geosite_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat
cd ../..
sh tools/managed-release/prepare-linux.sh "$repo_dir/build" "$source_commit" "$release_tag" "linux-$PLATFORM"
./build/x-ui verify-release --directory "$repo_dir/build" --commit "$source_commit" --tag "$release_tag" --platform "linux-$PLATFORM"
