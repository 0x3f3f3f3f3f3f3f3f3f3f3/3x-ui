#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    echo "usage: $0 OUTPUT_BINARY [OUTPUT_SOURCE_ARCHIVE]" >&2
    exit 2
fi
tool_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
output_dir=$(CDPATH= cd -- "$(dirname -- "$1")" && pwd)
output="$output_dir/$(basename -- "$1")"
if [ -e "$output" ] || [ -L "$output" ]; then
    echo "refusing to overwrite an existing output: $output" >&2
    exit 1
fi
source_output=
if [ "$#" -eq 2 ]; then
    source_dir=$(CDPATH= cd -- "$(dirname -- "$2")" && pwd)
    source_output="$source_dir/$(basename -- "$2")"
    if [ -e "$source_output" ] || [ -L "$source_output" ] || [ "$source_output" = "$output" ]; then
        echo "refusing to overwrite source output: $source_output" >&2
        exit 1
    fi
fi
stage=$(mktemp -d "$output_dir/.managed-xray.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
sh "$tool_dir/prepare.sh" "$stage/source" >/dev/null
cd "$stage/source"
go build -trimpath -buildvcs=false -ldflags '-X github.com/xtls/xray-core/core.build=3x-ui-managed-1' -o "$stage/xray" ./main
if [ -n "$source_output" ]; then
    tar --format=gnu --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
        --mode='u=rwX,go=rX' -cf "$stage/source.tar" -C "$stage" source
    gzip -n "$stage/source.tar"
    ln -- "$stage/source.tar.gz" "$source_output"
fi
ln -- "$stage/xray" "$output"
