#!/usr/bin/env python3
"""Run the actual updater only inside an owned chroot and private namespaces.

Negative acceptance for bootstrap, archive, identity and runtime preflight.
The final case uses a real managed candidate, then refuses the service stop.
It does not claim successful activation, schema migration or rollback coverage.
"""
import argparse
import codecs
import hashlib
import http.client
import io
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import time

# HTTP's numeric-address path still requests IDNA; preload lazy codecs before
# the child deliberately loses access to the host Python standard library.
for encoding in ('idna', 'ascii', 'utf-8', 'latin-1'):
    codecs.lookup(encoding)


def web_fixture(env, mode):
    """Exercise authenticated HTTP and the actual detached updater in this chroot."""
    panel = '/usr/local/x-ui/x-ui'
    env.update({'XUI_DB_FOLDER': '/etc/x-ui', 'XUI_DB_TYPE': 'sqlite', 'XUI_DB_DSN': '',
                'XUI_BIN_FOLDER': '/usr/local/x-ui/bin', 'XUI_LOG_FOLDER': '/var/log/x-ui'})
    Path('/etc/x-ui/x-ui.db').unlink()
    setup = subprocess.run([panel, 'setting', '-username', 'fixture-admin', '-password', 'owned-fixture-only',
                            '-port', '18080', '-listenIP', '127.0.0.1', '-webBasePath', '/fixture/'],
                           env=env, text=True, capture_output=True, timeout=25)
    assert setup.returncode == 0, setup.stderr[-1000:]
    minted = subprocess.run([panel, 'setting', '-getApiToken', 'true', '-tokenName', 'owned-update-fixture'],
                            env=env, text=True, capture_output=True, timeout=15)
    match = re.search(r'^apiToken: (\S+)$', minted.stdout, re.MULTILINE)
    assert minted.returncode == 0 and match, 'fixture API token creation failed'
    token = match.group(1)
    with sqlite3.connect('/etc/x-ui/x-ui.db') as db:
        db.execute('INSERT INTO settings(key,value) VALUES(?,?)', ('ownedUpdateSentinel', 'keep this configuration'))

    def request(method, path, body=None, language='en-US'):
        connection = http.client.HTTPConnection('127.0.0.1', 18080, timeout=2)
        try:
            connection.request(method, '/fixture/panel/api/server/' + path, body=body,
                               headers={'Authorization': 'Bearer ' + token,
                                        'Accept-Language': language,
                                        'Content-Type': 'application/x-www-form-urlencoded'})
            response = connection.getresponse()
            payload = response.read(1 << 20)
            assert response.status == 200, (response.status, payload[:400])
            return json.loads(payload)
        finally:
            connection.close()

    with Path('/fixture/panel.log').open('wb') as log:
        process = subprocess.Popen([panel, 'run'], cwd='/usr/local/x-ui', env=env,
                                   stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 15
            while True:
                assert process.poll() is None, Path('/fixture/panel.log').read_text()[-1500:]
                try:
                    ready = request('GET', 'getUpdateStatus')
                    assert ready['success'], ready
                    break
                except OSError:
                    assert time.monotonic() < deadline, 'owned panel HTTP startup timed out'
                    time.sleep(0.05)
            started = request('POST', 'updatePanel', 'dev=' + ('true' if mode == 'web-dev' else 'false'))
            if mode in ['web-changed-script', 'web-docker', 'web-dirty']:
                reason = {'web-docker': 'container image', 'web-changed-script': 'installed updater',
                          'web-dirty': 'unmodified panel'}[mode]
                assert not started['success'] and reason in started['msg'], started
                assert not Path('/etc/x-ui/update-status.json').exists()
                if mode in ['web-docker', 'web-dirty']:
                    chinese = request('POST', 'updatePanel', 'dev=false', 'zh-CN')
                    translated = '请通过容器运行时' if mode == 'web-docker' else '已知且未修改'
                    assert not chinese['success'] and translated in chinese['msg'], chinese
                    fallback = request('POST', 'updatePanel', 'dev=false', 'uk-UA')
                    assert not fallback['success'] and reason in fallback['msg'], fallback
                result = {'rejectedBeforeLaunch': True}
            else:
                assert started['success'] and started['obj']['runId'], started
                run_id = started['obj']['runId']
                deadline = time.monotonic() + 10
                while True:
                    status = request('GET', 'getUpdateStatus')
                    assert status['success'], status
                    status = status['obj']
                    if status.get('runId') == run_id and status['state'] != 'pending':
                        break
                    assert time.monotonic() < deadline, 'detached updater did not publish its terminal status'
                    time.sleep(0.05)
                assert status['state'] == 'failed' and status['exitCode'] == 2, status
                result = {'runIdMatched': True, 'failedDownloadReported': True}
            assert process.poll() is None, 'validation stopped the running panel'
            with sqlite3.connect('/etc/x-ui/x-ui.db') as db:
                value = db.execute('SELECT value FROM settings WHERE key=?', ('ownedUpdateSentinel',)).fetchone()
                assert value == ('keep this configuration',), value
            result.update({'actualHTTP': True, 'panelStillRunning': True, 'databaseSettingPreserved': True})
            print(json.dumps(result), flush=True)
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()

if not __debug__:
    raise SystemExit('assertions are required')

if len(sys.argv) > 1 and sys.argv[1] == '--child':
    root, host_net, host_mount, marker = sys.argv[2:]
    assert Path('/proc/self/stat').read_text().split()[0] == '1'
    assert os.readlink('/proc/self/ns/net') != host_net
    assert os.readlink('/proc/self/ns/mnt') != host_mount
    assert Path(root).resolve() == Path(root) and root.startswith('/tmp/3x-ui-update-probe-')
    assert Path(root, '.fixture').read_text() == marker
    subprocess.run(['ip', 'link', 'set', 'lo', 'up'], check=True)
    subprocess.run(['mount', '-t', 'proc', 'proc', str(Path(root, 'proc'))], check=True)
    env = json.loads(Path(root, 'fixture/env.json').read_text())
    os.chroot(root)
    os.chdir('/')
    mode = env.pop('FIXTURE_WEB', '')
    if mode:
        web_fixture(env, mode)
        raise SystemExit(0)
    if env.pop('FIXTURE_MENU', ''):
        command = env.pop('FIXTURE_MENU_COMMAND')
        os.execve('/bin/bash', ['bash', '/usr/bin/x-ui', command], env)
    os.execve('/bin/bash', ['bash', '/update.sh'], env)

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--helper', required=True, type=Path)
parser.add_argument('--panel', required=True, type=Path)
parser.add_argument('--managed-core', required=True, type=Path)
parser.add_argument('--stock-core', required=True, type=Path)
parser.add_argument('--menu', action='store_true', help='exercise actual stable/dev menu update entry points')
parser.add_argument('--web', action='store_true', help='exercise authenticated HTTP and detached update entry points')
parser.add_argument('--dirty-source-rejection', action='store_true', help='web rejection probe with an actually modified-source build')
args = parser.parse_args()
assert not (args.menu and args.web), 'choose menu or web probe'
assert not args.dirty_source_rejection or args.web, 'dirty-source probe requires --web'
repo = Path(__file__).resolve().parents[2]
busybox = Path('/usr/bin/busybox')
assert busybox.is_file(), 'requires a static BusyBox fixture'
info = json.loads(subprocess.check_output([str(args.panel), 'release-info'], text=True))
assert info['repository'] == '0x3f3f3f3f3f3f3f3f3f3f3/3x-ui' and info['modified'] == args.dirty_source_rejection
assert re.fullmatch('[0-9a-f]{40}', info['commit'])
platform = info['platform']
core_name = 'xray-' + ("linux-arm32" if platform in ['linux-armv5', 'linux-armv6', 'linux-armv7'] else platform)
with args.helper.open('rb') as stream:
    helper_sha = hashlib.file_digest(stream, 'sha256').hexdigest()
libraries = set()
for binary in [Path('/bin/bash'), args.helper, args.panel, args.managed_core, args.stock_core]:
    result = subprocess.run(['ldd', str(binary)], text=True, capture_output=True)
    libraries.update(re.findall(r'(/[^\s]+)', result.stdout))
assert all(Path(p).is_file() for p in libraries)

def write(root, path, content, executable=False):
    p = root / path.lstrip('/')
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content)
    if executable:
        p.chmod(0o755)

def sha(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()

curl = r'''#!/bin/bash
url="${!#}"
printf 'curl %s\n' "$url" >> /fixture/actions
case "$url" in
    https://github.com/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui/releases/*/update-stage-*.sha256)
        if [[ $(cat /fixture/mode) == bootstrap-missing-checksum ]]; then exit 22; fi
        printf '%064d  update-stage-%s\n' 0 "$(cat /fixture/platform)" ;;
    https://github.com/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui/releases/*/update-stage-*)
        cat /fixture/helper ;;
    *) printf 'unexpected curl request\n' >&2; exit 91 ;;
esac
'''
report = []
cases = ['menu-stable', 'menu-dev', 'menu-changed-script'] if args.menu else ['bootstrap-missing-checksum', 'bootstrap-wrong-checksum', 'bad-checksum',
             'corrupt-archive', 'missing-panel', 'missing-unit', 'wrong-source',
             'stock-core', 'service-stop-failure']
if args.web:
    cases = ['web-dirty'] if args.dirty_source_rejection else ['web-stable', 'web-dev', 'web-changed-script', 'web-docker']
for mode in cases:
    with tempfile.TemporaryDirectory(prefix='3x-ui-update-probe-', dir='/tmp') as tmp:
        root = Path(tmp)
        marker = os.urandom(16).hex()
        write(root, '.fixture', marker)
        for path in [Path('/bin/bash'), *map(Path, sorted(libraries))]:
            dest = root / str(path).lstrip('/')
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, dest)
        shutil.copy2(busybox, root / 'bin/busybox')
        for applet in ['uname','which','dirname','readlink','basename','rm','mkdir','mv',
                       'awk','sed','sha256sum','tar','gzip','date','cat','tr','grep','touch',
                       'chmod','chown','cp','head','wc','mktemp','cut','ps']:
            (root / 'bin' / applet).symlink_to('busybox')
        for path in ['proc','tmp','etc/x-ui','usr/bin','etc/init.d']:
            (root/path).mkdir(parents=True, exist_ok=True)
        write(root, '/etc/os-release', 'ID=debian\nVERSION_ID=12\n')
        write(root, '/dev/null', '')
        (root/'dev/fd').symlink_to('/proc/self/fd')
        write(root, '/fixture/actions', '')
        write(root, '/fixture/mode', mode)
        write(root, '/fixture/platform', platform)
        shutil.copy2(args.helper, root / 'fixture/helper')
        write(root, '/bin/curl', curl, True)
        for command in ['apt-get','systemctl','pkill']:
            write(root, '/bin/'+command,
                  '#!/bin/bash\nprintf "'+command+' %s\\n" "$*" >> /fixture/actions\n'
                  + ('[[ "$1" == stop ]] && exit 75\n' if command == 'systemctl' else '')
                  + 'exit 0\n', True)
        write(root, '/usr/local/x-ui/x-ui', '#!/bin/bash\nprintf "fixture-old-panel\\n"\n', True)
        write(root, '/usr/local/x-ui/bin/'+core_name, 'fixture-old-core\n', True)
        write(root, '/etc/systemd/system/x-ui.service', 'fixture-old-unit\n')
        write(root, '/usr/bin/x-ui', 'fixture-old-menu\n')
        write(root, '/etc/x-ui/x-ui.db', 'fixture-existing-database\n')
        archive = root / 'fixture/archive'
        archive.write_bytes(b'deliberately not a tar archive\n')
        if mode == 'bad-checksum':
            with tarfile.open(archive, 'w:gz', format=tarfile.GNU_FORMAT, compresslevel=1) as tar:
                payload = b'fixture archive with a deliberately wrong supplied hash\n'
                entry = tarfile.TarInfo('x-ui/x-ui')
                entry.size, entry.mode = len(payload), 0o755
                tar.addfile(entry, io.BytesIO(payload))
        commit = info['commit']
        if args.menu or args.web or mode in ['missing-panel','missing-unit','wrong-source','stock-core','service-stop-failure']:
            source = root / 'fixture/source'
            (source/'bin').mkdir(parents=True)
            for name in ['update.sh','install.sh','x-ui.sh','x-ui.rc','x-ui.service.debian','x-ui.service.arch','x-ui.service.rhel']:
                shutil.copy2(repo/name, source/name)
                (source/name).chmod(0o644 if '.service.' in name else 0o755)
            shutil.copy2(args.helper, source/'update-stage')
            shutil.copy2(args.panel, source/'x-ui')
            shutil.copy2(args.stock_core if mode == 'stock-core' else args.managed_core, source/'bin'/core_name)
            if mode == 'missing-panel': (source/'x-ui').unlink()
            if mode == 'missing-unit': (source/'x-ui.service.debian').unlink()
            if mode == 'wrong-source': commit = '0'*40
            inventory = {str(p.relative_to(source)): {'sha256':sha(p), 'size':p.stat().st_size,
                         'executable':bool(p.stat().st_mode & 0o111)} for p in source.rglob('*') if p.is_file()}
            (source/'release.json').write_text(json.dumps({'schema':1, 'policyABI':1, 'routingABI':1,
                'identity':{'repository':info['repository'],'commit':commit,'tag':'fixture-release','platform':platform},
                'files':inventory}))
            if args.menu or args.web:
                shutil.copytree(source, root/'usr/local/x-ui', dirs_exist_ok=True)
                shutil.copy2(repo/'x-ui.sh', root/'usr/bin/x-ui')
                write(root, '/usr/local/x-ui/bin/runtime.json', 'existing runtime configuration')
                if mode in ['menu-changed-script', 'web-changed-script']:
                    write(root, '/usr/local/x-ui/update.sh', 'altered installed updater', True)
            else:
                with tarfile.open(archive, 'w:gz', format=tarfile.GNU_FORMAT, compresslevel=1) as tar:
                    tar.add(source, arcname='x-ui')
        preserved_paths = ['usr/local/x-ui/x-ui','usr/local/x-ui/bin/'+core_name,
                           'etc/systemd/system/x-ui.service','usr/bin/x-ui']
        if not args.web:
            preserved_paths.append('etc/x-ui/x-ui.db')
        sentinels = {p: sha(root/p) for p in preserved_paths}
        digest = '0'*64 if mode == 'bad-checksum' else sha(archive)
        env = {'PATH':'/bin','LANG':'C','TMPDIR':'/tmp','XUI_UPDATE_TAG':'fixture-release',
               'XUI_MAIN_FOLDER':'/usr/local/x-ui','XUI_SERVICE':'/etc/systemd/system',
               'XUI_UPDATE_STATUS_FILE':'/etc/x-ui/update-status.json','XUI_ENABLE_FAIL2BAN':'false'}
        if args.web:
            env['FIXTURE_WEB'] = mode
            env.pop('XUI_UPDATE_TAG')
            if mode == 'web-docker':
                env['XUI_IN_DOCKER'] = 'true'
        elif args.menu:
            env['FIXTURE_MENU'] = 'true'
            env['FIXTURE_MENU_COMMAND'] = 'update-dev' if mode == 'menu-dev' else 'update'
            env.pop('XUI_UPDATE_TAG')
        elif not mode.startswith('bootstrap-'):
            env.update({'XUI_UPDATE_ARCHIVE':'/fixture/archive','XUI_UPDATE_SHA256':digest,
                        'XUI_UPDATE_COMMIT':commit,'XUI_UPDATE_HELPER':'/fixture/helper',
                        'XUI_UPDATE_HELPER_SHA256':helper_sha})
        write(root, '/fixture/env.json', json.dumps(env))
        shutil.copy2(repo/'update.sh', root/'update.sh')
        proc = subprocess.run(['unshare','--mount-proc','--net','--pid','--fork','--kill-child=SIGKILL',
            sys.executable,str(Path(__file__).resolve()),'--child',tmp,os.readlink('/proc/self/ns/net'),
            os.readlink('/proc/self/ns/mnt'),marker], input='y\n\n0\n' if args.menu else None,
            text=True,capture_output=True,timeout=8 if args.menu else 65 if args.web else 40)
        actions = (root/'fixture/actions').read_text().splitlines()
        preserved = all((root/p).is_file() and sha(root/p)==digest for p,digest in sentinels.items())
        stopped = 'systemctl stop x-ui' in actions
        case = {'case':mode,'exit_code':proc.returncode,'old_files_preserved':preserved,
                'service_stop_attempted':stopped,'actions':actions,'stderr':proc.stderr[-1500:],
                'output_tail':proc.stdout.splitlines()[-6:]}
        report.append(case)
        print(json.dumps(case), flush=True)
        assert (proc.returncode == 0 if args.web else proc.returncode != 0) and preserved, case
        expected_failure = {
            'bootstrap-missing-checksum': 'Failed to download',
            'bootstrap-wrong-checksum': 'helper checksum mismatch',
            'bad-checksum': 'archive SHA256 mismatch', 'corrupt-archive': 'invalid header',
            'missing-panel': 'nonempty executable', 'missing-unit': 'nonempty nonexecutable unit',
            'wrong-source': 'compiled panel source/platform', 'stock-core': 'managed routing verification failed',
            'service-stop-failure': 'Cannot stop the installed service',
            'menu-stable': 'helper checksum mismatch', 'menu-dev': 'helper checksum mismatch',
            'menu-changed-script': 'installed updater',
            'web-stable': 'failedDownloadReported', 'web-dev': 'failedDownloadReported',
            'web-changed-script': 'rejectedBeforeLaunch', 'web-docker': 'rejectedBeforeLaunch',
            'web-dirty': 'rejectedBeforeLaunch',
        }[mode]
        assert expected_failure in proc.stdout + proc.stderr, case
        if mode == 'service-stop-failure':
            assert stopped and 'runtime preflight passed' in proc.stdout, case
        else:
            assert not stopped and not any(x.startswith(('apt-get','pkill')) for x in actions), case
        assert not list((root/'usr/local').glob('.x-ui-update-*')), case
        assert not list((root/'tmp').glob('3x-ui-update-*.sh')), case
        if mode in ['menu-changed-script', 'web-changed-script', 'web-docker', 'web-dirty']:
            assert not any(x.startswith('curl') for x in actions), case
            assert not (root/'etc/x-ui/update-status.json').exists(), case
        else:
            status = json.loads((root/'etc/x-ui/update-status.json').read_text())
            assert status['state'] == 'failed' and status['exitCode'] != 0, case
        if args.menu or args.web:
            assert not any('raw.githubusercontent.com' in x or 'MHSanaei' in x for x in actions), case
            if mode not in ['menu-changed-script', 'web-changed-script', 'web-docker', 'web-dirty']:
                channel = '/download/dev-latest/' if mode in ['menu-dev', 'web-dev'] else '/latest/download/'
                assert any(channel in x for x in actions), case
print(json.dumps({'passed':len(report),'activation_rollback_tested':False}))
