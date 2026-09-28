import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { once } from 'node:events';
import {
  mkdirSync,
  mkdtempSync,
  openSync,
  closeSync,
  readFileSync,
  writeFileSync,
  symlinkSync,
  rmSync,
} from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const panelBinary = process.env.XUI_E2E_PANEL;
const xrayBinary = process.env.XRAY_E2E_BINARY;
assert(panelBinary && xrayBinary, 'Set XUI_E2E_PANEL and XRAY_E2E_BINARY to built binaries');
assert.equal(process.platform, 'linux', 'This isolated OpenSSH fixture requires Linux');
const root = path.resolve(fileURLToPath(new URL('../..', import.meta.url)));
const temp = mkdtempSync(path.join(tmpdir(), 'xui-policy-ui-'));
const env = {
  ...process.env,
  XUI_DB_TYPE: 'sqlite',
  XUI_DB_DSN: '',
  XUI_DB_FOLDER: path.join(temp, 'db'),
  XUI_BIN_FOLDER: path.join(temp, 'bin'),
  XUI_LOG_FOLDER: path.join(temp, 'log'),
  XUI_DEBUG: 'false',
};
const processes = [];
let browser;
let target;
let panelLog;

async function port() {
  const server = net.createServer();
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const value = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return value;
}
async function until(check, label, milliseconds = 15000) {
  const deadline = Date.now() + milliseconds;
  do {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 100));
  } while (Date.now() < deadline);
  throw new Error(`Timed out: ${label}`);
}
async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, 'exit');
  child.kill('SIGTERM');
  const force = setTimeout(() => child.kill('SIGKILL'), 5000);
  await exited;
  clearTimeout(force);
}

try {
  mkdirSync(env.XUI_DB_FOLDER);
  mkdirSync(env.XUI_BIN_FOLDER);
  const panelPort = await port();
  env.XUI_PORT = String(panelPort);
  const sshPort = await port();
  const forwardPort = await port();
  const apiPort = await port();
  const password = randomUUID();
  execFileSync(
    panelBinary,
    [
      'setting',
      '-username',
      'policy-ui',
      '-password',
      password,
      '-port',
      String(panelPort),
      '-listenIP',
      '127.0.0.1',
      '-webBasePath',
      '/',
    ],
    { env, stdio: 'ignore' },
  );
  const template = JSON.parse(
    readFileSync(path.join(root, 'internal/web/service/config.json'), 'utf8'),
  );
  template.inbounds[0].port = apiPort;
  template.outbounds[0].settings = {};
  template.routing.rules = [template.routing.rules[0]];
  delete template.metrics;
  const settings = {
    subEnable: 'false',
    subJsonEnable: 'false',
    subClashEnable: 'false',
    xrayTemplateConfig: JSON.stringify(template),
  };
  execFileSync(
    'python3',
    [
      '-c',
      'import json,sqlite3,sys\nc=sqlite3.connect(sys.argv[1])\nfor k,v in json.load(sys.stdin).items():\n c.execute("delete from settings where key=?",(k,))\n c.execute("insert into settings(key,value) values (?,?)",(k,v))\nc.commit()',
      path.join(env.XUI_DB_FOLDER, 'x-ui.db'),
    ],
    { input: JSON.stringify(settings) },
  );
  symlinkSync(
    path.resolve(xrayBinary),
    path.join(env.XUI_BIN_FOLDER, `xray-linux-${process.arch === 'x64' ? 'amd64' : process.arch}`),
  );
  panelLog = openSync(path.join(temp, 'panel.log'), 'w', 0o600);
  const panel = spawn(panelBinary, [], { env, cwd: temp, stdio: ['ignore', panelLog, panelLog] });
  processes.push(panel);
  const origin = `http://127.0.0.1:${panelPort}`;
  await until(async () => {
    try {
      return (await fetch(origin, { signal: AbortSignal.timeout(1000) })).ok;
    } catch {
      return false;
    }
  }, 'panel startup');
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await page.goto(origin);
  await page.getByPlaceholder('Username', { exact: true }).fill('policy-ui');
  await page.getByPlaceholder('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Log In', exact: true }).click();
  await page.waitForURL('**/panel/**');
  async function api(route, data) {
    let response;
    if (data === undefined) response = await page.request.get(origin + route);
    else {
      const csrf = await (await page.request.get(origin + '/csrf-token')).json();
      response = await page.request.post(origin + route, {
        data,
        headers: { 'X-CSRF-Token': csrf.obj },
      });
    }
    assert(response.ok(), `${route}: HTTP ${response.status()}`);
    const result = await response.json();
    assert.equal(result.success, true, `${route}: ${result.msg}`);
    return result.obj;
  }
  const key = path.join(temp, 'client-key');
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', key]);
  target = net.createServer((socket) => socket.pipe(socket));
  target.listen(0, '127.0.0.1');
  await once(target, 'listening');
  const targetPort = target.address().port;
  const email = 'policy-browser';
  const inbound = await api('/panel/api/inbounds/add', {
    enable: true,
    listen: '127.0.0.1',
    port: sshPort,
    protocol: 'ssh',
    remark: 'isolated policy browser test',
    settings: JSON.stringify({
      clients: [
        {
          email,
          enable: true,
          totalGB: 104857600,
          ssh: {
            publicKeys: [readFileSync(key + '.pub', 'utf8')],
            targets: [{ host: '127.0.0.1', port: targetPort }],
          },
        },
      ],
    }),
    streamSettings: '{}',
    sniffing: '{}',
  });
  await api('/panel/api/server/restartXrayService', {});
  await page.goto(origin + '/panel/clients');
  await page.getByText(email, { exact: true }).first().waitFor();
  await page.getByRole('button', { name: 'Edit', exact: true }).click();
  await page.getByRole('tab', { name: 'Traffic policy' }).click();
  await page.getByLabel('Upload limit (B/s)', { exact: true }).fill('32768');
  await page.getByLabel('Download limit (B/s)', { exact: true }).fill('65536');
  await page.getByLabel('Billing multiplier', { exact: true }).fill('1.5');
  await page.getByRole('button', { name: 'Apply traffic policy', exact: true }).click();
  await page.getByText('Traffic policy applied.', { exact: true }).waitFor();
  const saved = await api(`/panel/api/clients/policy/${email}`);
  assert.equal(saved.uploadBps, 32768);
  assert.equal(saved.downloadBps, 65536);
  assert.equal(saved.multiplier, '1.5');
  assert.equal(saved.version, 1);

  const meterWaitStarted = Date.now();
  let initiallyReady;
  await until(
    () => {
      const ready = execFileSync(
        'python3',
        [
          '-c',
          'import sqlite3,sys\nc=sqlite3.connect(sys.argv[1])\nprint(c.execute("select count(*) from client_usage_meters m join client_usage_accounts a on a.policy_id=m.policy_id where m.policy_id=? and m.revision=a.revision and m.closed=0 and m.admission_only=1",(sys.argv[2],)).fetchone()[0])',
          path.join(env.XUI_DB_FOLDER, 'x-ui.db'),
          saved.policyId,
        ],
        { encoding: 'utf8' },
      );
      const available = ready.trim() === '1';
      initiallyReady ??= available;
      return available;
    },
    'fresh meter after multiplier boundary',
    2000,
  );
  const meterWaitMs = Date.now() - meterWaitStarted;
  assert(meterWaitMs <= 2000, `Meter replacement took ${meterWaitMs} ms`);

  const hostFile = path.join(temp, 'host-key');
  writeFileSync(hostFile, inbound.settings.hostKey, { mode: 0o600 });
  const publicHost = execFileSync('ssh-keygen', ['-y', '-f', hostFile], {
    encoding: 'utf8',
  }).trim();
  const knownHosts = path.join(temp, 'known_hosts');
  writeFileSync(knownHosts, `[127.0.0.1]:${sshPort} ${publicHost}\n`, { mode: 0o600 });
  const sshIdentityArgs = [
    '-F',
    '/dev/null',
    '-o',
    'BatchMode=yes',
    '-o',
    'IdentitiesOnly=yes',
    '-o',
    'StrictHostKeyChecking=yes',
    '-o',
    `UserKnownHostsFile=${knownHosts}`,
    '-o',
    'ExitOnForwardFailure=yes',
    '-o',
    'ConnectTimeout=3',
    '-i',
    key,
    '-p',
    String(sshPort),
  ];
  const ssh = spawn(
    'ssh',
    [
      ...sshIdentityArgs,
      '-N',
      '-L',
      `127.0.0.1:${forwardPort}:127.0.0.1:${targetPort}`,
      `${email}@127.0.0.1`,
    ],
    { stdio: ['ignore', 'ignore', 'pipe'] },
  );
  processes.push(ssh);
  let sshError = '';
  ssh.stderr.on('data', (chunk) => {
    sshError += chunk;
  });
  await until(() => {
    if (ssh.exitCode !== null || ssh.signalCode !== null)
      throw new Error(`OpenSSH exited: ${sshError}`);
    return new Promise((resolve) => {
      const socket = net.connect(forwardPort, '127.0.0.1');
      socket.once('connect', () => {
        socket.destroy();
        resolve(true);
      });
      socket.once('error', () => resolve(false));
    });
  }, 'OpenSSH forward');
  const payload = Buffer.alloc(16384, 0x37);
  const echoed = await new Promise((resolve, reject) => {
    const socket = net.connect(forwardPort, '127.0.0.1');
    const chunks = [];
    let received = 0;
    socket.setTimeout(10000, () => socket.destroy(new Error('SSH payload timeout')));
    socket.once('connect', () => socket.write(payload));
    socket.on('data', (chunk) => {
      chunks.push(chunk);
      received += chunk.length;
      if (received >= payload.length) {
        socket.destroy();
        resolve(Buffer.concat(chunks));
      }
    });
    socket.on('error', reject);
    socket.once('close', () => {
      if (received < payload.length) reject(new Error(`SSH echo closed after ${received} bytes`));
    });
  });
  assert.deepEqual(echoed, payload);
  await until(async () => {
    const current = await api(`/panel/api/clients/policy/${email}`);
    return (
      current.usage.up === '16384' &&
      current.usage.down === '16384' &&
      current.usage.billed === '49152'
    );
  }, 'real SSH billing');
  await page.getByText('49152 B', { exact: true }).waitFor({ timeout: 10000 });

  const listed = await api('/panel/api/clients/list/paged');
  const row = listed.items.find((item) => item.email === email);
  assert.equal(row.billing.billed, '49152');
  assert.equal(row.billing.remaining, '104808448');
  assert.equal(row.billing.exhausted, false);
  const hydrated = await api(`/panel/api/clients/get/${email}`);
  assert.equal(hydrated.billing.billed, '49152');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await page
    .getByRole('progressbar', { name: 'Billed usage: 49152 B / 104857600 B', exact: true })
    .waitFor({ timeout: 10000 });
  await page.getByRole('cell', { name: '99.95 MiB', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Edit', exact: true }).click();
  await page.getByRole('tab', { name: 'Traffic policy' }).click();

  await page.getByLabel('Upload limit (B/s)', { exact: true }).fill('12345');
  await api(`/panel/api/clients/policy/${email}`, { ...saved, downloadBps: 131072 });
  await page
    .getByText(
      'Reload the saved policy before applying again. Reload replaces your unsaved policy edits.',
      { exact: true },
    )
    .waitFor({ timeout: 10000 });
  assert.equal(await page.getByLabel('Upload limit (B/s)', { exact: true }).inputValue(), '12345');
  assert.equal(
    await page.getByRole('button', { name: 'Apply traffic policy', exact: true }).isDisabled(),
    true,
  );
  await page.getByRole('button', { name: 'Reload saved policy' }).click();
  await until(
    async () =>
      (await page.getByLabel('Download limit (B/s)', { exact: true }).inputValue()) === '131072',
    'saved policy reload',
  );
  assert.equal(await page.getByLabel('Upload limit (B/s)', { exact: true }).inputValue(), '32768');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await api(`/panel/api/clients/update/${email}`, {
    email,
    id: hydrated.client.uuid || '',
    subId: hydrated.client.subId,
    enable: true,
    totalGB: 32769,
  });
  const depleted = await api('/panel/api/clients/list/paged?filter=depleted');
  assert.equal(depleted.filtered, 1);
  assert.equal(depleted.summary.depletedCount, 1);
  assert.equal(depleted.items[0].billing.exhausted, true);
  assert.equal(depleted.items[0].billing.remaining, '0');
  assert.equal(depleted.items[0].billing.billed, '49152');
  await page
    .getByRole('progressbar', { name: 'Billed usage: 49152 B / 32769 B', exact: true })
    .waitFor({ timeout: 10000 });
  await page.getByRole('cell', { name: '0 B', exact: true }).waitFor();
  let denial;
  try {
    execFileSync(
      'ssh',
      [...sshIdentityArgs, '-W', `127.0.0.1:${targetPort}`, `${email}@127.0.0.1`],
      {
        input: 'quota-probe',
        encoding: 'utf8',
        timeout: 5000,
        stdio: ['pipe', 'pipe', 'pipe'],
      },
    );
  } catch (error) {
    denial = error;
  }
  assert(
    denial && denial.status !== null && denial.status !== 0,
    'Exhausted SSH client must exit with refusal, not a test timeout',
  );
  assert.equal(denial.stdout, '');
  assert.match(
    denial.stderr,
    /Permission denied|administratively prohibited|Connection .* closed by remote host/,
  );
  assert.deepEqual(pageErrors, []);
  if (process.env.XUI_E2E_SCREENSHOT)
    await page.screenshot({ path: process.env.XUI_E2E_SCREENSHOT });
  console.log(
    `PASS: real browser -> authenticated panel API -> SQLite -> managed SSH -> pinned Xray -> loopback echo; 16384 B each direction, 49152 B billed at 1.5x; stale editor preserved and explicitly reloaded; list balance uses billed bytes; quota reduced below billed usage marks depleted and rejects real SSH. Meter initially ready: ${initiallyReady}; readiness wait: ${meterWaitMs} ms.`,
  );
} catch (error) {
  const diagnostics = (
    panelLog === undefined ? '' : readFileSync(path.join(temp, 'panel.log'), 'utf8')
  )
    .split('\n')
    .filter((line) =>
      /managed SSH inbound \d+ protected:|Xray \d+|XRAY:.*(started|exited)/.test(line),
    );
  if (diagnostics.length) console.error(diagnostics.join('\n'));
  throw error;
} finally {
  if (browser) await browser.close();
  for (const child of processes.reverse()) await stop(child);
  if (target) await new Promise((resolve) => target.close(resolve));
  if (panelLog !== undefined) closeSync(panelLog);
  rmSync(temp, { recursive: true, force: true });
}
