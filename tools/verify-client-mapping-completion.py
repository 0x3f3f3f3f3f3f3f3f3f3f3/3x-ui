#!/usr/bin/env python3
"""Require exact owning mapping parents and actual database backend evidence."""
import re
import sys
from pathlib import Path

PARENTS = (
    'TestNodeClientMappingRequiresOwnedFreshPolicy',
    'TestNodeClientMappingPreservesOriginalEvidence',
    'TestCoordinatorClientMappingPinsOriginalJournal',
    'TestNodeClientMappingHTTPAuthenticationAndBounds',
    'TestClientMappingEffectivePolicyDigestIgnoresOnlyIdentityAndVersion',
    'TestNodeClientMappingJSONCanonicalAndBounded',
    'TestRemoteClientMappingRequiresVerifiedProof',
    'TestMappedRemoteAuthorityAPITranslatesCanonicalIdentity',
    'TestNodeClientMappingHTTPActualCanonicalGrant',
)
MARKERS = (
    'node client mapping backend: ',
    'node client mapping HTTP backend: ',
    'coordinator client mapping backend: ',
    'node client mapping canonical HTTP backend: ',
    'node client mapping local issuer HTTP backend: ',
)


def verify(backend, log):
    if backend not in ('sqlite', 'postgres'):
        raise ValueError('expected sqlite or postgres')
    passed = re.findall(r'^--- PASS: (Test\w+) ', log, re.M)
    if any(passed.count(name) != 1 for name in PARENTS):
        raise ValueError('missing or duplicate owning mapping parent')
    if re.search(r'^\s*--- (?:FAIL|SKIP):|^FAIL(?:\s|$)|no tests to run', log, re.M):
        raise ValueError('mapping acceptance failed, skipped or absent')
    for marker in MARKERS:
        if re.findall(re.escape(marker) + r'(\w+)', log) != [backend]:
            raise ValueError('missing, duplicate or wrong backend marker: ' + marker)


if __name__ == '__main__':
    backend, path = sys.argv[1:]
    verify(backend, Path(path).read_text())
    print(f'mapping {backend}: {len(PARENTS)} exact parents passed')
