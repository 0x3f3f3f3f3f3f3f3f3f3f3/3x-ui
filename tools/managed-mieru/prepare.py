#!/usr/bin/env python3
"""Reproduce or verify the maintained, protocol-only mieru server extension."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile


TOOL = Path(__file__).resolve().parent
REPO = TOOL.parent.parent
NATIVE = REPO / "internal/mieru/native"


def run(*command, cwd=REPO):
    return subprocess.check_output(command, cwd=cwd, text=True)


def prepare(destination):
    pin = json.loads((TOOL / "source.json").read_text())
    version = run("go", "list", "-m", "-f", "{{.Version}}", pin["module"]).strip()
    if version != pin["version"]:
        raise SystemExit("mieru dependency differs from the reviewed source pin")
    source = json.loads(run("go", "mod", "download", "-json", pin["module"] + "@" + version))
    if source.get("Sum") != pin["sum"] or source.get("GoModSum") != pin["goModSum"]:
        raise SystemExit("mieru module checksums differ from the reviewed source")
    run("go", "mod", "verify")
    files = {}
    for name, expected in pin["files"].items():
        if Path(name).name != name:
            raise SystemExit("source manifest contains a nonlocal filename")
        path = Path(source["Dir"]) / ("LICENSE" if name == "LICENSE" else "pkg/protocol/" + name)
        if hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise SystemExit("mieru source file checksum differs: " + name)
        files[name] = path
    destination.mkdir()
    for name, path in files.items():
        shutil.copyfile(path, destination / name)
    patch = str(TOOL / "managed-resources.patch")
    run("git", "apply", "--check", patch, cwd=destination)
    run("git", "apply", patch, cwd=destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--output", type=Path, help="new directory under an existing parent")
    mode.add_argument("--verify", action="store_true", help="compare with checked-in native Go sources and license")
    args = parser.parse_args()
    if args.output is not None:
        prepare(args.output.resolve())
        print(args.output.resolve())
        return
    with tempfile.TemporaryDirectory(prefix="xui-mieru-source-") as temporary:
        generated = Path(temporary) / "native"
        prepare(generated)
        expected = {p.name for p in NATIVE.glob("*.go")} | {"LICENSE"}
        actual = {p.name for p in generated.iterdir()}
        if actual != expected:
            raise SystemExit("mieru native file set differs from the reproducible patch")
        for name in sorted(expected):
            if (generated / name).read_bytes() != (NATIVE / name).read_bytes():
                raise SystemExit("mieru native file differs from the reproducible patch: " + name)
    print("mieru native source and license reproduced exactly")


if __name__ == "__main__":
    main()
