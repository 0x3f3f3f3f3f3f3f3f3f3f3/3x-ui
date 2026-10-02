#!/usr/bin/env python3
"""Run every test selected by an unchanged Go regexp in bounded package batches."""
import argparse
import json
from pathlib import Path
import re
import subprocess
import sys


def events(command, log, display):
    result = []
    with log.open('x') as output:
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        for line in process.stdout:
            output.write(line)
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                if display:
                    print(line, end='', flush=True)
                continue
            result.append(event)
            if display and event.get('Output'):
                print(event['Output'], end='', flush=True)
        code = process.wait()
    if code:
        raise RuntimeError(f'Go test command failed (exit {code}); retained log: {log}')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', required=True, help='original Go -run selection regexp')
    parser.add_argument('--max-tests', type=int, default=12)
    parser.add_argument('--timeout', default='10m', help='per-batch test guard; keep protocol-specific deadlines in tests')
    parser.add_argument('--log-dir', type=Path, required=True)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--plan-only', action='store_true')
    parser.add_argument('packages', nargs='+')
    args = parser.parse_args()
    if not 1 <= args.max_tests <= 100:
        parser.error('--max-tests must be between 1 and 100')
    args.log_dir.mkdir(parents=True, exist_ok=False)
    common = ['go', 'test', '-json', '-count=1'] + (['-race'] if args.race else [])
    listed = events(common + ['-list', args.run, *args.packages], args.log_dir / 'list.jsonl', False)
    selected = {}
    for event in listed:
        name = event.get('Output', '').strip()
        if event.get('Package') and re.fullmatch(r'Test\w+', name):
            selected.setdefault(event['Package'], set()).add(name)
    if not selected:
        raise RuntimeError('original selection matched no tests')
    batches = []
    for package, names in sorted(selected.items()):
        names = sorted(names)
        for offset in range(0, len(names), args.max_tests):
            batches.append({'package': package, 'tests': names[offset:offset + args.max_tests]})
    plan = {'originalRun': args.run, 'maxTests': args.max_tests, 'timeout': args.timeout, 'selected': {p: sorted(n) for p, n in sorted(selected.items())}, 'batches': batches}
    (args.log_dir / 'selection.json').write_text(json.dumps(plan, indent=2) + '\n')
    print(f'Selected {sum(map(len, selected.values()))} tests in {len(batches)} bounded package batches; guard={args.timeout}', flush=True)
    if args.plan_only:
        return
    for index, batch in enumerate(batches, 1):
        names = batch['tests']
        pattern = '^(' + '|'.join(map(re.escape, names)) + ')$'
        print(f'Batch {index}/{len(batches)}: {batch["package"]} ({len(names)} tests)', flush=True)
        output = events(common + ['-v', '-timeout', args.timeout, '-run', pattern, batch['package']], args.log_dir / f'batch-{index:03}.jsonl', True)
        passed = [e['Test'] for e in output if e.get('Action') == 'pass' and '/' not in e.get('Test', '/')]
        ran = [e['Test'] for e in output if e.get('Action') == 'run' and '/' not in e.get('Test', '/')]
        if sorted(passed) != sorted(names) or sorted(ran) != sorted(names):
            missing = sorted(set(names) - set(passed))
            raise RuntimeError(f'selected tests did not pass exactly once: {missing}; retained log: {args.log_dir}')
    print('Every originally selected test passed exactly once; all nested cases were retained.', flush=True)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError) as error:
        print(error, file=sys.stderr)
        sys.exit(1)
