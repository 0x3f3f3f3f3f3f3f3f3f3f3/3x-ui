#!/usr/bin/env python3
"""Behavior checks for geodata integrity and complete bounded Go test selection."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

# Dynamic helper imports must not leave generated files beside reviewable source.
sys.dont_write_bytecode = True

TOOLS = Path(__file__).resolve().parent


def load(name):
    path = TOOLS / (name + '.py')
    if not path.exists():
        raise AssertionError('CI helper is not implemented: ' + str(path))
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class GeodataIntegrityTests(unittest.TestCase):
    def test_validated_full_inputs_are_written_without_replacing_prior_files(self):
        helper = load('prepare-core-test-geodata')
        content = b'complete source bytes used solely to test transport integrity\n'
        assets = {'geoip.dat': {'url': 'https://example.invalid/pinned/geoip.dat', 'sha256': hashlib.sha256(content).hexdigest(), 'size': len(content)}}
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            helper.prepare(root, assets, lambda _: io.BytesIO(content))
            self.assertEqual((root / 'geoip.dat').read_bytes(), content)
            helper.prepare(root, assets, lambda _: self.fail('verified existing input must not be downloaded again'))
            (root / 'geoip.dat').write_bytes(b'prior different asset')
            with self.assertRaises(ValueError):
                helper.prepare(root, assets, lambda _: io.BytesIO(content))
            self.assertEqual((root / 'geoip.dat').read_bytes(), b'prior different asset')

    def test_wrong_digest_or_symlink_never_becomes_accepted_resource(self):
        helper = load('prepare-core-test-geodata')
        assets = {'geosite.dat': {'url': 'https://example.invalid/pinned/geosite.dat', 'sha256': '0' * 64, 'size': 6}}
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            with self.assertRaises(ValueError):
                helper.prepare(root, assets, lambda _: io.BytesIO(b'wrong!'))
            self.assertFalse((root / 'geosite.dat').exists())
            # A valid linked target must still be rejected; a bad checksum
            # would otherwise mask a missing symlink guard.
            (root / 'prior').write_bytes(b'wrong!')
            linked_assets = {'geosite.dat': {**assets['geosite.dat'], 'sha256': hashlib.sha256(b'wrong!').hexdigest()}}
            (root / 'geosite.dat').symlink_to(root / 'prior')
            with self.assertRaises(ValueError):
                helper.prepare(root, linked_assets, lambda _: io.BytesIO(b'wrong!'))
            self.assertEqual((root / 'prior').read_bytes(), b'wrong!')


class GoBatchBehaviorTests(unittest.TestCase):
    def run_fixture(self, test_source, pattern='TestSelected', timeout='10m', race_source=None):
        script = TOOLS / 'run-go-test-batches.py'
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            (root / 'go.mod').write_text('module ci-fixture\n\ngo 1.27.1\n')
            for package in ['first', 'second']:
                (root / package).mkdir()
                (root / package / 'fixture_test.go').write_text('package ' + package + '\n' + test_source)
                if race_source is not None:
                    (root / package / 'race_only_test.go').write_text('//go:build race\n\npackage ' + package + '\n' + race_source)
            output = root / 'logs'
            command = ['python3', str(script), '--run', pattern, '--max-tests', '1', '--timeout', timeout, '--log-dir', str(output)]
            if race_source is not None:
                command.append('--race')
            result = subprocess.run(command + ['./first', './second'], cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            plan = json.loads((output / 'selection.json').read_text()) if (output / 'selection.json').exists() else None
            return result, plan

    def test_every_selected_test_and_nested_case_runs_once_per_package(self):
        result, plan = self.run_fixture('''import "testing"
func TestSelectedA(t *testing.T) { t.Run("nested", func(t *testing.T) {}) }
func TestSelectedB(t *testing.T) {}
func TestOtherMustNotRun(t *testing.T) { t.Fatal("selection widened") }
''')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(result.stdout.count('--- PASS: TestSelectedA ('), 2, result.stdout)
        self.assertEqual(result.stdout.count('--- PASS: TestSelectedA/nested ('), 2, result.stdout)
        self.assertEqual(result.stdout.count('--- PASS: TestSelectedB ('), 2, result.stdout)
        self.assertEqual(sum(len(b['tests']) for b in plan['batches']), 4)
        self.assertEqual(max(len(b['tests']) for b in plan['batches']), 1)
        self.assertEqual({b['package'] for b in plan['batches']}, {'ci-fixture/first', 'ci-fixture/second'})

    def test_race_flag_is_preserved_for_both_selection_and_execution(self):
        result, plan = self.run_fixture('import "testing"\nfunc TestOther(t *testing.T) { t.Fatal("selection widened") }\n',
            race_source='import "testing"\nfunc TestSelectedRaceEnabled(t *testing.T) {}\n')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(result.stdout.count('--- PASS: TestSelectedRaceEnabled ('), 2, result.stdout)
        self.assertEqual(sum(len(b['tests']) for b in plan['batches']), 2)

    def test_skipped_selected_case_fails_instead_of_counting_as_pass(self):
        result, _ = self.run_fixture('import "testing"\nfunc TestSelectedA(t *testing.T) { t.Skip("must be detected") }\n')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('selected tests did not pass', result.stdout)

    def test_test_failure_and_deadline_remain_failures(self):
        result, _ = self.run_fixture('import "testing"\nfunc TestSelectedA(t *testing.T) { t.Fatal("original failure") }\n')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('original failure', result.stdout)
        result, _ = self.run_fixture('import ("testing"; "time")\nfunc TestSelectedA(t *testing.T) { time.Sleep(3*time.Second) }\n', timeout='100ms')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('test timed out after', result.stdout)


if __name__ == '__main__':
    unittest.main()
