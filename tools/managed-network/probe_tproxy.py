#!/usr/bin/env python3
'''Probe nftables TPROXY in a disposable network/PID namespace; needs root.

This checks Linux socket/routing prerequisites, not Snell interoperability,
per-client policy execution, host firewall coexistence, or production isolation.
'''
import json
import os
import signal
import socket
import struct
import subprocess
import sys
import threading
if not __debug__:
    raise SystemExit('run without Python optimization; probe assertions are required')

def run(*args, data=None):
    p = subprocess.run(args, input=data, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
    if p.returncode:
        raise RuntimeError(f'{args!r}: {p.stderr.strip()}')
    return p.stdout.strip()

def external_ingress(report):
    hold = 'import os,sys; print(os.readlink("/proc/self/ns/net"),flush=True); sys.stdin.read()'
    peer = subprocess.Popen(['unshare', '--net', sys.executable, '-c', hold], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        namespace = peer.stdout.readline().strip()
        assert namespace.startswith('net:[') and namespace != os.readlink('/proc/self/ns/net'), namespace
        assert os.readlink(f'/proc/{peer.pid}/ns/net') == namespace, 'peer PID does not name its network namespace'
        prefix = ['nsenter', '--target', str(peer.pid), '--net', '--']
        run('ip', 'link', 'add', 'probe_a', 'type', 'veth', 'peer', 'name', 'probe_b')
        run('ip', 'link', 'set', 'probe_b', 'netns', str(peer.pid))
        run('ip', 'link', 'set', 'probe_a', 'up')
        run(*prefix, 'ip', 'link', 'set', 'lo', 'up')
        run(*prefix, 'ip', 'link', 'set', 'probe_b', 'up')
        for family, local, remote, mask in [('-4', '10.203.0.1', '10.203.0.2', '24'), ('-6', 'fd00:3f::1', 'fd00:3f::2', '64')]:
            flags = ['nodad'] if family == '-6' else []
            run('ip', family, 'addr', 'add', local + '/' + mask, 'dev', 'probe_a', *flags)
            run(*prefix, 'ip', family, 'addr', 'add', remote + '/' + mask, 'dev', 'probe_b', *flags)
        for family, local, remote in [(socket.AF_INET, '10.203.0.1', '10.203.0.2'), (socket.AF_INET6, 'fd00:3f::1', 'fd00:3f::2')]:
            for kind in [socket.SOCK_STREAM, socket.SOCK_DGRAM]:
                code = '''import json,socket,sys
family,kind=int(sys.argv[1]),int(sys.argv[2]);s=socket.socket(family,kind);s.settimeout(4)
s.bind((sys.argv[3],28889))
if kind==socket.SOCK_STREAM:s.listen(1)
print('ready',flush=True)
if kind==socket.SOCK_STREAM:
 c,p=s.accept();c.settimeout(4);data=c.recv(100);c.sendall(data);c.close()
else:
 data,p=s.recvfrom(100);s.sendto(data,p)
assert data==b'external-ingress-reply',data
print(json.dumps({'observed_source':p[:2],'received_bytes':len(data)}),flush=True)
s.close()
'''
                backend = subprocess.Popen(['setpriv', '--reuid=65534', '--regid=65534', '--clear-groups', sys.executable, '-c', code, str(family), str(kind), local], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                try:
                    assert backend.stdout.readline().strip() == 'ready'
                    client_code = '''import json,socket,sys
s=socket.socket(int(sys.argv[1]),int(sys.argv[2]));s.settimeout(4);s.bind((sys.argv[3],0))
s.connect((sys.argv[4],28889));s.sendall(b'external-ingress-reply');data=s.recv(100)
assert data==b'external-ingress-reply',data
print(json.dumps({'client_source':s.getsockname()[:2],'reply_peer':s.getpeername()[:2],'reply_bytes':len(data)}))
s.close()
'''
                    client = subprocess.run([*prefix, sys.executable, '-c', client_code, str(family), str(kind), remote, local], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=6)
                    out, err = backend.communicate(timeout=5)
                    entry = {'family': 'IPv6' if family == socket.AF_INET6 else 'IPv4', 'transport': 'tcp' if kind == socket.SOCK_STREAM else 'udp', 'external_veth': True, 'client_exit': client.returncode, 'backend_exit': backend.returncode, 'client_error': client.stderr.strip(), 'backend_error': err.strip()}
                    if out.strip():
                        entry.update(json.loads(out))
                    if client.stdout.strip():
                        entry.update(json.loads(client.stdout))
                    report['results'].append(entry)
                    assert client.returncode == 0 and backend.returncode == 0, entry
                    assert entry['observed_source'] == entry['client_source'], entry
                    assert entry['observed_source'][0] == remote and entry['reply_peer'] == [local, 28889], entry
                finally:
                    if backend.poll() is None:
                        backend.kill()
                    backend.communicate(timeout=3)
    finally:
        if peer.poll() is None:
            peer.kill()
        peer.communicate(timeout=3)

if len(sys.argv) == 1:
    ns = os.readlink('/proc/self/ns/net')
    p = subprocess.Popen(['unshare', '--mount-proc', '--net', '--pid', '--fork', '--kill-child=SIGKILL', sys.executable, os.path.abspath(__file__), ns], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    try:
        out, err = p.communicate(timeout=45)
    except subprocess.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL)
        out, err = p.communicate()
        print(out, end='')
        print(err, end='', file=sys.stderr)
        raise SystemExit('isolated TPROXY probe timed out; owned process group killed')
    assert os.readlink('/proc/self/ns/net') == ns
    print(out, end='')
    print(err, end='', file=sys.stderr)
    sys.exit(p.returncode)
assert len(sys.argv) == 2 and os.getpid() == 1, 'use the launcher; refuse non-isolated PID'
assert open('/proc/self/stat').read().split()[0] == '1', 'refuse mismatched procfs PID namespace'
assert os.readlink('/proc/self/ns/net') != sys.argv[1], 'refuse host network namespace'
report = {'kernel': os.uname().release, 'machine': os.uname().machine, 'isolated_netns': True, 'versions': {}, 'results': []}
for tool in ['nft', 'iptables', 'iptables-nft', 'iptables-legacy']:
    try:
        report['versions'][tool] = run(tool, '--version')
    except Exception as e:
        report['versions'][tool] = str(e)
run('ip', 'link', 'set', 'lo', 'up')
run('ip', 'addr', 'add', '192.0.2.1/32', 'dev', 'lo')
run('ip', '-6', 'addr', 'add', '2001:db8::1/128', 'dev', 'lo', 'nodad')
for family, source, local in [('-4', '192.0.2.1', '0.0.0.0/0'), ('-6', '2001:db8::1', '::/0')]:
    run('ip', family, 'route', 'add', 'default', 'dev', 'lo', 'src', source)
    run('ip', family, 'rule', 'add', 'priority', '100', 'fwmark', '0x3f01', 'lookup', '100')
    run('ip', family, 'route', 'add', 'local', local, 'dev', 'lo', 'table', '100')
rules = '''table inet xui_prereq {
 chain mark_output {
  type route hook output priority mangle; policy accept;
  ct direction reply counter return
  meta skuid 65534 meta l4proto { tcp, udp } meta mark set 0x3f01 counter
 }
 chain transparent_input {
  type filter hook prerouting priority mangle; policy accept;
  meta mark 0x3f01 meta l4proto tcp tproxy to :28080 counter accept
  meta mark 0x3f01 meta l4proto udp tproxy to :28081 counter accept
 }
}'''
try:
    run('nft', '-f', '-', data='''table inet unrelated_control {
 chain existing {}
}
''')
    run('nft', '--check', '-f', '-', data=rules)
    run('nft', '-f', '-', data=rules)
    for family, source, target in [(socket.AF_INET, '192.0.2.1', '198.51.100.9'), (socket.AF_INET6, '2001:db8::1', '2001:db8:1::9')]:
        ipv6 = family == socket.AF_INET6
        for kind in [socket.SOCK_STREAM, socket.SOCK_DGRAM]:
            protocol = 'tcp' if kind == socket.SOCK_STREAM else 'udp'
            level = socket.IPPROTO_IPV6 if ipv6 else socket.SOL_IP
            transparent = 75 if ipv6 else 19
            original = 74 if ipv6 else 20
            listener = socket.socket(family, kind)
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            if ipv6:
                listener.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
            listener.setsockopt(level, transparent, 1)
            if protocol == 'udp':
                listener.setsockopt(level, original, 1)
            listener.bind(('::' if ipv6 else '0.0.0.0', 28080 if protocol == 'tcp' else 28081))
            listener.settimeout(4)
            if protocol == 'tcp':
                listener.listen(4)
            payload = (protocol + ('-v6' if ipv6 else '-v4') + '-original-destination').encode()
            observed = {}

            def serve():
                try:
                    if protocol == 'tcp':
                        c, peer = listener.accept()
                        c.settimeout(4)
                        with c:
                            dst = c.getsockname()
                            data = c.recv(4096)
                            c.sendall(data)
                    else:
                        data, anc, flags, peer = listener.recvmsg(4096, 1024)
                        addr = next((value for lev, typ, value in anc if lev == level and typ == original))
                        port = struct.unpack('!H', addr[2:4])[0]
                        dst = (socket.inet_ntop(family, addr[8:24] if ipv6 else addr[4:8]), port)
                        with socket.socket(family, socket.SOCK_DGRAM) as reply:
                            reply.setsockopt(level, transparent, 1)
                            reply.bind(dst)
                            reply.sendto(data, peer)
                    observed.update(destination=dst[:2], source=peer[:2], bytes=len(data))
                    assert dst[:2] == (target, 28888), (dst, target)
                    assert data == payload
                except Exception as e:
                    observed['error'] = repr(e)
            worker = threading.Thread(target=serve, daemon=True)
            worker.start()
            code = '''import socket,sys
family=int(sys.argv[1]);kind=int(sys.argv[2]);source,target=sys.argv[3:5];payload=sys.argv[5].encode()
s=socket.socket(family,kind);s.settimeout(4);s.bind((source,0));s.connect((target,28888));s.sendall(payload)
reply=s.recv(4096);assert reply==payload,(reply,payload);print('echo-ok');s.close()
'''
            client = subprocess.run(['setpriv', '--reuid=65534', '--regid=65534', '--clear-groups', sys.executable, '-c', code, str(family), str(kind), source, target, payload.decode()], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=6)
            worker.join(5)
            listener.close()
            entry = {'family': 'IPv6' if ipv6 else 'IPv4', 'transport': protocol, **observed, 'client_exit': client.returncode, 'client_output': client.stdout.strip(), 'client_error': client.stderr.strip()}
            report['results'].append(entry)
            assert not worker.is_alive() and (not observed.get('error')) and (client.returncode == 0), entry
    for family, source in [(socket.AF_INET, '192.0.2.1'), (socket.AF_INET6, '2001:db8::1')]:
        for kind in [socket.SOCK_STREAM, socket.SOCK_DGRAM]:
            code = '''import socket,sys
s=socket.socket(int(sys.argv[1]),int(sys.argv[2]));s.settimeout(4);s.bind((sys.argv[3],28889))
if int(sys.argv[2])==socket.SOCK_STREAM:s.listen(1)
print('ready',flush=True)
if int(sys.argv[2])==socket.SOCK_STREAM:
 c,p=s.accept();data=c.recv(100);c.sendall(data);c.close()
else:
 data,p=s.recvfrom(100);s.sendto(data,p)
s.close()
'''
            backend = subprocess.Popen(['setpriv', '--reuid=65534', '--regid=65534', '--clear-groups', sys.executable, '-c', code, str(family), str(kind), source], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                assert backend.stdout.readline().strip() == 'ready'
                with socket.socket(family, kind) as client:
                    client.settimeout(2)
                    client.connect((source, 28889))
                    client.sendall(b'native-reply-control')
                    assert client.recv(100) == b'native-reply-control'
                assert backend.wait(timeout=3) == 0
                report['results'].append({'family': 'IPv6' if family == socket.AF_INET6 else 'IPv4', 'transport': 'tcp' if kind == socket.SOCK_STREAM else 'udp', 'ingress_reply': 'preserved'})
            finally:
                if backend.poll() is None:
                    backend.kill()
                backend.communicate(timeout=3)
    external_ingress(report)
    report['rules'] = run('nft', 'list', 'table', 'inet', 'xui_prereq')
    invalid = '''add chain inet xui_prereq staged
add rule inet xui_prereq absent counter
'''
    rejected = subprocess.run(['nft', '-f', '-'], input=invalid, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
    assert rejected.returncode != 0, 'invalid batch was accepted'
    absent = subprocess.run(['nft', 'list', 'chain', 'inet', 'xui_prereq', 'staged'], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
    assert absent.returncode != 0, 'failed atomic batch retained its earlier chain'
    report['invalid_batch_atomic'] = True
    run('nft', 'delete', 'table', 'inet', 'xui_prereq')
    tables = json.loads(run('nft', '--json', 'list', 'tables'))['nftables']
    names = {item['table']['name'] for item in tables if 'table' in item}
    assert names == {'unrelated_control'}, names
    report['owned_cleanup_preserved_control'] = True
    report['success'] = True
except Exception as e:
    report['error'] = str(e)
    report['success'] = False
finally:
    try:
        run('nft', 'delete', 'table', 'inet', 'xui_prereq')
    except Exception:
        pass
    print(json.dumps(report, indent=2))
sys.exit(0 if report.get('success') else 1)
