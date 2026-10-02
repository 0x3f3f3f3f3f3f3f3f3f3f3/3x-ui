// Distribution-only metadata: never emitted into the embedded frontend.
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import zlib from 'node:zlib';

const sha256 = (data) => crypto.createHash('sha256').update(data).digest('hex');
const slash = (p) => p.split(path.sep).join('/');
const noticeName = (p) => /^(?:licen[sc]e|copying|copyright|notice|third[-_ ]?party(?:[-_ ]?(?:licen[sc]es?|notices?))?)(?:[._ -]|$)/i.test(path.posix.basename(p));
const safePath = (p) => p && !p.includes('\\') && !p.includes('\0') && !p.startsWith('/') && p.split('/').every((s) => s && s !== '.' && s !== '..');
const licenseTemplateSource = { MIT: 'https://spdx.org/licenses/MIT.html', 'Apache-2.0': 'https://www.apache.org/licenses/LICENSE-2.0.txt' };
const declaredLicense = (metadata) => typeof metadata.license === 'string' ? metadata.license : metadata.licenses?.length === 1 ? metadata.licenses[0].type : '';
const completeTerms = (text) => /permission is hereby granted/i.test(text) && /the software is provided/i.test(text) || /terms and conditions for use, reproduction/i.test(text) && /end of terms and conditions/i.test(text);

function integrityParts(integrity) {
  const m = /^(sha512|sha384|sha256|sha1)-([A-Za-z0-9+/]+={0,2})$/.exec(integrity || '');
  if (!m) throw new Error('locked npm archive requires a single supported SRI digest');
  const digest = Buffer.from(m[2], 'base64');
  if (digest.length !== crypto.createHash(m[1]).digest().length || digest.toString('base64') !== m[2]) throw new Error('invalid npm integrity');
  return { algorithm: m[1], digest };
}

// Read npm's unmodified published source tarball; it is retained in full.
// Parsing never writes tar paths to disk and rejects links and special files.
export function archiveFiles(compressed) {
  const tar = zlib.gunzipSync(compressed, { maxOutputLength: 256 * 1024 * 1024 });
  const files = new Map();
  let localPax = {}, globalPax = {}, longName;
  const string = (b) => b.toString('utf8').split('\0')[0];
  for (let offset = 0; offset + 512 <= tar.length;) {
    const header = tar.subarray(offset, offset + 512);
    if (!header.some(Boolean)) break;
    const size = parseInt(string(header.subarray(124, 136)).trim(), 8);
    if (!Number.isSafeInteger(size) || size < 0 || offset + 512 + size > tar.length) throw new Error('invalid npm tar entry size');
    const expected = parseInt(string(header.subarray(148, 156)).trim(), 8);
    const checksum = header.reduce((n, byte, i) => n + (i >= 148 && i < 156 ? 32 : byte), 0);
    if (checksum !== expected) throw new Error('invalid npm tar header checksum');
    const body = tar.subarray(offset + 512, offset + 512 + size);
    offset += 512 + Math.ceil(size / 512) * 512;
    const type = String.fromCharCode(header[156] || 48);
    if (type === 'x' || type === 'g') {
      const values = {};
      for (let i = 0; i < body.length;) {
        const space = body.indexOf(32, i), count = Number(body.subarray(i, space).toString());
        if (space < i || !Number.isSafeInteger(count) || count <= space - i + 1 || i + count > body.length) throw new Error('invalid npm PAX record');
        const record = body.subarray(space + 1, i + count - 1).toString();
        const equals = record.indexOf('=');
        if (equals <= 0) throw new Error('invalid npm PAX value');
        values[record.slice(0, equals)] = record.slice(equals + 1);
        i += count;
      }
      if (type === 'g') globalPax = { ...globalPax, ...values }; else localPax = values;
      continue;
    }
    if (type === 'L') { longName = string(body); continue; }
    let name = localPax.path || globalPax.path || longName || [string(header.subarray(345, 500)), string(header.subarray(0, 100))].filter(Boolean).join('/');
    localPax = {}; longName = undefined;
    if (type === '5') name = name.replace(/\/$/, '');
    if (!safePath(name) || (!name.startsWith('package/') && !(type === '5' && name === 'package'))) throw new Error(`unsafe npm tar path: ${name}`);
    if (type === '5') continue;
    if (type !== '0') throw new Error(`npm source archive contains a link or special file: ${name}`);
    if (files.has(name)) throw new Error(`duplicate npm tar path: ${name}`);
    files.set(name, body);
  }
  if (!files.has('package/package.json')) throw new Error('npm source archive has no package metadata');
  return files;
}

async function regularFile(file) {
  const info = await fs.lstat(file);
  if (!info.isFile() || info.isSymbolicLink()) throw new Error(`expected a regular file: ${file}`);
  return fs.readFile(file);
}

async function walkFiles(root, prefix = '') {
  const result = [];
  for (const entry of await fs.readdir(path.join(root, prefix), { withFileTypes: true })) {
    const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (!safePath(relative)) throw new Error('unsafe generated frontend path');
    if (entry.isDirectory()) result.push(...await walkFiles(root, relative));
    else if (entry.isFile()) result.push(relative);
    else throw new Error(`frontend contains a link or special file: ${relative}`);
  }
  return result.sort();
}

async function buildInputHashes(frontendDir) {
  const repository = path.dirname(frontendDir), inputs = [];
  const add = async (relative) => inputs.push({ path: relative, sha256: sha256(await regularFile(path.join(repository, relative))) });
  for (const directory of ['frontend/src', 'frontend/public', 'frontend/scripts', 'internal/web/translation']) {
    const exists = await fs.lstat(path.join(repository, directory)).catch((e) => { if (e.code !== 'ENOENT') throw e; });
    if (!exists) continue;
    if (!exists.isDirectory() || exists.isSymbolicLink()) throw new Error('frontend source input directory contains a link');
    for (const file of await walkFiles(path.join(repository, directory))) await add(`${directory}/${file}`);
  }
  for (const file of ['.nvmrc', 'frontend/package.json', 'frontend/package-lock.json', 'frontend/vite.config.js', 'frontend/index.html', 'frontend/login.html', 'frontend/subpage.html', 'tools/frontend-dependencies.mjs', 'tools/frontenddepsverify/licenses/MIT.txt', 'tools/frontenddepsverify/licenses/Apache-2.0.txt']) {
    const exists = await fs.lstat(path.join(repository, file)).catch((e) => { if (e.code !== 'ENOENT') throw e; });
    if (exists) await add(file);
  }
  return inputs.sort((a, b) => a.path.localeCompare(b.path, 'en'));
}

async function lockedArchive(lock, cache) {
  const { algorithm, digest } = integrityParts(lock.integrity);
  const hex = digest.toString('hex');
  const cached = path.join(cache, '_cacache/content-v2', algorithm, hex.slice(0, 2), hex.slice(2, 4), hex.slice(4));
  let data;
  try { data = await regularFile(cached); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  if (!data) {
    // Only the immutable URL from package-lock is used, never a version range.
    const url = new URL(lock.resolved);
    if (url.protocol !== 'https:' || url.username || url.password) throw new Error('npm source archive requires an HTTPS lockfile URL');
    const response = await fetch(url, { signal: AbortSignal.timeout(120000) });
    if (!response.ok) throw new Error(`locked npm archive unavailable: ${lock.resolved}`);
    if (Number(response.headers.get('content-length')) > 128 * 1024 * 1024) throw new Error('npm archive exceeds size bound');
    const chunks = []; let size = 0;
    for await (const chunk of response.body) {
      size += chunk.length;
      if (size > 128 * 1024 * 1024) throw new Error('npm archive exceeds size bound');
      chunks.push(chunk);
    }
    data = Buffer.concat(chunks);
  }
  if (data.length > 128 * 1024 * 1024 || !crypto.createHash(algorithm).update(data).digest().equals(digest)) throw new Error('locked npm archive integrity mismatch');
  return data;
}

export async function collectFrontendDependencies({ frontendDir, distDir, outputDir, moduleIDs, cacheDir, licenseTemplatesDir = path.resolve(frontendDir, '../tools/frontenddepsverify/licenses') }) {
  if (process.version !== 'v26.10.0') throw new Error('frontend source closure requires Node 26.10.0');
  const lockData = await regularFile(path.join(frontendDir, 'package-lock.json'));
  const lock = JSON.parse(lockData);
  if (lock.lockfileVersion !== 3 || !lock.packages) throw new Error('frontend requires npm lockfile version 3');
  const selected = new Map();
  const generatedRuntimeInputs = [];
  for (const { id, kind } of moduleIDs) {
    const file = id.replace(/^\0+/, '').split('?')[0];
    if (!path.isAbsolute(file) && /(?:vite[/:]|rolldown[/:])/.test(file)) {
      const supplier = file.includes('rolldown') ? ['node_modules/rolldown', 'dist/experimental-runtime-base.mjs'] : ['node_modules/vite', 'dist/node/chunks/node.js'];
      if (!lock.packages[supplier[0]]) throw new Error('generated runtime supplier is missing from lockfile');
      if (!selected.has(supplier[0])) selected.set(supplier[0], new Map());
      selected.get(supplier[0]).set(supplier[1], 'generated-runtime-source');
      generatedRuntimeInputs.push({ id: file, packagePath: supplier[0], sourcePath: supplier[1] });
    }
    if (!path.isAbsolute(file) || !slash(file).includes('/node_modules/')) continue;
    const relative = slash(path.relative(frontendDir, file));
    if (!safePath(relative)) throw new Error('bundled npm input is outside the locked frontend');
    let packagePath = path.posix.dirname(relative);
    while (packagePath !== '.' && !lock.packages[packagePath]) packagePath = path.posix.dirname(packagePath);
    if (packagePath === '.' || !packagePath.startsWith('node_modules/')) throw new Error(`bundled npm input is absent from package-lock: ${relative}`);
    if (!selected.has(packagePath)) selected.set(packagePath, new Map());
    selected.get(packagePath).set(relative.slice(packagePath.length + 1), kind);
  }
  if (!selected.size) throw new Error('bundler supplied no production npm inputs');
  await fs.mkdir(outputDir, { recursive: false });
  const files = [], dependencies = [];
  const save = async (relative, data) => {
    if (!safePath(relative)) throw new Error('unsafe frontend closure destination');
    await fs.mkdir(path.dirname(path.join(outputDir, relative)), { recursive: true });
    await fs.writeFile(path.join(outputDir, relative), data, { flag: 'wx' });
    const record = { path: relative, sha256: sha256(data) }; files.push(record); return record;
  };
  await save('package-lock.json', lockData);
  const cache = cacheDir || process.env.npm_config_cache || (process.platform === 'win32' ? path.join(process.env.LOCALAPPDATA || os.homedir(), 'npm-cache') : path.join(os.homedir(), '.npm'));
  for (const packagePath of [...selected.keys()].sort()) {
    const locked = lock.packages[packagePath];
    if (!locked.version || !locked.resolved || !locked.integrity || locked.link) throw new Error(`npm input is not an immutable locked package: ${packagePath}`);
    const archive = await lockedArchive(locked, cache);
    const source = archiveFiles(archive);
    const installedJSON = await regularFile(path.join(frontendDir, packagePath, 'package.json'));
    const packageJSON = source.get('package/package.json');
    if (!installedJSON.equals(packageJSON)) throw new Error(`installed npm metadata differs from locked archive: ${packagePath}`);
    const metadata = JSON.parse(packageJSON);
    if (!metadata.name || metadata.version !== locked.version) throw new Error(`installed npm version differs from lockfile: ${packagePath}`);
    const digest = sha256(archive), archivePath = `sources/${digest}.tgz`;
    if (!files.some((f) => f.path === archivePath)) await save(archivePath, archive);
    const inputs = [];
    for (const [file, kind] of [...selected.get(packagePath)].sort(([a], [b]) => a.localeCompare(b, 'en'))) {
      const original = source.get(`package/${file}`);
      if (!original || !(await regularFile(path.join(frontendDir, packagePath, file))).equals(original)) throw new Error(`bundled npm source differs from locked archive: ${packagePath}/${file}`);
      inputs.push({ path: file, sha256: sha256(original), kind });
    }
    let notices = [...source.keys()].filter(noticeName).sort();
    // Some small packages publish their notice only in README; retain it whole.
    if (!notices.length) notices = [...source.keys()].filter((f) => /^readme(?:\.|$)/i.test(path.posix.basename(f)) && /(?:permission is hereby granted|redistribution and use|copyright|license|licence)/i.test(source.get(f).toString())).sort();
    const noticeRecords = [];
    let noticeKind = 'published';
    const license = declaredLicense(metadata);
    const needsTemplate = !notices.length || (notices.every((f) => /^readme(?:\.|$)/i.test(path.posix.basename(f))) && !notices.some((f) => completeTerms(source.get(f).toString())));
    if (needsTemplate) {
      // An explicit SPDX declaration still supplies a license when an upstream
      // omits a notice file. Preserve its whole metadata (including authors and
      // contributors), and label standard terms instead of inventing copyright.
      if (!licenseTemplateSource[license] || (locked.license && locked.license !== license)) throw new Error(`locked production npm package has no supported complete published license: ${metadata.name}@${metadata.version}`);
      noticeKind = notices.length ? 'published-with-declared-license-template' : 'declared-license-template';
      const terms = await regularFile(path.join(licenseTemplatesDir, `${license}.txt`));
      const data = Buffer.concat([Buffer.from(`No complete standalone license text was published in this locked npm archive.\nAttribution and the ${license} declaration below are the original package.json;\npublished notices are retained separately. The following terms are the\nstandard ${license} license. No copyright year or holder has been inferred.\n\n`), packageJSON, Buffer.from(`\n\nStandard ${license} license terms:\n\n`), terms]);
      const noticePath = `notices/${digest}/DECLARED-LICENSE-${license}.txt`;
      if (!files.some((f) => f.path === noticePath)) await save(noticePath, data);
      noticeRecords.push({ path: noticePath, archivePath: 'package/package.json', sha256: sha256(data) });
    }
    for (const file of notices) {
      const data = source.get(file), installed = await regularFile(path.join(frontendDir, packagePath, file.slice('package/'.length)));
      if (!installed.equals(data)) throw new Error(`installed npm notice differs from locked archive: ${packagePath}/${file}`);
      const noticePath = `notices/${digest}/${file.slice('package/'.length)}`;
      if (!files.some((f) => f.path === noticePath)) await save(noticePath, data);
      noticeRecords.push({ path: noticePath, archivePath: file, sha256: sha256(data) });
    }
    dependencies.push({ packagePath, name: metadata.name, version: metadata.version, resolved: locked.resolved, integrity: locked.integrity, license: locked.license || license || '', archive: archivePath, archiveSHA256: digest, packageJSONSHA256: sha256(packageJSON), inputs, noticeKind, ...(needsTemplate ? { licenseTemplateSource: licenseTemplateSource[license] } : {}), notices: noticeRecords });
  }
  const outputs = [];
  for (const file of await walkFiles(distDir)) outputs.push({ path: file, sha256: sha256(await regularFile(path.join(distDir, file))) });
  if (!outputs.some((f) => f.path === 'index.html') || !outputs.some((f) => f.path.startsWith('assets/'))) throw new Error('compiled frontend is incomplete');
  const manifest = { schemaVersion: 1, nodeVersion: process.version, lockfileSHA256: sha256(lockData), buildInputs: await buildInputHashes(frontendDir), generatedRuntimeInputs: generatedRuntimeInputs.sort((a, b) => a.id.localeCompare(b.id, 'en')), outputs, dependencies, files: files.sort((a, b) => a.path.localeCompare(b.path, 'en')) };
  await fs.writeFile(path.join(outputDir, 'frontend-compiled-deps.json'), `${JSON.stringify(manifest, null, 2)}\n`, { flag: 'wx' });
  return manifest;
}

export function frontendDependencyClosurePlugin(frontendDir, distDir) {
  let moduleIDs = [];
  return {
    name: 'xui-frontend-distribution-source-closure',
    apply: 'build',
    generateBundle(_options, bundle) {
      const included = new Map();
      for (const entry of Object.values(bundle)) if (entry.type === 'chunk') for (const id of Object.keys(entry.modules)) included.set(id, 'bundled-module');
      // CSS and asset imports can disappear from chunk.modules after extraction.
      for (const id of this.getModuleIds()) if (/\.(?:css|less|scss|sass|styl|svg|png|jpe?g|gif|webp|avif|woff2?|ttf|otf)(?:\?|$)/i.test(id)) included.set(id, 'style-or-asset-input');
      moduleIDs = [...included].map(([id, kind]) => ({ id, kind }));
    },
    async closeBundle() {
      const build = path.resolve(frontendDir, '../build');
      await fs.mkdir(build, { recursive: true });
      const staging = await fs.mkdtemp(path.join(build, 'frontend-license-stage-'));
      const generated = path.join(staging, 'closure');
      await collectFrontendDependencies({ frontendDir, distDir, outputDir: generated, moduleIDs });
      const destination = path.join(build, 'frontend-licenses');
      // Only the known generated directory is replaced; failed staging survives.
      const old = await fs.lstat(destination).catch((e) => { if (e.code !== 'ENOENT') throw e; });
      if (old && (!old.isDirectory() || old.isSymbolicLink())) throw new Error('frontend license output is not a regular directory');
      if (old) await fs.rm(destination, { recursive: true });
      await fs.rename(generated, destination);
      await fs.rmdir(staging);
    },
  };
}
