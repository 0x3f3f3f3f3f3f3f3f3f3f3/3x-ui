import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import dgram from 'node:dgram';
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
const mieruBinary = process.env.XUI_MIERU_E2E_BINARY;
assert(
  panelBinary && xrayBinary && mieruBinary,
  'Set XUI_E2E_PANEL, XRAY_E2E_BINARY and XUI_MIERU_E2E_BINARY',
);
const root = path.resolve(fileURLToPath(new URL('../..', import.meta.url)));
const temp = mkdtempSync(path.join(tmpdir(), 'xui-mieru-ui-'));
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
const logs = [];
const sockets = new Set();
const datagrams = new Set();
let browser;
let page;
let phase = 'startup';
let tcpTarget;
let udpTarget;

function step(value) {
  phase = value;
  console.log(`CHECK ${phase}`);
}

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
function start(binary, args, childEnv, name) {
  const fd = openSync(path.join(temp, name + '.log'), 'w', 0o600);
  logs.push(fd);
  const child = spawn(binary, args, { cwd: temp, env: childEnv, stdio: ['ignore', fd, fd] });
  processes.push(child);
  return child;
}
async function read(socket, size) {
  const chunks = [];
  let received = 0;
  while (received < size) {
    const bytes = socket.read(Math.min(size - received, socket.readableLength) || size - received);
    if (bytes) {
      chunks.push(bytes);
      received += bytes.length;
      continue;
    }
    if (socket.destroyed || socket.readableEnded) throw new Error('SOCKS stream closed');
    await new Promise((resolve, reject) => {
      const cleanup = () => {
        socket.off('readable', ready);
        socket.off('error', failed);
        socket.off('close', closed);
      };
      const ready = () => {
        cleanup();
        resolve();
      };
      const failed = (error) => {
        cleanup();
        reject(error);
      };
      const closed = () => failed(new Error('SOCKS stream closed'));
      socket.once('readable', ready);
      socket.once('error', failed);
      socket.once('close', closed);
    });
  }
  return Buffer.concat(chunks);
}
async function socks(proxyPort, targetPort, command = 1, timeout = 5000) {
  const socket = net.connect(proxyPort, '127.0.0.1');
  sockets.add(socket);
  socket.on('error', () => {});
  const timer = setTimeout(() => socket.destroy(new Error('SOCKS request timed out')), timeout);
  try {
    await once(socket, 'connect');
    socket.write(Buffer.from([5, 1, 0]));
    assert.deepEqual(await read(socket, 2), Buffer.from([5, 0]));
    socket.write(Buffer.from([5, command, 0, 1, 127, 0, 0, 1, targetPort >> 8, targetPort & 255]));
    const reply = await read(socket, 4);
    assert.equal(reply[1], 0, 'Official client rejected SOCKS request');
    assert.equal(reply[3], 1, 'Loopback fixture expects an IPv4 SOCKS bind address');
    const endpoint = await read(socket, 6);
    return { socket, port: endpoint.readUInt16BE(4), host: [...endpoint.subarray(0, 4)].join('.') };
  } catch (error) {
    socket.destroy();
    throw error;
  } finally {
    clearTimeout(timer);
  }
}
async function readySocks(proxyPort, targetPort) {
  const deadline = Date.now() + 2000;
  let cause;
  while (Date.now() < deadline) {
    try {
      const { socket } = await socks(proxyPort, targetPort, 1, deadline - Date.now());
      if (Date.now() > deadline) {
        socket.destroy();
        throw new Error('Native admission exceeded its absolute startup deadline');
      }
      return socket;
    } catch (error) {
      cause = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error('Native admission did not recover within 2s of client startup', { cause });
}

try {
  mkdirSync(env.XUI_DB_FOLDER);
  mkdirSync(env.XUI_BIN_FOLDER);
  const panelPort = await port();
  env.XUI_PORT = String(panelPort);
  const password = randomUUID();
  execFileSync(
    panelBinary,
    [
      'setting',
      '-username',
      'mieru-ui',
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
  template.inbounds[0].port = await port();
  template.outbounds[0].settings = {};
  template.routing.rules = [template.routing.rules[0]];
  delete template.metrics;
  execFileSync(
    'python3',
    [
      '-c',
      'import json,sqlite3,sys\nc=sqlite3.connect(sys.argv[1])\nfor k,v in json.load(sys.stdin).items():\n c.execute("delete from settings where key=?",(k,))\n c.execute("insert into settings(key,value) values (?,?)",(k,v))\nc.commit()',
      path.join(env.XUI_DB_FOLDER, 'x-ui.db'),
    ],
    {
      input: JSON.stringify({
        subEnable: 'false',
        subJsonEnable: 'false',
        subClashEnable: 'false',
        xrayTemplateConfig: JSON.stringify(template),
      }),
    },
  );
  symlinkSync(
    path.resolve(xrayBinary),
    path.join(env.XUI_BIN_FOLDER, `xray-linux-${process.arch === 'x64' ? 'amd64' : process.arch}`),
  );
  start(panelBinary, [], env, 'panel');
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
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await page.goto(origin);
  await page.getByPlaceholder('Username', { exact: true }).fill('mieru-ui');
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
  tcpTarget = net.createServer((socket) => {
    sockets.add(socket);
    socket.on('error', () => {});
    socket.pipe(socket);
  });
  tcpTarget.listen(0, '127.0.0.1');
  await once(tcpTarget, 'listening');
  udpTarget = dgram.createSocket('udp4');
  udpTarget.on('message', (bytes, sender) => udpTarget.send(bytes, sender.port, sender.address));
  udpTarget.bind(0, '127.0.0.1');
  await once(udpTarget, 'listening');

  for (const network of ['tcp', 'udp', 'both']) {
    step(`${network}: create inbound`);
    const inboundPort = await port();
    const email = `mieru-browser-${network}`;
    const nativePassword = 'p@:/密-' + randomUUID();
    await page.goto(origin + '/panel/inbounds');
    await page.getByRole('button', { name: 'Add Inbound', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.locator('#protocol').click();
    const option = page.locator('.ant-select-item-option[title="mieru"]');
    for (let attempt = 0; attempt < 25 && !(await option.count()); attempt++)
      await dialog.locator('#protocol').press('ArrowDown');
    assert.equal(await option.count(), 1, 'mieru protocol option missing');
    await option.click();
    await dialog.getByLabel('Address', { exact: true }).fill('127.0.0.1');
    await dialog.getByLabel('Port', { exact: true }).fill(String(inboundPort));
    await dialog.getByLabel('Remark', { exact: true }).fill(`browser mieru ${network}`);
    await dialog.getByLabel('Share address strategy', { exact: true }).click();
    await page.locator('.ant-select-item-option[title="Custom"]').click();
    await dialog.getByLabel('Custom share address', { exact: true }).fill('127.0.0.1');
    await dialog.getByRole('tab', { name: 'Protocol', exact: true }).click();
    await dialog.locator('#mieru-network').click();
    await page
      .locator(
        `.ant-select-item-option[title="${network === 'both' ? 'TCP, UDP' : network.toUpperCase()}"]`,
      )
      .click();
    const response = page.waitForResponse(
      (res) => res.url().endsWith('/panel/api/inbounds/add') && res.request().method() === 'POST',
    );
    await dialog.getByRole('button', { name: 'Create', exact: true }).click();
    const created = await (await response).json();
    assert.equal(created.success, true, created.msg);
    const inbound = created.obj;
    await dialog.waitFor({ state: 'hidden' });
    await page.getByRole('status', { name: /mieru runtime: Idle/i }).waitFor();

    step(`${network}: create client`);
    await page.goto(origin + '/panel/clients');
    await page.getByRole('button', { name: 'Add Clients', exact: true }).click();
    await dialog.getByPlaceholder('Email', { exact: true }).fill(email);
    await dialog.getByLabel('Traffic Limit (GB)', { exact: true }).fill('0.09765625');
    await dialog.getByRole('button', { name: 'Select all', exact: true }).click();
    await dialog.getByRole('tab', { name: 'Credentials', exact: true }).click();
    await dialog
      .locator('.ant-form-item')
      .filter({ has: page.locator('label').filter({ hasText: /^Password$/ }) })
      .locator('input')
      .fill(nativePassword);
    await dialog.getByRole('button', { name: 'Create', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    await page.getByText(email, { exact: true }).first().waitFor();
    await until(
      async () =>
        (await api('/panel/api/inbounds/mieru/status')).some(
          (row) => row.inboundId === inbound.id && row.state === 'running',
        ),
      'scheduled native runtime application',
      35000,
    );

    step(`${network}: browser download and official import`);
    await page.getByRole('button', { name: 'Client Information', exact: true }).click();
    await dialog.getByRole('button', { name: 'QR Code', exact: true }).click();
    const downloadEvent = page.waitForEvent('download');
    await page.getByRole('button', { name: 'Download mieru JSON', exact: true }).click();
    const download = await downloadEvent;
    assert.equal(download.suggestedFilename(), 'mieru.json');
    const downloadedPath = path.join(temp, `${network}-download.json`);
    await download.saveAs(downloadedPath);
    const downloaded = JSON.parse(readFileSync(downloadedPath, 'utf8'));
    assert.equal(downloaded.profiles.length, 1);
    const profile = downloaded.profiles[0];
    assert.equal(profile.user.name, email);
    assert.equal(profile.user.password, nativePassword);
    assert.equal(profile.servers[0].ipAddress, '127.0.0.1');
    assert.deepEqual(
      profile.multiplexing,
      network === 'udp' ? undefined : { level: 'MULTIPLEXING_OFF' },
    );
    assert.deepEqual(
      profile.servers[0].portBindings,
      (network === 'both' ? ['TCP', 'UDP'] : [network.toUpperCase()]).map((protocol) => ({
        port: inboundPort,
        protocol,
      })),
    );
    assert.equal(downloaded.activeProfile, profile.profileName);
    assert.equal(downloaded.socks5Port, 1080);
    assert.equal(downloaded.socks5ListenLAN, false);
    assert(!JSON.stringify(downloaded).includes('bridgePort'));
    assert(!JSON.stringify(downloaded).includes('quotas'));
    const configPath = path.join(temp, `${network}-client.json`);
    const nativeEnv = { ...env, MIERU_CONFIG_JSON_FILE: configPath };
    execFileSync(mieruBinary, ['apply', 'config', downloadedPath], {
      env: nativeEnv,
      stdio: 'pipe',
    });
    const imported = JSON.parse(readFileSync(configPath, 'utf8'));
    assert.equal(imported.profiles.length, 1);
    assert.equal(imported.profiles[0].profileName, profile.profileName);
    assert.equal(imported.profiles[0].user.name, email);
    assert.equal(imported.profiles[0].user.password, nativePassword);
    assert.deepEqual(imported.profiles[0].servers, profile.servers);
    assert.deepEqual(imported.profiles[0].multiplexing, profile.multiplexing);
    imported.socks5Port = await port();
    writeFileSync(configPath, JSON.stringify(imported), { mode: 0o600 });
    await dialog.getByRole('button', { name: 'Close', exact: true }).click();

    step(`${network}: traffic policy editor`);
    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    await dialog.getByRole('tab', { name: 'Traffic policy', exact: true }).click();
    await dialog.getByLabel('Upload limit (B/s)', { exact: true }).fill('32768');
    await dialog.getByLabel('Download limit (B/s)', { exact: true }).fill('65536');
    await dialog.getByLabel('Billing multiplier', { exact: true }).fill('1.5');
    await dialog.getByRole('button', { name: 'Apply traffic policy', exact: true }).click();
    await page.getByText('Traffic policy applied.', { exact: true }).waitFor();
    const saved = await api(`/panel/api/clients/policy/${email}`);
    assert.equal(saved.uploadBps, 32768);
    assert.equal(saved.downloadBps, 65536);
    assert.equal(saved.multiplier, '1.5');
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();

    step(`${network}: official native TCP and UDP data path`);
    const native = start(mieruBinary, ['run'], nativeEnv, network);
    await until(async () => {
      assert.equal(native.exitCode, null, 'Official client exited before SOCKS startup');
      return new Promise((resolve) => {
        const socket = net.connect(imported.socks5Port, '127.0.0.1');
        socket.once('error', () => resolve(false));
        socket.once('connect', () => {
          socket.destroy();
          resolve(true);
        });
      });
    }, 'official client SOCKS listener');
    const tcp = await readySocks(imported.socks5Port, tcpTarget.address().port);
    tcp.setTimeout(10000, () => tcp.destroy(new Error('TCP payload timed out')));
    const payload = Buffer.alloc(16384, 0x37);
    tcp.write(payload);
    assert.deepEqual(await read(tcp, payload.length), payload);
    tcp.setTimeout(0);
    tcp.resume();
    const association = await socks(imported.socks5Port, 0, 3);
    const udp = dgram.createSocket('udp4');
    datagrams.add(udp);
    udp.bind(0, '127.0.0.1');
    await once(udp, 'listening');
    const udpPort = udpTarget.address().port;
    const udpPayload = Buffer.alloc(1024, 0x62);
    const packet = Buffer.concat([
      Buffer.from([0, 0, 0, 1, 127, 0, 0, 1, udpPort >> 8, udpPort & 255]),
      udpPayload,
    ]);
    const received = once(udp, 'message', { signal: AbortSignal.timeout(10000) });
    udp.send(packet, association.port, association.host);
    const [reply] = await received;
    assert.deepEqual(reply, packet);
    await until(async () => {
      const current = await api(`/panel/api/clients/policy/${email}`);
      return (
        current.usage.up === '17408' &&
        current.usage.down === '17408' &&
        current.usage.billed === '52224'
      );
    }, 'raw TCP/UDP payload billed exactly once at 1.5x');
    await page.goto(origin + '/panel/inbounds');
    const running = page.getByRole('status', { name: /mieru runtime: Running/i });
    await until(
      async () => (await running.textContent({ timeout: 1000 }).catch(() => '')) === 'Running · 2',
      'two authenticated logical native sessions',
    );
    await page.setViewportSize({ width: 390, height: 844 });
    await running.waitFor();
    assert(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      'Mobile runtime badge overflows viewport',
    );
    await page.setViewportSize({ width: 1440, height: 1000 });

    step(`${network}: quota cutoff and disable`);
    const hydrated = await api(`/panel/api/clients/get/${email}`);
    await api(`/panel/api/clients/update/${email}`, {
      email,
      id: hydrated.client.uuid || '',
      subId: hydrated.client.subId,
      password: nativePassword,
      enable: true,
      totalGB: 40000,
    });
    await until(
      () => tcp.destroyed || tcp.readableEnded,
      'existing native TCP flow closes after quota reduction',
      2000,
    );
    const depleted = await api('/panel/api/clients/list/paged?filter=depleted');
    assert.equal(depleted.filtered, 1);
    assert.equal(depleted.items[0].billing.billed, '52224');
    assert.equal(depleted.items[0].billing.remaining, '0');
    assert.equal(depleted.items[0].billing.exhausted, true);
    await page.getByRole('switch').click();
    await page
      .getByRole('status', { name: /mieru runtime: Disabled/i })
      .waitFor({ timeout: 10000 });
    udp.close();
    datagrams.delete(udp);
    association.socket.destroy();
    tcp.destroy();
    await stop(native);
    await api(`/panel/api/clients/del/${email}`, {});
    await api(`/panel/api/inbounds/del/${inbound.id}`, {});
    assert.deepEqual(pageErrors, []);
    console.log(
      `PASS ${network}: browser CRUD, actual JSON download -> official mieru import -> TCP/UDP echo; raw 17408 B each direction, billed 52224 B at 1.5x; desktop/mobile runtime count 2; reduced quota closes existing TCP within 2s; disabled runtime and deleted owned resources.`,
    );
  }
} catch (error) {
  console.error(`Failed phase: ${phase}`);
  for (const name of ['panel', 'tcp', 'udp', 'both']) {
    try {
      const lines = readFileSync(path.join(temp, name + '.log'), 'utf8')
        .split('\n')
        .filter((line) => /error|warn|protected|failed/i.test(line));
      if (lines.length) console.error(`${name} diagnostics:`, lines.slice(-12).join('\n'));
    } catch {}
  }
  if (page && process.env.XUI_E2E_SCREENSHOT)
    await page.screenshot({
      path: process.env.XUI_E2E_SCREENSHOT,
      mask: [page.locator('input'), page.locator('textarea')],
    });
  throw error;
} finally {
  if (browser) await browser.close();
  for (const socket of sockets) socket.destroy();
  for (const socket of datagrams) socket.close();
  for (const child of processes.reverse()) await stop(child);
  if (tcpTarget) await new Promise((resolve) => tcpTarget.close(resolve));
  if (udpTarget) udpTarget.close();
  for (const fd of logs) closeSync(fd);
  rmSync(temp, { recursive: true, force: true });
}
