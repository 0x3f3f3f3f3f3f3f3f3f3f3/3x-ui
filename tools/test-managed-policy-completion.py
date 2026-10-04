#!/usr/bin/env python3
"""Exercise the acceptance verifier against incomplete and dishonest logs."""
import copy
import importlib.util
import json
from pathlib import Path
import sys
import unittest

sys.dont_write_bytecode = True


class ManagedCompletionTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location(
            'managed_completion', Path(__file__).with_name('verify-managed-policy-completion.py'))
        self.helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.helper)
        self.sha = 'a' * 64

    def events(self, gate='physical', backend='sqlite'):
        result = []
        for package, names in self.helper.PARENTS[gate].items():
            for name in names:
                result += [{'Action': 'run', 'Package': package, 'Test': name},
                           {'Action': 'pass', 'Package': package, 'Test': name}]
                if name.startswith('TestManagedPolicyTwoPhysicalNodes'):
                    for node in ('physical-node-a', 'physical-node-b'):
                        result.append({'Action': 'output', 'Package': package, 'Test': name,
                                       'Output': f'physical node {node} backend: {backend} coreSHA256: {self.sha}\n'})
            result.append({'Action': 'pass', 'Package': package})
        result.append({'Action': 'output', 'Package': package,
                       'Output': f'managed policy product HTTP backend: {backend}\n'})
        return result

    def verify(self, events, gate='physical', backend='sqlite', sha=None):
        self.helper.verify(gate, backend, '\n'.join(json.dumps(e) for e in events), sha or self.sha)

    def test_completed_exact_owners_on_both_backends(self):
        for gate in ('physical', 'product'):
            for backend in ('sqlite', 'postgres'):
                self.verify(self.events(gate, backend), gate, backend)

    def test_false_success_is_rejected(self):
        good = self.events()
        invalid = [[], good[:-2], good + [good[1]],
                   [e for e in good if not (e['Action'] == 'pass' and 'Test' not in e)],
                   [e for e in good if e['Action'] != 'run'],
                   good + [{'Action': 'pass', 'Package': 'unselected/package'}],
                   good + [{'Action': 'skip', 'Package': good[0]['Package'], 'Test': good[0]['Test'] + '/missing-core'}],
                   good + [{'Action': 'fail', 'Package': good[0]['Package'], 'Test': good[0]['Test'] + '/business'}],
                   good + [{'Action': 'build-fail', 'Package': good[0]['Package']}],
                   good + [{'Action': 'unexpected', 'Package': good[0]['Package']}],
                   good + [{'Action': 'output', 'Package': good[0]['Package'], 'Output': 'WARNING: DATA RACE'}],
                   good + [{'Action': 'output', 'Package': good[0]['Package'], 'Output': 'warning: no tests to run'}],
                   self.events(backend='postgres')]
        wrong_hash = copy.deepcopy(good)
        missing_node = copy.deepcopy(good)
        for e in wrong_hash:
            if 'Output' in e: e['Output'] = e['Output'].replace(self.sha, 'b' * 64)
        missing_node = [e for e in missing_node if 'physical-node-b' not in e.get('Output', '')]
        invalid += [wrong_hash, missing_node]
        for index, events in enumerate(invalid):
            with self.subTest(index=index), self.assertRaises(ValueError): self.verify(events)

    def test_malformed_truncated_and_absent_binary_are_rejected(self):
        log = '\n'.join(json.dumps(e) for e in self.events())
        for broken in ('', log + '\n{"Action":', log + '\nFAIL build', log + '\n{}'):
            with self.subTest(log=broken[-40:]), self.assertRaises(ValueError):
                self.helper.verify('physical', 'sqlite', broken, self.sha)
        for sha in ('', 'bad', 'A' * 64):
            with self.subTest(sha=sha), self.assertRaises(ValueError):
                self.helper.verify('physical', 'sqlite', log, sha)
        for gate, backend in (('absent', 'sqlite'), ('physical', 'unknown')):
            with self.assertRaises(ValueError): self.helper.verify(gate, backend, log, self.sha)


if __name__ == '__main__':
    unittest.main()
