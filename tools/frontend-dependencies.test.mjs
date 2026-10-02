import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import { archiveFiles, collectFrontendDependencies } from './frontend-dependencies.mjs';

const repository = path.resolve(import.meta.dirname, '..');
const lock = JSON.parse(await fs.readFile(path.join(repository, 'frontend/package-lock.json')));
const react = lock.packages['node_modules/react'];
const hex = Buffer.from(react.integrity.split('-')[1], 'base64').toString('hex');
const cachePath = `_cacache/content-v2/sha512/${hex.slice(0, 2)}/${hex.slice(2, 4)}/${hex.slice(4)}`;
const archive = await fs.readFile(path.join(os.homedir(), '.npm', cachePath));
const source = archiveFiles(archive);
const digest = (data) => crypto.createHash('sha256').update(data).digest('hex');

async function fixture() {
  // Deliberately retained so failures and tampered fixtures can be inspected.
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'paired-frontend-dependencies-test-'));
  const frontendDir = path.join(root, 'frontend'), distDir = path.join(root, 'dist'), cacheDir = path.join(root, 'cache');
  await fs.mkdir(frontendDir); await fs.mkdir(path.join(distDir, 'assets'), { recursive: true });
  await fs.writeFile(path.join(distDir, 'index.html'), '<script src="assets/react.js"></script>');
  await fs.writeFile(path.join(distDir, 'assets/react.js'), 'compiled react fixture');
  await fs.writeFile(path.join(frontendDir, 'package-lock.json'), JSON.stringify({ lockfileVersion: 3, packages: { 'node_modules/react': react } }));
  for (const [name, data] of source) {
    const file = path.join(frontendDir, 'node_modules/react', name.slice('package/'.length));
    await fs.mkdir(path.dirname(file), { recursive: true }); await fs.writeFile(file, data);
  }
  const cachedArchive = path.join(cacheDir, cachePath);
  await fs.mkdir(path.dirname(cachedArchive), { recursive: true }); await fs.writeFile(cachedArchive, archive);
  return { root, frontendDir, distDir, cacheDir, cachedArchive, outputDir: path.join(root, 'closure'), moduleIDs: [{ id: path.join(frontendDir, 'node_modules/react/cjs/react.production.js'), kind: 'bundled-module' }] };
}

test('bundled React retains its complete original MIT notice and exact locked source archive', async () => {
  const f = await fixture(), manifest = await collectFrontendDependencies(f);
  const dep = manifest.dependencies[0];
  assert.equal(dep.name, 'react'); assert.equal(dep.version, '19.3.0'); assert.equal(dep.integrity, react.integrity);
  assert.deepEqual(await fs.readFile(path.join(f.outputDir, dep.archive)), archive);
  assert.equal(dep.archiveSHA256, digest(archive));
  assert.equal(dep.inputs[0].sha256, digest(source.get('package/cjs/react.production.js')));
  const notice = await fs.readFile(path.join(f.outputDir, dep.notices.find((n) => n.archivePath === 'package/LICENSE').path));
  assert.deepEqual(notice, source.get('package/LICENSE'));
  assert.match(notice.toString(), /Permission is hereby granted/); assert.match(notice.toString(), /THE SOFTWARE IS PROVIDED/);
  assert.equal(JSON.stringify(manifest).includes(f.root), false);
  assert.deepEqual(manifest.outputs.map((o) => o.path), ['assets/react.js', 'index.html']);
});

test('changed installed production source cannot borrow a pristine locked archive', async () => {
  const f = await fixture();
  await fs.appendFile(f.moduleIDs[0].id, '\n// tampered input');
  await assert.rejects(collectFrontendDependencies(f), /bundled npm source differs/);
});

test('changed complete notice is rejected against the locked source archive', async () => {
  const f = await fixture();
  await fs.writeFile(path.join(f.frontendDir, 'node_modules/react/LICENSE'), 'MIT');
  await assert.rejects(collectFrontendDependencies(f), /installed npm notice differs/);
});

test('changed archive is rejected using the original lock integrity', async () => {
  const f = await fixture(); await fs.appendFile(f.cachedArchive, 'changed');
  await assert.rejects(collectFrontendDependencies(f), /integrity mismatch/);
});

test('installed version cannot be substituted for a different locked version', async () => {
  const f = await fixture();
  await fs.writeFile(path.join(f.frontendDir, 'package-lock.json'), JSON.stringify({ lockfileVersion: 3, packages: { 'node_modules/react': { ...react, version: '19.2.0' } } }));
  await assert.rejects(collectFrontendDependencies(f), /version differs/);
});

test('production closure rejects a symlink in place of a bundled file', async () => {
  const f = await fixture(), input = f.moduleIDs[0].id;
  await fs.rename(input, `${input}.original`); await fs.symlink(`${input}.original`, input);
  await assert.rejects(collectFrontendDependencies(f), /regular file/);
});
