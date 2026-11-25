import { createHash } from 'node:crypto';
import { execFile } from 'node:child_process';
import { appendFile, chmod, lstat, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { promisify } from 'node:util';

const execute = promisify(execFile);
const releaseBase = 'https://github.com/docker/sandbox-kit-spec/releases/download';
const platforms = new Map([['Linux', 'linux'], ['macOS', 'darwin'], ['Windows', 'windows']]);
const architectures = new Map([['X64', 'amd64'], ['ARM64', 'arm64']]);
const exactVersion = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

function releaseTarget(env) {
  const version = (env.INPUT_VERSION ?? '').trim();
  if (!exactVersion.test(version)) {
    throw new Error('version must be an exact release version, such as v3.0.0-m.13');
  }
  const sha256 = (env.INPUT_SHA256 ?? '').trim().toLowerCase();
  if (sha256 && !/^[a-f0-9]{64}$/.test(sha256)) {
    throw new Error('sha256 must contain exactly 64 hexadecimal characters');
  }
  // Runner metadata describes the target CPU even if a tool on an ARM64
  // runner happens to execute through x64 emulation.
  const os = platforms.get(env.RUNNER_OS);
  const arch = architectures.get(env.RUNNER_ARCH);
  if (!os || !arch) {
    throw new Error('supported runners are Linux, macOS, or Windows on X64 or ARM64');
  }
  const number = version.replace(/^v/, '');
  return {
    version: `v${number}`,
    archive: `kit-tck_${number}_${os}_${arch}.${os === 'windows' ? 'zip' : 'tar.gz'}`,
    executable: os === 'windows' ? 'kit-tck.exe' : 'kit-tck',
    windows: os === 'windows',
    sha256,
  };
}

async function download(url, request) {
  const response = await request(url, { signal: AbortSignal.timeout(60_000) });
  if (!response.ok) {
    throw new Error(`download failed with HTTP ${response.status}: ${url}`);
  }
  return Buffer.from(await response.arrayBuffer());
}

function releaseChecksum(text, archive) {
  const entries = text.split(/\r?\n/).map(line => line.trim().split(/\s+/))
    .filter(fields => fields[1]?.replace(/^\*/, '') === archive);
  if (entries.length !== 1 || entries[0].length !== 2 || !/^[a-fA-F0-9]{64}$/.test(entries[0][0])) {
    throw new Error(`checksums.txt must contain exactly one valid SHA256 entry for ${archive}`);
  }
  return entries[0][0].toLowerCase();
}

// The fetch boundary is injectable so tests can supply corrupt release bytes
// while exercising the same checksum, extraction, and PATH publication path.
export async function install(env, request = globalThis.fetch) {
  const target = releaseTarget(env);
  for (const name of ['RUNNER_TEMP', 'GITHUB_PATH', 'GITHUB_OUTPUT']) {
    if (!env[name] || /[\r\n]/.test(env[name])) {
      throw new Error(`${name} must name a runner-provided path without line breaks`);
    }
  }
  const base = `${releaseBase}/${encodeURIComponent(target.version)}`;
  const checksums = await download(`${base}/checksums.txt`, request);
  const expected = releaseChecksum(checksums.toString('utf8'), target.archive);
  // The caller pin is additive. A changed release and matching checksum file
  // cannot silently replace bytes a caller previously approved.
  if (target.sha256 && target.sha256 !== expected) {
    throw new Error('sha256 input does not match the release checksum');
  }
  const archive = await download(`${base}/${encodeURIComponent(target.archive)}`, request);
  const actual = createHash('sha256').update(archive).digest('hex');
  if (actual !== expected) {
    throw new Error(`SHA256 mismatch for ${target.archive}`);
  }

  // Separate installations avoid reusing partially extracted or stale files.
  const directory = await mkdtemp(join(env.RUNNER_TEMP, 'setup-tck-'));
  try {
    const bin = join(directory, 'bin');
    await mkdir(bin);
    const archivePath = join(directory, target.archive);
    await writeFile(archivePath, archive);
    // Git for Windows may put GNU tar first on PATH. Windows' own bsdtar
    // supports the ZIP archives GoReleaser publishes for Windows.
    const tar = target.windows ? join(env.SystemRoot ?? 'C:\\Windows', 'System32', 'tar.exe') : 'tar';
    await execute(tar, ['-xf', archivePath, '-C', bin, target.executable], { timeout: 60_000 });
    const executable = join(bin, target.executable);
    if (!(await lstat(executable)).isFile()) {
      throw new Error('release archive must contain a regular kit-tck executable');
    }
    if (!target.windows) {
      await chmod(executable, 0o755);
    }
    await rm(archivePath);
    // Consumers must never see a PATH entry for bytes that failed verification
    // or extraction. The output paths contain no caller-provided release text.
    await appendFile(env.GITHUB_PATH, `${bin}\n`);
    await appendFile(env.GITHUB_OUTPUT, `version=${target.version}\npath=${executable}\n`);
    return { version: target.version, path: executable };
  } catch (error) {
    await rm(directory, { recursive: true, force: true });
    throw error;
  }
}
