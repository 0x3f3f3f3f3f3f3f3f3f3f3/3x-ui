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
  XUI_ENABLE_FAIL2BAN: 'true',
};
const processes = [];
let browser;
let page;
let lastListing;
let target;
let collision;
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
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.on('response', async (response) => {
    if (!response.url().includes('/panel/api/clients/list/paged')) return;
    try {
      const body = await response.json();
      lastListing = {
        status: response.status(),
        success: body.success,
        billing: body.obj?.items?.map((item) => item.billing),
      };
    } catch {}
  });
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
  await page.goto(origin + '/panel/inbounds');
  await page.getByRole('button', { name: 'Add Inbound', exact: true }).click();
  const inboundDialog = page.getByRole('dialog');
  await inboundDialog.locator('#protocol').click();
  const sshOption = page.locator('.ant-select-item-option[title="ssh"]');
  for (let attempt = 0; attempt < 20 && !(await sshOption.count()); attempt++)
    await inboundDialog.locator('#protocol').press('ArrowDown');
  assert.equal(await sshOption.count(), 1, 'SSH must be selectable in the actual inbound form');
  await sshOption.click();
  await inboundDialog.getByLabel('Address', { exact: true }).fill('127.0.0.1');
  await inboundDialog.getByLabel('Port', { exact: true }).fill(String(sshPort));
  await inboundDialog.getByLabel('Remark', { exact: true }).fill('isolated policy browser test');
  const inboundResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith('/panel/api/inbounds/add') && response.request().method() === 'POST',
  );
  await inboundDialog.getByRole('button', { name: 'Create', exact: true }).click();
  const createdInbound = await (await inboundResponse).json();
  assert.equal(createdInbound.success, true, createdInbound.msg);
  const inbound = createdInbound.obj;
  await inboundDialog.waitFor({ state: 'hidden' });
  await page.getByRole('status', { name: 'SSH runtime: Idle', exact: true }).waitFor();
  collision = net.createServer((socket) => socket.destroy());
  collision.listen(sshPort, '127.0.0.1');
  await once(collision, 'listening');
  await page.goto(origin + '/panel/clients');
  await page.getByRole('button', { name: 'Add Clients', exact: true }).click();
  const clientDialog = page.getByRole('dialog');
  await clientDialog.getByPlaceholder('Email', { exact: true }).fill(email);
  await clientDialog.getByLabel('Traffic Limit (GB)', { exact: true }).fill('0.09765625');
  await clientDialog.getByRole('button', { name: 'Select all', exact: true }).click();
  await clientDialog.getByRole('tab', { name: 'Credentials', exact: true }).click();
  await clientDialog
    .getByLabel('SSH public keys', { exact: true })
    .fill(readFileSync(key + '.pub', 'utf8').trim());
  await clientDialog.getByRole('button', { name: /Add target/ }).click();
  await clientDialog.getByLabel('Target host', { exact: true }).fill('127.0.0.1');
  await clientDialog.getByLabel('Target port', { exact: true }).fill(String(targetPort));
  await clientDialog.getByRole('button', { name: 'Create', exact: true }).click();
  await clientDialog.waitFor({ state: 'hidden' });
  await page.getByText(email, { exact: true }).first().waitFor();
  const startupStarted = Date.now();
  await page.goto(origin + '/panel/inbounds');
  const protectedBadge = page.getByRole('status', { name: 'SSH runtime: Protected', exact: true });
  await protectedBadge.waitFor({ timeout: 35000 });
  assert.equal(await page.getByRole('switch').getAttribute('aria-checked'), 'true');
  await protectedBadge.hover();
  await page
    .getByText('SSH listener unavailable. Check the listening address and port.', { exact: true })
    .waitFor();
  await new Promise((resolve) => collision.close(resolve));
  collision = undefined;
  const runningBadge = page.getByRole('status', { name: 'SSH runtime: Running', exact: true });
  await until(
    async () =>
      (await runningBadge.textContent({ timeout: 1000 }).catch(() => '')) === 'Running · 0',
    'listener recovers after owned port conflict',
  );
  await page.goto(origin + '/panel/clients');
  await until(
    () =>
      new Promise((resolve) => {
        const socket = net.connect(sshPort, '127.0.0.1');
        socket.setTimeout(1000, () => {
          socket.destroy();
          resolve(false);
        });
        socket.once('error', () => resolve(false));
        socket.once('data', (data) => {
          socket.destroy();
          resolve(data.toString().startsWith('SSH-'));
        });
      }),
    'initial scheduled SSH configuration application',
    35000,
  );
  const startupWaitMs = Date.now() - startupStarted;
  await page.getByRole('button', { name: 'Client Information', exact: true }).click();
  const infoDialog = page.getByRole('dialog');
  const alias = `xui-ssh-${inbound.id}`;
  const configName = `${alias}.conf`;
  const knownHostsName = `${alias}.known_hosts`;
  for (const fileName of [configName, knownHostsName]) {
    const exportPanel = infoDialog
      .locator('.qr-panel')
      .filter({ has: page.getByText(fileName, { exact: true }) });
    const download = page.waitForEvent('download');
    await exportPanel.getByRole('button', { name: 'Download', exact: true }).click();
    const artifact = await download;
    assert.equal(artifact.suggestedFilename(), fileName);
    await artifact.saveAs(path.join(temp, fileName));
  }
  const knownHosts = path.join(temp, knownHostsName);
  const exportedPin = readFileSync(knownHosts, 'utf8');
  const options = await api('/panel/api/inbounds/options');
  const actualHostKey = options.find((row) => row.id === inbound.id)?.sshHostKey;
  assert.equal(exportedPin, `[127.0.0.1]:${sshPort} ${actualHostKey}\n`);
  assert(!readFileSync(path.join(temp, configName), 'utf8').includes('PRIVATE KEY'));
  await infoDialog.getByRole('button', { name: 'Close', exact: true }).click();
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

  const sshIdentityArgs = [
    '-F',
    path.join(temp, configName),
    '-i',
    key,
    '-o',
    'BatchMode=yes',
    '-o',
    'ConnectTimeout=3',
  ];
  const wrongKey = path.join(temp, 'wrong-host');
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', wrongKey]);
  writeFileSync(knownHosts, `[127.0.0.1]:${sshPort} ${readFileSync(wrongKey + '.pub', 'utf8')}`);
  let wrongHost;
  try {
    execFileSync('ssh', [...sshIdentityArgs, '-W', `127.0.0.1:${targetPort}`, alias], {
      cwd: temp,
      input: 'must-not-arrive',
      encoding: 'utf8',
      timeout: 5000,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
  } catch (error) {
    wrongHost = error;
  } finally {
    writeFileSync(knownHosts, exportedPin);
  }
  assert(
    wrongHost && wrongHost.status !== null && wrongHost.status !== 0,
    'Mismatched host key must fail',
  );
  assert.equal(wrongHost.stdout, '');
  assert.match(wrongHost.stderr, /Host key verification failed/);
  const ssh = spawn(
    'ssh',
    [...sshIdentityArgs, '-N', '-L', `127.0.0.1:${forwardPort}:127.0.0.1:${targetPort}`, alias],
    { cwd: temp, stdio: ['ignore', 'ignore', 'pipe'] },
  );
  processes.push(ssh);
  let sshError = '';
  ssh.stderr.on('data', (chunk) => {
    sshError += chunk;
  });
  await until(async () => {
    if (ssh.exitCode !== null || ssh.signalCode !== null)
      throw new Error(`OpenSSH exited: ${sshError}`);
    const statuses = await api('/panel/api/inbounds/ssh/status');
    return statuses.some(
      (status) => status.inboundId === inbound.id && status.authenticatedConnections === 1,
    );
  }, 'OpenSSH authentication before opening any forwarding channel');
  await until(async () => {
    const online = await api('/panel/api/clients/onlines', {});
    return (online ?? []).includes(email);
  }, 'idle SSH client in the existing online list');
  const idleUsage = await api(`/panel/api/clients/policy/${email}`);
  assert.equal(idleUsage.usage.up, '0');
  assert.equal(idleUsage.usage.down, '0');
  await until(async () => {
    const ips = await api(`/panel/api/clients/ips/${email}`, {});
    return ips.length === 1 && ips[0].ip === '127.0.0.1' && ips[0].node === '';
  }, 'actual idle SSH source IP in the existing client IP view');
  const lastOnline = await api('/panel/api/clients/lastOnline', {});
  assert(lastOnline[email] >= Date.now() - 15000, 'idle SSH last-online was not refreshed');
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
  await page.getByRole('cell', { name: 'Online', exact: true }).waitFor({ timeout: 10000 });
  await page
    .getByRole('progressbar', { name: 'Billed usage: 49152 B / 104857600 B', exact: true })
    .waitFor({ timeout: 10000 });
  await page.getByRole('cell', { name: '99.95 MiB', exact: true }).waitFor();
  await page.goto(origin + '/panel/inbounds');
  await until(
    async () =>
      (await runningBadge.textContent({ timeout: 1000 }).catch(() => '')) === 'Running · 1',
    'one real authenticated SSH connection',
  );
  const secondForwardPort = await port();
  const secondSSH = spawn(
    'ssh',
    [...sshIdentityArgs, '-N', '-D', `127.0.0.1:${secondForwardPort}`, alias],
    { cwd: temp, stdio: 'ignore' },
  );
  processes.push(secondSSH);
  await until(async () => {
    assert.equal(secondSSH.exitCode, null, 'second SSH connection exited before authentication');
    return (await runningBadge.textContent({ timeout: 1000 }).catch(() => '')) === 'Running · 2';
  }, 'two authenticated transports for the same SSH client');
  await stop(secondSSH);
  await until(
    async () =>
      (await runningBadge.textContent({ timeout: 1000 }).catch(() => '')) === 'Running · 1',
    'closed SSH transport disappears from status',
  );
  await page.context().setOffline(true);
  await page
    .getByRole('status', { name: 'SSH runtime: Unavailable', exact: true })
    .waitFor({ timeout: 10000 });
  assert.equal(await runningBadge.count(), 0, 'offline browser retained cached running status');
  await page.context().setOffline(false);
  await until(
    async () =>
      (await runningBadge.textContent({ timeout: 1000 }).catch(() => '')) === 'Running · 1',
    'fresh status after browser network recovery',
  );
  if (process.env.XUI_E2E_STATUS_SCREENSHOT)
    await page.screenshot({ path: process.env.XUI_E2E_STATUS_SCREENSHOT });
  await page.setViewportSize({ width: 390, height: 844 });
  await until(
    async () =>
      (await runningBadge.textContent({ timeout: 1000 }).catch(() => '')) === 'Running · 1',
    'mobile SSH runtime badge',
  );
  assert(
    await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    'mobile status badge overflows the viewport',
  );
  if (process.env.XUI_E2E_STATUS_SCREENSHOT)
    await page.screenshot({ path: process.env.XUI_E2E_STATUS_SCREENSHOT + '.mobile.png' });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(origin + '/panel/clients');
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
    execFileSync('ssh', [...sshIdentityArgs, '-W', `127.0.0.1:${targetPort}`, alias], {
      cwd: temp,
      input: 'quota-probe',
      encoding: 'utf8',
      timeout: 5000,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
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
  await page.goto(origin + '/panel/inbounds');
  await page.getByRole('switch').click();
  await page
    .getByRole('status', { name: 'SSH runtime: Disabled', exact: true })
    .waitFor({ timeout: 10000 });
  assert.equal(await page.getByRole('switch').getAttribute('aria-checked'), 'false');
  await until(
    async () => {
      const online = await api('/panel/api/clients/onlines', {});
      return !(online ?? []).includes(email);
    },
    'disconnected SSH client leaves online list after the existing grace window',
    30000,
  );
  assert.deepEqual(pageErrors, []);
  console.log(
    `PASS: browser-created SSH inbound and public-key client; idle SSH appears in existing online/IP/last-online APIs before payload, without billed traffic; actual Online UI and disconnect aging; idle/protected on owned port collision/recovery; actual authenticated SSH counts 1/2/1 in desktop/mobile list; offline browser clears cached running state and recovers after reconnection; UI disable clears runtime; downloaded OpenSSH config + actual host pin; mismatched host key refused; real browser -> authenticated panel API -> SQLite -> managed SSH -> pinned Xray -> loopback echo; 16384 B each direction, 49152 B billed at 1.5x; stale editor preserved and explicitly reloaded; list balance uses billed bytes; quota reduced below billed usage marks depleted and rejects real SSH. Initial scheduled application and collision recovery: ${startupWaitMs} ms. Meter initially ready: ${initiallyReady}; readiness wait: ${meterWaitMs} ms.`,
  );
} catch (error) {
  if (page && !page.isClosed()) {
    console.error(
      'UI billing diagnostics:',
      JSON.stringify({
        lastListing,
        labels: await page
          .locator('[role="progressbar"]')
          .evaluateAll((nodes) => nodes.map((node) => node.getAttribute('aria-label'))),
        visibility: await page.evaluate(() => document.visibilityState),
      }),
    );
    if (process.env.XUI_E2E_SCREENSHOT)
      await page.screenshot({ path: process.env.XUI_E2E_SCREENSHOT });
  }
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
  if (collision) await new Promise((resolve) => collision.close(resolve));
  if (target) await new Promise((resolve) => target.close(resolve));
  if (panelLog !== undefined) closeSync(panelLog);
  rmSync(temp, { recursive: true, force: true });
}
