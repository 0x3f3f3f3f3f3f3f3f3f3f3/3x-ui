#!/usr/bin/env bash
# Preserve all current native behavior except the negotiated trusted identity marker.
set -euo pipefail
native_fixture_dir=$(mktemp -d "${TMPDIR:-/tmp}/snell-capability-fixture.XXXXXX")
trap 'rm -rf "$native_fixture_dir"' EXIT
python3 - "$native_fixture_dir" <<'PY'
import json
import sys
from pathlib import Path

source = Path("core/xray/app/clientpolicy/command/command.go").resolve()
fixture = Path(sys.argv[1])
text = source.read_text()
marker = 'features = append(features, "trusted-snell-client-id-v1")'
assert text.count(marker) == 1
replacement = fixture / "command.go"
replacement.write_text(text.replace(marker, "", 1))
(fixture / "overlay.json").write_text(json.dumps({"Replace": {str(source): str(replacement)}}))
PY
mkdir -p build
go -C core/xray build -overlay="$native_fixture_dir/overlay.json" -o "$PWD/build/pre-snell-marker-xray" ./main
