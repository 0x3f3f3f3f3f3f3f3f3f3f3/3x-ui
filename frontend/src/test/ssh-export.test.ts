import { afterEach, describe, it, expect } from 'vitest';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { buildSSHClientExport } from '@/pages/clients/sshConfig';

const hostKey = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMlM7FlZhqPVGX/CZdQJcMWTCfcRWAXXrgNKWBGPVH4L';
const inbound = {
  id: 17,
  protocol: 'ssh',
  port: 2222,
  sshHostKey: hostKey,
  shareAddrStrategy: 'custom',
  shareAddr: '2001:db8::17',
};
const paths: string[] = [];
afterEach(() => paths.splice(0).forEach((path) => rmSync(path, { recursive: true, force: true })));

describe('OpenSSH export', () => {
  it('parses in OpenSSH with an IPv6 endpoint, isolated host pin and no default identity', () => {
    const bundle = buildSSHClientExport({ email: 'alice@example.com' }, inbound, 'panel.example');
    expect(bundle).not.toBeNull();
    if (!bundle) throw new Error('export missing');
    const directory = mkdtempSync(join(tmpdir(), 'xui-ssh-export-'));
    paths.push(directory);
    writeFileSync(join(directory, bundle.configName), bundle.config);
    writeFileSync(join(directory, bundle.knownHostsName), bundle.knownHosts);
    const parsed = execFileSync('ssh', ['-G', '-F', bundle.configName, bundle.alias], {
      cwd: directory,
      encoding: 'utf8',
    });
    expect(parsed).toContain('hostname 2001:db8::17\n');
    expect(parsed).toContain('port 2222\n');
    expect(parsed).toContain('user alice@example.com\n');
    expect(parsed).toContain('stricthostkeychecking true\n');
    expect(parsed).toContain('globalknownhostsfile none\n');
    expect(parsed).toContain('identityfile none\n');
    expect(parsed).toContain('identitiesonly yes\n');
    expect(parsed).toContain('sessiontype none\n');
    expect(parsed).toContain('forwardagent no\n');
    expect(bundle.knownHosts).toBe(`[2001:db8::17]:2222 ${hostKey}\n`);
  });

  it('refuses missing pins and config directives hidden in endpoint or username fields', () => {
    expect(
      buildSSHClientExport({ email: 'alice' }, { ...inbound, sshHostKey: '' }, 'panel.example'),
    ).toBeNull();
    expect(
      buildSSHClientExport(
        { email: 'alice\nProxyCommand touch /tmp/unsafe' },
        inbound,
        'panel.example',
      ),
    ).toBeNull();
    expect(
      buildSSHClientExport(
        { email: 'alice' },
        { ...inbound, shareAddr: 'evil%h' },
        'panel.example',
      ),
    ).toBeNull();
    expect(
      buildSSHClientExport(
        { email: 'alice' },
        { ...inbound, sshHostKey: hostKey + '\n@cert-authority * other' },
        'panel.example',
      ),
    ).toBeNull();
  });
});
