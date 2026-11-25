import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { install } from './install.mjs';

// These archives contain only "fixture kit-tck executable\n" under the release
// executable name. Real extraction is exercised on each native CI runner.
const tarArchive = Buffer.from('H4sIAFsxB2sC/+3SMQqAMBBE0a09RS4gBEPwPBoiSKx0Ax7fEAvBXhH8r5llmmk2zdpqSPIkW/Te1yzuaa1z1137vnNejJUX5E2HtUzKP03zrnmNJp1/YOIeQ9ZhXGIjAAAAAAAAAAAAAAAAAIDvOgCxRqZ3ACgAAA==', 'base64');
const zipArchive = Buffer.from('UEsDBBQAAAAIAJ2YeV183LQoHQAAABsAAAALAAAAa2l0LXRjay5leGVLy6woKS1KVcjOLNEtSc5WSK1ITS4tSUzKSeUCAFBLAQIUAxQAAAAIAJ2YeV183LQoHQAAABsAAAALAAAAAAAAAAAAAACAAQAAAABraXQtdGNrLmV4ZVBLBQYAAAAAAQABADkAAABGAAAAAAA=', 'base64');
const hostOS = { linux: 'Linux', darwin: 'macOS', win32: 'Windows' }[process.platform];
const hostArch = process.arch === 'arm64' ? 'ARM64' : 'X64';

async function fixture(t, overrides = {}) {
  const directory = await mkdtemp(join(tmpdir(), 'setup-tck-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const env = {
    INPUT_VERSION: 'v3.0.0-m.13', RUNNER_OS: hostOS, RUNNER_ARCH: hostArch,
    RUNNER_TEMP: directory, GITHUB_PATH: join(directory, 'path'),
    GITHUB_OUTPUT: join(directory, 'output'), SystemRoot: process.env.SystemRoot,
    ...overrides,
  };
  await writeFile(env.GITHUB_PATH, '');
  await writeFile(env.GITHUB_OUTPUT, '');
  const archive = env.RUNNER_OS === 'Windows' ? zipArchive : tarArchive;
  const digest = createHash('sha256').update(archive).digest('hex');
  const requests = [];
  const request = async (url) => {
    requests.push(url);
    if (url.endsWith('/checksums.txt')) {
      return new Response(releaseAssets.map(asset => `${digest}  ${asset}`).join('\n'));
    }
    return new Response(archive);
  };
  return { env, archive, digest, requests, request };
}

const releaseAssets = [
  'kit-tck_3.0.0-m.13_linux_amd64.tar.gz',
  'kit-tck_3.0.0-m.13_linux_arm64.tar.gz',
  'kit-tck_3.0.0-m.13_darwin_amd64.tar.gz',
  'kit-tck_3.0.0-m.13_darwin_arm64.tar.gz',
  'kit-tck_3.0.0-m.13_windows_amd64.zip',
  'kit-tck_3.0.0-m.13_windows_arm64.zip',
];

async function assertUnpublished(env) {
  assert.equal(await readFile(env.GITHUB_PATH, 'utf8'), '');
  assert.equal(await readFile(env.GITHUB_OUTPUT, 'utf8'), '');
}

test('verified archive installs the executable and publishes PATH and outputs', async t => {
  const f = await fixture(t, { INPUT_VERSION: '3.0.0-m.13' });
  const result = await install(f.env, f.request);
  assert.equal(await readFile(result.path, 'utf8'), 'fixture kit-tck executable\n');
  assert.equal(result.version, 'v3.0.0-m.13');
  assert.equal(await readFile(f.env.GITHUB_PATH, 'utf8'), `${dirname(result.path)}\n`);
  assert.equal(await readFile(f.env.GITHUB_OUTPUT, 'utf8'), `version=v3.0.0-m.13\npath=${result.path}\n`);
  if (process.platform !== 'win32') {
    assert.equal((await stat(result.path)).mode & 0o777, 0o755);
  }
  assert.deepEqual(f.requests.map(url => new URL(url).host), ['github.com', 'github.com']);
});

test('a matching caller SHA256 pin also permits installation', async t => {
  const f = await fixture(t);
  f.env.INPUT_SHA256 = f.digest.toUpperCase();
  assert.equal((await install(f.env, f.request)).version, 'v3.0.0-m.13');
});

for (const input of ['', 'latest', 'v3', 'v3.0', '../v3.0.0', 'v3.0.0\nextra', 'v03.0.0', 'v3.0.0-01']) {
  test(`invalid version ${JSON.stringify(input)} fails before download`, async t => {
    const f = await fixture(t, { INPUT_VERSION: input });
    await assert.rejects(install(f.env, f.request), /exact release version/);
    assert.deepEqual(f.requests, []);
    await assertUnpublished(f.env);
  });
}

for (const overrides of [{ INPUT_SHA256: 'not-a-hash' }, { RUNNER_OS: 'FreeBSD' }, { RUNNER_ARCH: 'ARM' }, { RUNNER_OS: 'toString' }]) {
  test(`unsupported input ${JSON.stringify(overrides)} fails before download`, async t => {
    const f = await fixture(t, overrides);
    await assert.rejects(install(f.env, f.request));
    assert.deepEqual(f.requests, []);
    await assertUnpublished(f.env);
  });
}

for (const [os, arch, asset] of [
  ['Linux', 'X64', releaseAssets[0]], ['Linux', 'ARM64', releaseAssets[1]],
  ['macOS', 'X64', releaseAssets[2]], ['macOS', 'ARM64', releaseAssets[3]],
  ['Windows', 'X64', releaseAssets[4]], ['Windows', 'ARM64', releaseAssets[5]],
]) {
  test(`${os}/${arch} requests its published release asset`, async t => {
    const f = await fixture(t, { RUNNER_OS: os, RUNNER_ARCH: arch });
    const urls = [];
    await assert.rejects(install(f.env, async url => {
      urls.push(url);
      return url.endsWith('/checksums.txt')
        ? new Response(`${'0'.repeat(64)}  ${asset}\n`)
        : new Response(f.archive);
    }), /SHA256 mismatch/);
    assert.deepEqual(urls, [
      'https://github.com/docker/sandbox-kit-spec/releases/download/v3.0.0-m.13/checksums.txt',
      `https://github.com/docker/sandbox-kit-spec/releases/download/v3.0.0-m.13/${asset}`,
    ]);
    await assertUnpublished(f.env);
  });
}

for (const kind of ['missing', 'duplicate', 'malformed']) {
  test(`${kind} release checksum fails before installing`, async t => {
    const f = await fixture(t);
    const contents = releaseAssets.map(asset => `${f.digest}  ${asset}`).join('\n');
    const broken = kind === 'missing' ? '' : kind === 'duplicate' ? `${contents}\n${contents}` : contents.replaceAll(f.digest, 'bad-hash');
    await assert.rejects(install(f.env, async () => new Response(broken)), /exactly one valid SHA256/);
    await assertUnpublished(f.env);
  });
}

test('caller pin rejects replacement archive even when its release checksum matches', async t => {
  const f = await fixture(t, { INPUT_SHA256: '0'.repeat(64) });
  await assert.rejects(install(f.env, f.request), /input does not match/);
  assert.equal(f.requests.length, 1);
  await assertUnpublished(f.env);
});

test('download failure leaves PATH and outputs untouched', async t => {
  const f = await fixture(t);
  await assert.rejects(install(f.env, async () => new Response('not found', { status: 404 })), /HTTP 404/);
  await assertUnpublished(f.env);
});

test('invalid archive fails extraction and removes its installation directory', async t => {
  const f = await fixture(t);
  const badArchive = Buffer.from('not an archive');
  const digest = createHash('sha256').update(badArchive).digest('hex');
  await assert.rejects(install(f.env, async url => url.endsWith('/checksums.txt')
    ? new Response(releaseAssets.map(asset => `${digest}  ${asset}`).join('\n'))
    : new Response(badArchive)));
  await assertUnpublished(f.env);
  assert.deepEqual((await readdir(f.env.RUNNER_TEMP)).sort(), ['output', 'path']);
});
