#!/usr/bin/env python3
"""Require completed owning JSON tests, actual backends and physical core hashes."""
import argparse
from collections import Counter, defaultdict
import hashlib
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
PREFIX = 'github.com/mhsanaei/3x-ui/v3/'
PARENTS = {
    'physical': {PREFIX + 'internal/sub': (
        'TestManagedPolicyTwoPhysicalNodesActualTunnelBilling',
        'TestManagedPolicyTwoPhysicalNodesActualNativeProtocols',
        'TestManagedPolicyTwoPhysicalNodesActualScopeQuotas',
        'TestManagedPolicyTwoPhysicalNodesActualDirectionalRateAndBurst',
        'TestManagedPolicyTwoPhysicalNodesActualPartitionExpiryAndCoordinatorRecovery',
        'TestManagedPolicyTwoPhysicalNodesActualRestartRetainsUnsealedBudget',
        'TestManagedPolicyTwoPhysicalNodesActualAutomaticRecovery',
        'TestManagedPolicyTwoPhysicalNodesActualDeletionClosesOriginalAccounts',
        'TestManagedPolicyTwoPhysicalNodesActualPartitionedDeletionRecovery',
        'TestMieruHTTPExportRealCoreLifecycle',
        'TestSnellHTTPExportRealCoreTCPUDPQUICAndSharedLifecycle',
        'TestSSHHTTPExportRealOpenSSHAndSharedLifecycle',
        'TestSSHHTTPAuthorizedReverseAndStrictNativeOutbound',
        'TestClientPolicyBulkHTTPNativeBindingsAndTunnelAccounting',
    )},
    'product': {
        PREFIX + 'internal/web/service': (
            'TestManagedPolicyProductStatusRequiresExplicitOriginalActivation',
            'TestManagedPolicyProductAccountPagesPreserveOriginalFractionAndScope',
            'TestManagedPolicyProductMaximumAccountPageContinuesWithinEnvelope',
            'TestManagedPolicyProductEnrollmentRejectsInactiveAndMalformedRequests',
        ),
        PREFIX + 'internal/web/controller': (
            'TestManagedPolicyProductHTTPAuthenticationAndExplicitActivation',
            'TestManagedPolicyProductResponseIsBoundedBeforeWriting',
        ),
        PREFIX + 'internal/policyauthority': (
            'TestManagedAccountPagesReadOriginalParentAndAtomicBalances',
            'TestClientMappingAccountLookupUsesOriginalReverseProof',
        ),
        PREFIX + 'tools/openapigen': (
            'TestGeneratePolicyScopeAndAccountContracts',
            'TestWalkPackagesExportsAllowedStructsWithoutDIInterfaces',
            'TestIntegerSchemaFormats',
        ),
    },
}
PATTERNS = {
    'physical': '^Test(ManagedPolicyTwoPhysicalNodesActual.*|MieruHTTPExportRealCoreLifecycle|SnellHTTPExportRealCoreTCPUDPQUICAndSharedLifecycle|SSHHTTPExportRealOpenSSHAndSharedLifecycle|SSHHTTPAuthorizedReverseAndStrictNativeOutbound|ClientPolicyBulkHTTPNativeBindingsAndTunnelAccounting)$',
    'product': '^Test(ManagedPolicyProduct.*|ManagedAccountPagesReadOriginalParentAndAtomicBalances|ClientMappingAccountLookupUsesOriginalReverseProof|GeneratePolicyScopeAndAccountContracts|WalkPackagesExportsAllowedStructsWithoutDIInterfaces|IntegerSchemaFormats)$',
}


def verify(gate, backend, log, binary_sha):
    if gate not in PARENTS or backend not in ('sqlite', 'postgres'):
        raise ValueError('unknown acceptance gate or backend')
    if not re.fullmatch('[0-9a-f]{64}', binary_sha):
        raise ValueError('missing actual core hash')
    try:
        events = [json.loads(line) for line in log.splitlines() if line.strip()]
    except (ValueError, TypeError) as error:
        raise ValueError('malformed or truncated JSON acceptance log') from error
    if not events or any(not isinstance(e, dict) or 'Action' not in e or 'Package' not in e for e in events):
        raise ValueError('absent or malformed test events')
    allowed = {'start', 'run', 'pause', 'cont', 'pass', 'bench', 'output', 'build-start', 'build-output'}
    if any(e['Action'] not in allowed for e in events):
        raise ValueError('acceptance failed or skipped, including nested tests')
    outputs = ''.join(e.get('Output', '') for e in events)
    if re.search(r'WARNING: DATA RACE|no tests to run|^FAIL\b', outputs, re.M):
        raise ValueError('race, absent tests or build failure')
    expected = PARENTS[gate]
    for action in ('run', 'pass'):
        actual = defaultdict(list)
        for e in events:
            if e['Action'] == action and 'Test' in e and '/' not in e['Test']:
                actual[e['Package']].append(e['Test'])
        if set(actual) != set(expected) or any(Counter(actual[p]) != Counter(names) for p, names in expected.items()):
            raise ValueError('missing, duplicate or unexpected owning parent ' + action)
    completed = [e['Package'] for e in events if e['Action'] == 'pass' and 'Test' not in e]
    if Counter(completed) != Counter(expected.keys()):
        raise ValueError('incomplete, duplicate or unexpected package completion')
    markers = re.findall(r'backend: (\w+)\b', outputs)
    if not markers or set(markers) != {backend}:
        raise ValueError('missing or wrong actual database backend')
    if gate == 'physical':
        for name in expected[PREFIX + 'internal/sub']:
            if not name.startswith('TestManagedPolicyTwoPhysicalNodes'):
                continue
            owned = ''.join(e.get('Output', '') for e in events if e.get('Test', '').split('/')[0] == name)
            nodes = re.findall(r'physical node (physical-node-[ab]) backend: (\w+) coreSHA256: ([0-9a-f]{64})', owned)
            if {n[0] for n in nodes} != {'physical-node-a', 'physical-node-b'} or any(n[1] != backend or n[2] != binary_sha for n in nodes):
                raise ValueError('physical owner missing actual nodes or using another backend/core: ' + name)


def validate_source_owners(gate):
    for package, names in PARENTS[gate].items():
        folder = ROOT / package.removeprefix(PREFIX)
        found = []
        for path in folder.glob('*_test.go'):
            found += [name for name in re.findall(r'^func (Test\w+)\(', path.read_text(), re.M)
                      if re.fullmatch(PATTERNS[gate], name)]
        if Counter(found) != Counter(names):
            raise ValueError('literal acceptance owners do not match current source: ' + package)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('gate', choices=PARENTS)
    parser.add_argument('backend', choices=('sqlite', 'postgres'))
    parser.add_argument('log', type=Path)
    parser.add_argument('binary', type=Path)
    args = parser.parse_args()
    validate_source_owners(args.gate)
    with args.binary.open('rb') as file:
        sha = hashlib.file_digest(file, 'sha256').hexdigest()
    verify(args.gate, args.backend, args.log.read_text(), sha)
    print(f'{args.gate} {args.backend}: {sum(map(len, PARENTS[args.gate].values()))} exact parents completed; no failures/skips')
