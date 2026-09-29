#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "usage: $0 NEW_SOURCE_DIRECTORY" >&2
    exit 2
fi
tool_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$tool_dir/../.." && pwd)
destination=$1
mkdir -- "$destination"
destination=$(CDPATH= cd -- "$destination" && pwd)
cd "$repo_dir"

pin=v1.260327.1-0.20260908222543-52a412d9e2f5
module=github.com/xtls/xray-core
actual=$(go list -m -f '{{.Version}}' "$module")
if [ "$actual" != "$pin" ]; then
    echo "managed Xray patches require the reviewed source pin $pin" >&2
    exit 1
fi
go mod download "$module@$pin"
go mod verify >&2
source_sum=$(go list -m -f '{{.Sum}}' "$module")
if [ "$source_sum" != 'h1:BsUC2sCXcdVCb09SUh1iWku0ci779t4bUIlKUor1ZRI=' ]; then
    echo "managed Xray source checksum differs from the reviewed source" >&2
    exit 1
fi
source_dir=$(go list -m -f '{{.Dir}}' "$module")
cp -R "$source_dir/." "$destination/"
chmod -R u+w "$destination"
cd "$destination"
for patch_file in "$tool_dir"/patches/*.patch; do
    git apply --check "$patch_file"
    git apply "$patch_file"
done
cp -R "$tool_dir/testdata/." "$destination/"
printf '%s\n' "$destination"
