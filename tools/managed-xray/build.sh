#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "usage: $0 OUTPUT_BINARY" >&2
    exit 2
fi
tool_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
output_dir=$(CDPATH= cd -- "$(dirname -- "$1")" && pwd)
output="$output_dir/$(basename -- "$1")"
if [ -e "$output" ]; then
    echo "refusing to overwrite an existing output: $output" >&2
    exit 1
fi
stage=$(mktemp -d "$output_dir/.managed-xray.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
sh "$tool_dir/prepare.sh" "$stage/source" >/dev/null
cd "$stage/source"
go build -trimpath -buildvcs=false -ldflags '-X github.com/xtls/xray-core/core.build=3x-ui-packets-1' -o "$stage/xray" ./main
ln -- "$stage/xray" "$output"
