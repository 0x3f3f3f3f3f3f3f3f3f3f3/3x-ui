#!/usr/bin/env python3
"""Prepare complete, checksum-pinned geodata for core tests, never distribution."""
import argparse
import hashlib
import os
from pathlib import Path
import tempfile
import time
import urllib.error
import urllib.request

RELEASE = '202610012207'
BASE = f'https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/{RELEASE}'
# Publisher .sha256sum files and GitHub release asset digests agree on these
# complete original files. Do not replace them with generated test-only rules.
ASSETS = {
    'geoip.dat': {'url': BASE + '/geoip.dat', 'size': 16515432, 'sha256': '391b522361c52804e486a98b53d97f3d9c1d3e4e4217bb34954774a0b456b9fd'},
    'geosite.dat': {'url': BASE + '/geosite.dat', 'size': 10990918, 'sha256': '446812233bb3080a82ae3863a0e7f7994bdabcad4a773121111a375a3c903c70'},
}


def verify(path, asset):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != asset['size']:
        raise ValueError(f'geodata input is not a regular file of the pinned size: {path}')
    with path.open('rb') as source:
        actual = hashlib.file_digest(source, 'sha256').hexdigest()
    if actual != asset['sha256']:
        raise ValueError(f'geodata checksum differs from pinned source: {path}')


def open_source(url):
    for attempt in range(3):
        try:
            return urllib.request.urlopen(url, timeout=60)
        except (urllib.error.URLError, TimeoutError):
            if attempt == 2:
                raise
            time.sleep(attempt + 1)


def prepare(destination, assets=ASSETS, open_url=open_source):
    destination = Path(destination)
    if destination.is_symlink():
        raise ValueError('resource directory cannot be a symlink')
    destination.mkdir(parents=True, exist_ok=True)
    # Existing inputs must already match. Preserve older/different resources
    # and fail instead of overwriting an engineering checkout's prior evidence.
    for name, asset in assets.items():
        target = destination / name
        if target.exists() or target.is_symlink():
            verify(target, asset)
    for name, asset in assets.items():
        target = destination / name
        if target.exists():
            continue
        fd, staged_name = tempfile.mkstemp(prefix='.' + name + '-', dir=destination)
        staged = Path(staged_name)
        with os.fdopen(fd, 'wb') as output, open_url(asset['url']) as source:
            size = 0
            while block := source.read(1024 * 1024):
                size += len(block)
                if size > asset['size']:
                    raise ValueError(f'download exceeds pinned geodata size: {name}')
                output.write(block)
        verify(staged, asset)
        # Exclusive promotion preserves an input created concurrently; failed
        # downloads remain distinct staged files for diagnosis.
        os.link(staged, target)
        staged.unlink()
        target.chmod(0o644)
    for name, asset in assets.items():
        verify(destination / name, asset)
        print(f'Verified complete geodata {RELEASE}: {name} sha256={asset["sha256"]}', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dest', type=Path, default=Path(__file__).resolve().parents[1] / 'core/xray/resources')
    args = parser.parse_args()
    prepare(args.dest)


if __name__ == '__main__':
    main()
