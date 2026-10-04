#!/usr/bin/env bash
# Build the original pre-mapping private core from an immutable ancestor.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-${repo_root}/build/pre-client-mapping-xray}"
baseline=acca4a09b86288666d564c03d1f1c78f30a28ecc
git -C "$repo_root" cat-file -e "${baseline}^{commit}"
mkdir -p -- "$(dirname -- "$output")" "$repo_root/build"
output="$(cd -- "$(dirname -- "$output")" && pwd)/$(basename -- "$output")"
fixture_dir=$(mktemp -d "$repo_root/build/pre-client-mapping-source.XXXXXX")
# Keep the original source and receipt for provenance; never edit an older fixture.
git -C "$repo_root" archive "$baseline" core | tar -x -C "$fixture_dir"
python3 - "$fixture_dir" "$baseline" "$output.source.json" <<'PY'
import hashlib, json, sys
from pathlib import Path
root, baseline, target = Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3])
command = (root / 'core/xray/app/clientpolicy/command/command.go').read_text()
assert 'client-authority-history-v1' not in command
assert 'AuthorityGrantHistory:' not in command
files = {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
         for p in sorted((root / 'core').rglob('*')) if p.is_file()}
target.write_text(json.dumps({'baseline': baseline, 'source': str(root), 'files': files}, indent=2)+'\n')
PY
export GOTOOLCHAIN=go1.27.1
go -C "$fixture_dir/core/xray" build -trimpath -buildvcs=false \
  -ldflags "-s -w -X github.com/xtls/xray-core/core.build=${baseline}" \
  -o "$output" ./main
python3 - "$output.source.json" <<'PY'
import hashlib, json, sys
from pathlib import Path
receipt = json.loads(Path(sys.argv[1]).read_text())
root = Path(receipt['source'])
assert all(hashlib.sha256((root / p).read_bytes()).hexdigest() == digest
           for p, digest in receipt['files'].items())
PY
sha256sum -- "$output" > "$output.sha256"
printf 'Original pre-mapping core built: %s\n' "$output"
