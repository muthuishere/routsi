#!/usr/bin/env node
'use strict';

// Zero-dependency postinstall: downloads the prebuilt routsi binary for this
// OS/arch from GitHub Releases and drops it at npm/bin/routsi[.exe].
//
// Asset naming scheme (MUST match .github/workflows/release.yml):
//   routsi_<os>_<arch>.tar.gz   (darwin/linux)
//   routsi_windows_<arch>.zip  (windows)
// where <os> in {darwin, linux, windows}, <arch> in {amd64, arm64}.
// Release URL: https://github.com/muthuishere/routsi/releases/download/v<version>/<asset>
// Each release also carries a checksums.txt (sha256sum output) covering all assets.

const https = require('https');
const fs = require('fs');
const path = require('path');
const os = require('os');
const zlib = require('zlib');
const crypto = require('crypto');
const { execFileSync } = require('child_process');

const REPO = 'muthuishere/routsi';
const PKG_ROOT = path.join(__dirname, '..');
const BIN_DIR = path.join(PKG_ROOT, 'npm', 'bin');

function pkgVersion() {
  const pkg = require(path.join(PKG_ROOT, 'package.json'));
  return pkg.version;
}

// Pure mapping function — unit-tested by scripts/postinstall.test.js.
// platform: node's os.platform() value ('darwin'|'linux'|'win32'|...)
// arch: node's os.arch() value ('x64'|'arm64'|...)
function mapPlatform(platform) {
  if (platform === 'darwin') return 'darwin';
  if (platform === 'linux') return 'linux';
  if (platform === 'win32') return 'windows';
  return null;
}

function mapArch(arch) {
  if (arch === 'x64') return 'amd64';
  if (arch === 'arm64') return 'arm64';
  return null;
}

function assetName(platform, arch, version) {
  const os_ = mapPlatform(platform);
  const arch_ = mapArch(arch);
  if (!os_ || !arch_) {
    throw new Error(`unsupported platform/arch: ${platform}/${arch}`);
  }
  const ext = os_ === 'windows' ? 'zip' : 'tar.gz';
  return `routsi_${os_}_${arch_}.${ext}`;
}

function binaryName(platform) {
  return mapPlatform(platform) === 'windows' ? 'routsi.exe' : 'routsi';
}

function releaseUrl(version, asset) {
  return `https://github.com/${REPO}/releases/download/v${version}/${asset}`;
}

function isOffline() {
  if (process.env.npm_config_offline === 'true') return true;
  if (process.env.npm_config_prefer_offline === 'true' && process.env.ROUTSI_ALLOW_STALE) return true;
  return false;
}

function fail(msg) {
  console.error('\nroutsi postinstall: ' + msg);
  console.error(
    `See release assets at https://github.com/${REPO}/releases — you can also\n` +
    'download the right binary by hand and place it on your PATH.\n'
  );
  process.exit(1);
}

function warnSkip(msg) {
  console.warn('routsi postinstall: ' + msg + ' — skipping download.');
  process.exit(0);
}

// GET with a bounded number of redirect hops. Returns a Buffer via callback.
function fetchBuffer(url, redirectsLeft, cb) {
  const req = https.get(url, { headers: { 'User-Agent': 'routsi-npm-postinstall' } }, (res) => {
    if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
      if (redirectsLeft <= 0) {
        cb(new Error('too many redirects fetching ' + url));
        return;
      }
      res.resume();
      fetchBuffer(res.headers.location, redirectsLeft - 1, cb);
      return;
    }
    if (res.statusCode !== 200) {
      res.resume();
      cb(new Error(`GET ${url} -> HTTP ${res.statusCode}`));
      return;
    }
    const chunks = [];
    res.on('data', (c) => chunks.push(c));
    res.on('end', () => cb(null, Buffer.concat(chunks)));
    res.on('error', cb);
  });
  req.on('error', cb);
}

function fetchBufferAsync(url) {
  return new Promise((resolve, reject) => {
    fetchBuffer(url, 5, (err, buf) => (err ? reject(err) : resolve(buf)));
  });
}

// Best-effort checksum verification. If checksums.txt can't be fetched (e.g.
// older release, network hiccup), we proceed without failing the install.
async function verifyChecksum(version, assetBuf, asset) {
  let checksumsBuf;
  try {
    checksumsBuf = await fetchBufferAsync(releaseUrl(version, 'checksums.txt'));
  } catch (err) {
    console.warn('routsi postinstall: could not fetch checksums.txt (' + err.message + '), skipping verification');
    return;
  }
  const text = checksumsBuf.toString('utf8');
  const line = text.split('\n').find((l) => l.trim().endsWith(asset));
  if (!line) {
    console.warn('routsi postinstall: no checksum entry for ' + asset + ', skipping verification');
    return;
  }
  const expected = line.trim().split(/\s+/)[0].toLowerCase();
  const actual = crypto.createHash('sha256').update(assetBuf).digest('hex');
  if (expected !== actual) {
    fail(`checksum mismatch for ${asset}: expected ${expected}, got ${actual}`);
  }
}

function extractTarGz(buf, destDir) {
  fs.mkdirSync(destDir, { recursive: true });
  const tmpTar = path.join(os.tmpdir(), `routsi-${Date.now()}.tar.gz`);
  fs.writeFileSync(tmpTar, buf);
  try {
    execFileSync('tar', ['-xzf', tmpTar, '-C', destDir], { stdio: 'inherit' });
  } finally {
    fs.rmSync(tmpTar, { force: true });
  }
}

function extractZip(buf, destDir) {
  fs.mkdirSync(destDir, { recursive: true });
  const tmpZip = path.join(os.tmpdir(), `routsi-${Date.now()}.zip`);
  fs.writeFileSync(tmpZip, buf);
  try {
    if (process.platform === 'win32') {
      execFileSync('powershell.exe', [
        '-NoProfile', '-Command',
        `Expand-Archive -Force -Path "${tmpZip}" -DestinationPath "${destDir}"`,
      ], { stdio: 'inherit' });
    } else {
      execFileSync('unzip', ['-o', tmpZip, '-d', destDir], { stdio: 'inherit' });
    }
  } finally {
    fs.rmSync(tmpZip, { force: true });
  }
}

// Locate the extracted binary — tarball/zip is flat (no top-level dir), but
// tolerate one either way.
function findExtractedBinary(destDir, wantName) {
  const direct = path.join(destDir, wantName);
  if (fs.existsSync(direct)) return direct;
  const entries = fs.readdirSync(destDir, { withFileTypes: true });
  for (const e of entries) {
    if (e.isDirectory()) {
      const nested = path.join(destDir, e.name, wantName);
      if (fs.existsSync(nested)) return nested;
    }
  }
  return null;
}

async function main() {
  const platform = process.platform;
  const arch = process.arch;
  const version = pkgVersion();
  const finalBinPath = path.join(BIN_DIR, binaryName(platform));

  if (fs.existsSync(finalBinPath) && fs.statSync(finalBinPath).size > 0) {
    console.log('routsi postinstall: binary already present at ' + finalBinPath + ', skipping download');
    return;
  }

  if (isOffline()) {
    warnSkip('offline mode requested (npm_config_offline)');
    return;
  }

  let asset;
  try {
    asset = assetName(platform, arch, version);
  } catch (err) {
    fail(
      `${err.message}. routsi ships prebuilt binaries for darwin/linux/windows on amd64/arm64.\n` +
      'Build from source instead: https://github.com/' + REPO
    );
    return;
  }

  const url = releaseUrl(version, asset);
  console.log(`routsi postinstall: downloading ${asset} from ${url}`);

  let assetBuf;
  try {
    assetBuf = await fetchBufferAsync(url);
  } catch (err) {
    fail(`failed to download ${url}: ${err.message}`);
    return;
  }

  await verifyChecksum(version, assetBuf, asset);

  const extractDir = fs.mkdtempSync(path.join(os.tmpdir(), 'routsi-extract-'));
  try {
    if (asset.endsWith('.zip')) {
      extractZip(assetBuf, extractDir);
    } else {
      extractTarGz(assetBuf, extractDir);
    }

    const wantName = binaryName(platform);
    const extracted = findExtractedBinary(extractDir, wantName);
    if (!extracted) {
      fail(`downloaded archive ${asset} did not contain expected binary ${wantName}`);
      return;
    }

    fs.mkdirSync(BIN_DIR, { recursive: true });
    fs.copyFileSync(extracted, finalBinPath);
    fs.chmodSync(finalBinPath, 0o755);
    console.log('routsi postinstall: installed binary at ' + finalBinPath);
  } finally {
    fs.rmSync(extractDir, { recursive: true, force: true });
  }
}

if (require.main === module) {
  main().catch((err) => fail(err && err.message ? err.message : String(err)));
}

module.exports = { assetName, mapPlatform, mapArch, binaryName, releaseUrl };
