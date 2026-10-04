#!/usr/bin/env python3
"""Acceptance log failures must never be mistaken for mapping completion."""
import importlib.util
from pathlib import Path
import sys
import unittest

sys.dont_write_bytecode = True


class MappingCompletionTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location(
            'mapping_completion', Path(__file__).with_name('verify-client-mapping-completion.py'))
        self.helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.helper)
        self.log = '\n'.join(
            ['--- PASS: ' + name + ' (0.10s)' for name in self.helper.PARENTS]
            + [marker + 'sqlite' for marker in self.helper.MARKERS])

    def test_all_literal_parents_and_actual_backend_markers_pass(self):
        self.helper.verify('sqlite', self.log)
        self.helper.verify('postgres', self.log.replace('sqlite', 'postgres'))

    def test_incomplete_or_false_success_is_rejected(self):
        name = self.helper.PARENTS[0]
        marker = self.helper.MARKERS[0]
        invalid = [
            '',
            self.log.replace('--- PASS: ' + name + ' ', '    --- PASS: ' + name + '/nested '),
            self.log + '\n--- PASS: ' + name + ' (0.10s)',
            self.log + '\n    --- SKIP: ' + name + '/missing-core (0.00s)',
            self.log + '\n    --- FAIL: ' + name + '/business (0.00s)',
            self.log + '\nFAIL\tfixture',
            self.log + '\nwarning: no tests to run',
            self.log.replace(marker + 'sqlite', marker + 'postgres'),
            self.log + '\n' + marker + 'sqlite',
            self.log.replace(marker + 'sqlite', ''),
        ]
        for log in invalid:
            with self.subTest(log=log[-100:]):
                with self.assertRaises(ValueError):
                    self.helper.verify('sqlite', log)
        with self.assertRaises(ValueError):
            self.helper.verify('unknown', self.log)


if __name__ == '__main__':
    unittest.main()
