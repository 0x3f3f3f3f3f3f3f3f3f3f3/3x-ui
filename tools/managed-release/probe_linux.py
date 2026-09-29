#!/usr/bin/env python3
"""Build and stage a native release fixture without installing it."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile


def run(*args, **kwargs):
    timeout = kwargs.pop("timeout")
    with subprocess.Popen(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          start_new_session=True, **kwargs) as process:
        try:
            out, err = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate()
            raise
        if process.returncode:
            print(out + err, file=sys.stderr)
            raise subprocess.CalledProcessError(process.returncode, args, out, err)
        return out


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def source_difference(first, second):
    def inventory(path):
        with tarfile.open(path) as archive:
            return {member.name: {
                "content": hashlib.file_digest(archive.extractfile(member), "sha256").hexdigest() if member.isfile() else None,
                "metadata": (member.mtime, member.mode, member.uid, member.gid, member.uname, member.gname, member.type.decode()),
            } for member in archive.getmembers()}
    a, b = inventory(first), inventory(second)
    content = [name for name in a.keys() | b.keys() if name not in a or name not in b or a[name]["content"] != b[name]["content"]]
    metadata = [name for name in a.keys() & b.keys() if a[name]["metadata"] != b[name]["metadata"]]
    return {"contentDifferences": content[:3], "contentCount": len(content),
            "metadataCount": len(metadata), "metadataExamples": [
                {"name": name, "first": a[name]["metadata"], "second": b[name]["metadata"]}
                for name in sorted(metadata)[:3]]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--panel", required=True, type=Path)
    args = parser.parse_args()
    panel = args.panel.resolve(strict=True)
    info = json.loads(run(str(panel), "release-info", timeout=10))
    if info["modified"] or not info["platform"].startswith("linux-"):
        parser.error("requires a native Linux panel with unmodified release metadata")
    repository = Path(__file__).resolve().parents[2]
    with tempfile.TemporaryDirectory(prefix="3x-ui-package-probe-") as name:
        root = Path(name)
        bundle = root / "x-ui"
        bundle.mkdir()
        shutil.copy2(panel, bundle / "x-ui")
        old = root / "old-install"
        old.write_bytes(b"owned previous installation, must survive\n")
        original = old.read_bytes()
        tag = "fixture-packaging"
        run("sh", str(repository / "tools/managed-release/prepare-linux.sh"),
            str(bundle), info["commit"], tag, info["platform"], cwd=repository, timeout=600)
        arch = info["platform"].removeprefix("linux-")
        arm = arch.removeprefix("armv") if arch.startswith("armv") else ""
        environment = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux",
                       "GOARCH": "arm" if arm else arch, "GOARM": arm}
        repeated_core, repeated_source = root / "repeated-core", root / "repeated-source.tar.gz"
        run("sh", str(repository / "tools/managed-xray/build.sh"), str(repeated_core),
            str(repeated_source), cwd=repository, env=environment, timeout=600)
        core_name = "xray-linux-" + ("arm32" if arm else arch)
        if digest(bundle / "bin" / core_name) != digest(repeated_core):
            raise RuntimeError("managed core rebuild is not byte-identical")
        if digest(bundle / "xray-managed-source.tar.gz") != digest(repeated_source):
            difference = source_difference(bundle / "xray-managed-source.tar.gz", repeated_source)
            raise RuntimeError("managed source archive rebuild is not byte-identical: " + json.dumps(difference))
        archive = root / "bundle.tar.gz"
        run("tar", "--format=gnu", "-czf", str(archive), "-C", str(root), "x-ui", timeout=120)
        stage = Path(run(str(bundle / "update-stage"), "--archive", str(archive),
                         "--sha256", digest(archive), "--parent", str(root),
                         "--release-commit", info["commit"], "--release-tag", tag,
                         "--release-platform", info["platform"], timeout=120).strip())
        if stage.parent != root or not stage.name.startswith(".x-ui-stage-"):
            raise RuntimeError("staging escaped the owned probe directory")
        environment = {**os.environ, "TMPDIR": str(root)}
        result = run(str(stage / "x-ui/x-ui"), "verify-release", "--directory", str(stage / "x-ui"),
                     "--commit", info["commit"], "--tag", tag, "--platform", info["platform"],
                     env=environment, timeout=30)
        if "managed routing verified" not in result or old.read_bytes() != original:
            raise RuntimeError("runtime verification failed or previous installation changed")
        if list(root.glob(".3x-ui-release-probe-*")):
            raise RuntimeError("runtime probe files were not cleaned")
        manifest = json.loads((stage / "x-ui/release.json").read_text())
        with tarfile.open(stage / "x-ui/xray-managed-source.tar.gz") as source:
            config = source.extractfile("source/proxy/trojan/managed.go").read()
            if b"3x-ui-managed/1" not in config:
                raise RuntimeError("distributed source lacks the managed configuration")
            source.getmember("source/go.mod")
            source.getmember("source/LICENSE")
        print(json.dumps({"platform": info["platform"], "declaredCommit": info["commit"],
                          "archiveBytes": archive.stat().st_size, "files": len(manifest["files"]),
                          "managedRouting": True, "previousInstallPreserved": True,
                          "coreAndSourceRebuiltIdentically": True}))


if __name__ == "__main__":
    main()
