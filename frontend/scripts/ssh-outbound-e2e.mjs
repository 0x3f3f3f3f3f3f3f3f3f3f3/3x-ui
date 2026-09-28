import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { once } from 'node:events';
import {
  closeSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import net from 'node:net';
import { homedir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const panelBinary = process.env.XUI_E2E_PANEL;
const xrayBinary = process.env.XRAY_E2E_BINARY;
const sshdBinary = process.env.SSH_E2E_SERVER;
assert(
  panelBinary && xrayBinary && sshdBinary,
  'Set XUI_E2E_PANEL, XRAY_E2E_BINARY and SSH_E2E_SERVER',
);
assert(
  process.platform === 'linux' && process.getuid() === 0,
  'The isolated OpenSSH fixture requires Linux/root',
);
const root = path.resolve(fileURLToPath(new URL('../..', import.meta.url)));
const temp = mkdtempSync(path.join(homedir(), '.3x-ui-ssh-outbound-ui-'));
const testURL = process.env.XUI_E2E_TEST_URL || 'https://example.com';
const env = {
  ...process.env,
  XUI_DB_TYPE: 'sqlite',
  XUI_DB_DSN: '',
  XUI_DB_FOLDER: path.join(temp, 'db'),
  XUI_BIN_FOLDER: path.join(temp, 'bin'),
  XUI_LOG_FOLDER: path.join(temp, 'logs'),
  XUI_DEBUG: 'false',
};
const processes = [];
const logs = [];
const secrets = [];
const targetSockets = new Set();
let phase = 'fixture startup';
let browser;
let page;
let target;
let targetConnections = 0;
let targetBytes = 0;

async function port() {
  const server = net.createServer();
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const value = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return value;
}

async function until(check, label, duration = 15000) {
  const deadline = Date.now() + duration;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`Timed out: ${label}`);
}

function start(binary, args, name) {
  const log = openSync(path.join(temp, `${name}.log`), 'w', 0o600);
  logs.push(log);
  const child = spawn(binary, args, {
    env,
    cwd: temp,
    detached: true,
    stdio: ['ignore', log, log],
  });
  processes.push(child);
  return child;
}

async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, 'exit');
  process.kill(-child.pid, 'SIGTERM');
  const force = setTimeout(() => {
    try {
      process.kill(-child.pid, 'SIGKILL');
    } catch {}
  }, 5000);
  await exited;
  clearTimeout(force);
}

async function screenshot() {
  if (!page || !process.env.XUI_E2E_SCREENSHOT) return;
  await page.addStyleTag({
    content:
      'textarea, input[type="password"] { color: transparent !important; -webkit-text-fill-color: transparent !important; text-shadow: none !important; }',
  });
  await page.evaluate(() =>
    Promise.all(
      document
        .getAnimations()
        .filter((animation) => animation.effect?.getTiming().iterations !== Infinity)
        .map((animation) => animation.finished.catch(() => {})),
    ),
  );
  await page.screenshot({
    path: process.env.XUI_E2E_SCREENSHOT,
    fullPage: true,
    animations: 'disabled',
    mask: [page.locator('textarea'), page.locator('input[type="password"]')],
  });
}

async function echoThroughSocks(proxyPort, targetPort) {
  const socket = net.connect(proxyPort, '127.0.0.1');
  socket.setTimeout(5000, () => socket.destroy(new Error('SOCKS echo timed out')));
  async function read(count) {
    while (true) {
      const bytes = socket.read(count);
      if (bytes) return bytes;
      if (socket.destroyed || socket.readableEnded) throw new Error('SOCKS stream closed');
      await once(socket, 'readable');
    }
  }
  try {
    await once(socket, 'connect');
    socket.write(Buffer.from([5, 1, 0]));
    assert((await read(2)).equals(Buffer.from([5, 0])), 'SOCKS greeting rejected');
    socket.write(Buffer.from([5, 1, 0, 1, 127, 0, 0, 1, targetPort >> 8, targetPort & 255]));
    assert((await read(10))[1] === 0, 'SOCKS CONNECT rejected');
    const payload = Buffer.from('browser-created-ssh-route:' + randomUUID());
    socket.write(payload);
    assert((await read(payload.length)).equals(payload), 'Actual target echo differs');
    return payload.length;
  } finally {
    socket.destroy();
  }
}

try {
  mkdirSync(env.XUI_DB_FOLDER);
  mkdirSync(env.XUI_BIN_FOLDER);
  const panelPort = await port();
  const sshPort = await port();
  const socksPort = await port();
  const apiPort = await port();
  env.XUI_PORT = String(panelPort);
  env.XUI_SSH_UPSTREAM_BRIDGE_PORT = String(await port());
  const password = randomUUID();
  secrets.push(password);
  execFileSync(
    panelBinary,
    [
      'setting',
      '-username',
      'ssh-outbound-ui',
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
  template.inbounds.push({
    tag: 'ui-socks',
    listen: '127.0.0.1',
    port: socksPort,
    protocol: 'socks',
    settings: { auth: 'noauth', udp: false },
  });
  template.outbounds[0].settings = {};
  template.routing.rules = [template.routing.rules[0]];
  delete template.metrics;
  const settings = {
    subEnable: 'false',
    subJsonEnable: 'false',
    subClashEnable: 'false',
    xrayTemplateConfig: JSON.stringify(template),
    xrayOutboundTestUrl: testURL,
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
  for (const name of ['host', 'client', 'wrong-host']) {
    execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(temp, name)]);
  }
  writeFileSync(path.join(temp, 'authorized_keys'), readFileSync(path.join(temp, 'client.pub')), {
    mode: 0o600,
  });
  const privateKey = readFileSync(path.join(temp, 'client'), 'utf8');
  secrets.push(privateKey, ...privateKey.trim().split('\n'));
  const hostKey = readFileSync(path.join(temp, 'host.pub'), 'utf8').trim();
  const wrongHost = readFileSync(path.join(temp, 'wrong-host.pub'), 'utf8').trim();
  const sshConfig = `Port ${sshPort}\nListenAddress 127.0.0.1\nHostKey ${path.join(temp, 'host')}\nAuthorizedKeysFile ${path.join(temp, 'authorized_keys')}\nPidFile ${path.join(temp, 'sshd.pid')}\nAllowUsers root\nPermitRootLogin prohibit-password\nAuthenticationMethods publickey\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM yes\nAllowTcpForwarding local\nAllowAgentForwarding no\nX11Forwarding no\nPermitTTY no\nMaxSessions 0\nLogLevel VERBOSE\n`;
  writeFileSync(path.join(temp, 'sshd_config'), sshConfig, { mode: 0o600 });
  start(sshdBinary, ['-D', '-e', '-f', path.join(temp, 'sshd_config')], 'sshd');
  const authenticated = () =>
    (readFileSync(path.join(temp, 'sshd.log'), 'utf8').match(/Accepted publickey for /g) || [])
      .length;
  target = net.createServer((socket) => {
    targetSockets.add(socket);
    socket.on('error', () => {});
    socket.on('close', () => targetSockets.delete(socket));
    targetConnections++;
    socket.on('data', (bytes) => {
      targetBytes += bytes.length;
      socket.write(bytes);
    });
  });
  target.listen(0, '127.0.0.1');
  await once(target, 'listening');
  const targetPort = target.address().port;
  start(panelBinary, [], 'panel');
  const origin = `http://127.0.0.1:${panelPort}`;
  await until(async () => {
    try {
      return (await fetch(origin, { signal: AbortSignal.timeout(1000) })).ok;
    } catch {
      return false;
    }
  }, 'panel startup');
  browser = await chromium.launch({ headless: true });
  phase = 'browser login';
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await page.goto(origin);
  await page.getByPlaceholder('Username', { exact: true }).fill('ssh-outbound-ui');
  await page.getByPlaceholder('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Log In', exact: true }).click();
  await page.waitForURL('**/panel/**');

  async function api(route, form = {}) {
    const csrf = await (await page.request.get(origin + '/csrf-token')).json();
    const response = await page.request.post(origin + route, {
      form,
      headers: { 'X-CSRF-Token': csrf.obj },
    });
    assert(response.ok(), `${route}: HTTP ${response.status()}`);
    const body = await response.json();
    assert(body.success, `${route}: ${body.msg}`);
    return body.obj;
  }
  async function save() {
    const response = page.waitForResponse(
      (r) => r.url().endsWith('/panel/api/xray/update') && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    const body = await (await response).json();
    assert(body.success, `Saving SSH outbound failed: ${body.msg}`);
  }
  const tag = 'browser-ssh-exit';
  const row = () => page.locator('tr').filter({ has: page.getByText(tag, { exact: true }) });
  async function editPin(pin) {
    await row().getByRole('button', { name: 'Edit', exact: true }).click();
    const dialog = page.getByRole('dialog');
    assert(
      (await dialog
        .getByRole('button', { name: 'Show / edit private key', exact: true })
        .count()) === 1,
      'Reopened editor exposed the private key',
    );
    await dialog.getByLabel('Server host key', { exact: true }).fill(pin);
    await dialog.getByRole('button', { name: 'Save Changes', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    await save();
  }
  await page.goto(origin + '/panel/outbound');
  phase = 'create SSH outbound';
  await page.getByRole('button', { name: /Outbounds$/ }).click();
  const dialog = page.getByRole('dialog');
  await dialog.locator('#protocol').click();
  const sshOption = page.locator('.ant-select-item-option[title="ssh"]');
  for (let i = 0; i < 20 && !(await sshOption.count()); i++)
    await dialog.locator('#protocol').press('ArrowDown');
  await sshOption.click();
  await dialog.getByPlaceholder('unique-tag').fill(tag);
  await dialog.getByLabel('Address', { exact: true }).fill('127.0.0.1');
  await dialog.getByLabel('Port', { exact: true }).fill(String(sshPort));
  await dialog.getByLabel('Username', { exact: true }).fill('root');
  await dialog.getByRole('button', { name: 'Show / edit private key', exact: true }).click();
  await dialog.getByLabel('Private Key', { exact: true }).fill(privateKey);
  await dialog.getByRole('button', { name: 'Hide private key', exact: true }).click();
  await dialog.getByLabel('Server host key', { exact: true }).fill(hostKey);
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  await dialog.waitFor({ state: 'hidden' });
  await save();
  phase = 'reload and route';
  await page.reload();
  await row().waitFor();
  const stored = await api('/panel/api/xray/');
  const value = typeof stored === 'string' ? JSON.parse(stored) : stored;
  const authored = value.xraySetting.outbounds.find((o) => o.tag === tag);
  assert(
    authored?.protocol === 'ssh' &&
      authored.settings.privateKey === privateKey &&
      authored.settings.hostKey === hostKey,
    'Saved SSH credentials did not round trip',
  );
  value.xraySetting.routing.rules.push({
    type: 'field',
    inboundTag: ['ui-socks'],
    outboundTag: tag,
  });
  await api('/panel/api/xray/update', {
    xraySetting: JSON.stringify(value.xraySetting),
    outboundTestUrl: testURL,
  });
  const beforeAuth = authenticated();
  const bytes = await echoThroughSocks(socksPort, targetPort);
  assert(
    targetConnections === 1 && targetBytes === bytes,
    'Browser-created route did not reach the independent target exactly once',
  );
  assert(authenticated() > beforeAuth, 'Browser-created route bypassed real OpenSSH');
  const compiled = readFileSync(path.join(env.XUI_BIN_FOLDER, 'config.json'), 'utf8');
  assert(
    !compiled.includes(privateKey.split('\n')[1]) && !compiled.includes('privateKey'),
    'Runtime core configuration exposed upstream private keys',
  );

  await page.reload();
  const beforeProbe = authenticated();
  phase = 'browser HTTP route probe';
  const probeResponse = page.waitForResponse((r) =>
    r.url().endsWith('/panel/api/xray/testOutbounds'),
  );
  await row().getByRole('button', { name: 'Check', exact: true }).click();
  const probe = await (await probeResponse).json();
  assert(
    probe.success && probe.obj[0].success && probe.obj[0].mode === 'http',
    'Browser SSH HTTP probe failed',
  );
  assert(authenticated() > beforeProbe, 'Browser probe bypassed OpenSSH');
  phase = 'wrong pin blocks traffic';
  await editPin(wrongHost);
  await assert.rejects(echoThroughSocks(socksPort, targetPort), 'Wrong host pin allowed traffic');
  assert(targetConnections === 1 && targetBytes === bytes, 'Wrong pin caused a direct fallback');
  phase = 'restore host pin';
  await editPin(hostKey);
  const restoredBytes = await echoThroughSocks(socksPort, targetPort);
  assert(
    targetConnections === 2 && targetBytes === bytes + restoredBytes,
    'Restored host pin did not restore the route',
  );

  const anonymous = await browser.newContext();
  phase = 'anonymous authorization';
  const denied = await anonymous.request.post(origin + '/panel/api/xray/', {
    form: {},
    maxRedirects: 0,
  });
  assert(denied.status() === 404, 'Unauthenticated configuration request was not rejected');
  assert(
    !(await denied.text()).includes(privateKey.split('\n')[1]),
    'Unauthenticated response exposed a private key',
  );
  phase = 'restricted API token authorization';
  for (const scope of ['monitor', 'node-sync']) {
    const token = await api('/panel/api/setting/apiTokens/create', { name: `ui-${scope}`, scope });
    secrets.push(token.token);
    const headers = { Authorization: `Bearer ${token.token}` };
    const allowed = await anonymous.request.get(origin + '/panel/api/server/status', { headers });
    assert(
      allowed.status() === 200 && (await allowed.json()).success,
      `${scope} token was not valid`,
    );
    for (const [method, route] of [
      ['post', '/panel/api/xray/'],
      ['post', '/panel/api/xray/update'],
      ['post', '/panel/api/xray/testOutbounds'],
      ['get', '/panel/api/xray/getXrayResult'],
      ['get', '/panel/api/server/getDb'],
    ]) {
      const response = await anonymous.request[method](origin + route, { headers });
      assert(response.status() === 403, `${scope} token accessed ${route}`);
      assert(
        !(await response.text()).includes(privateKey.split('\n')[1]),
        `${scope} response exposed a private key`,
      );
    }
  }
  await anonymous.close();

  phase = 'database backup and restore';
  const backupResponse = await page.request.get(origin + '/panel/api/server/getDb');
  assert(backupResponse.ok(), 'Admin could not download a database backup');
  const backup = await backupResponse.body();
  assert(
    backup.subarray(0, 16).equals(Buffer.from('SQLite format 3\0')),
    'Backup was not a SQLite database',
  );
  await editPin(wrongHost);
  await assert.rejects(
    echoThroughSocks(socksPort, targetPort),
    'Wrong host pin allowed traffic before restore',
  );
  const csrf = await (await page.request.get(origin + '/csrf-token')).json();
  const imported = await page.request.post(origin + '/panel/api/server/importDB', {
    headers: { 'X-CSRF-Token': csrf.obj },
    multipart: {
      db: { name: 'ssh-outbound.db', mimeType: 'application/octet-stream', buffer: backup },
    },
  });
  assert(imported.ok() && (await imported.json()).success, 'Database restore failed');
  await until(
    () =>
      readFileSync(path.join(temp, 'panel.log'), 'utf8').includes(
        'Web server restarted successfully.',
      ),
    'panel restart after restore',
  );
  await page.context().clearCookies();
  await page.goto(origin);
  await page.getByPlaceholder('Username', { exact: true }).fill('ssh-outbound-ui');
  await page.getByPlaceholder('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Log In', exact: true }).click();
  await page.waitForURL('**/panel/**');
  const restoredRaw = await api('/panel/api/xray/');
  const restored = (
    typeof restoredRaw === 'string' ? JSON.parse(restoredRaw) : restoredRaw
  ).xraySetting.outbounds.find((o) => o.tag === tag);
  assert(
    restored?.settings.hostKey === hostKey && restored.settings.privateKey === privateKey,
    'Backup did not restore authored SSH credentials',
  );
  const beforeRestoreAuth = authenticated();
  const backupBytes = await echoThroughSocks(socksPort, targetPort);
  assert(
    targetConnections === 3 && targetBytes === bytes + restoredBytes + backupBytes,
    'Restored route did not reach the independent target exactly once',
  );
  assert(
    authenticated() > beforeRestoreAuth,
    'Restored route did not create a fresh SSH connector',
  );
  await page.goto(origin + '/panel/outbound');
  await row().getByRole('button', { name: 'Edit', exact: true }).click();
  assert(
    (await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Show / edit private key', exact: true })
      .count()) === 1,
    'Restored editor exposed the private key',
  );
  assert(pageErrors.length === 0, 'Browser reported a JavaScript error');
  await screenshot();
  console.log(
    JSON.stringify({
      success: true,
      targetConnections,
      targetBytes,
      upstreamAuthentications: authenticated(),
      httpStatus: probe.obj[0].httpStatus,
      unauthorizedStatus: denied.status(),
      restrictedScopes: ['monitor', 'node-sync'],
      backupRestored: true,
    }),
  );
} catch (error) {
  let message = error.message;
  for (const secret of secrets) message = message.split(secret).join('[redacted]');
  console.error(`${phase}: ${message}`);
  await screenshot().catch(() => {});
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  for (const child of processes.reverse()) await stop(child);
  for (const socket of targetSockets) socket.destroy();
  if (target) await new Promise((resolve) => target.close(resolve));
  for (const log of logs) closeSync(log);
  rmSync(temp, { recursive: true, force: true });
}
