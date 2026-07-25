'use strict';

// Shared, zero-dependency binary resolution/fetch logic for routsi's npm
// distribution (ADR 006). Used by both scripts/postinstall.js (best-effort
// prefetch) and npm/launcher.js (guarantees the binary before every run).
//
// Asset naming scheme (MUST match .github/workflows/release.yml):
//   routsi_<os>_<arch>.tar.gz   (darwin/linux)
//   routsi_windows_<arch>.zip  (windows)
// where <os> in {darwin, linux, windows}, <arch> in {amd64, arm64}.
// Release URL: https://github.com/muthuishere/routsi/releases/download/v<version>/<asset>

const https = require('https');
const fs = require('fs');
const path = require('path');
const os = require('os');
const crypto = require('crypto');
const { execFileSync } = require('child_process');

const REPO = 'muthuishere/routsi';
const PKG_ROOT = path.join(__dirname, '..');
const BIN_DIR = path.join(PKG_ROOT, 'npm', 'bin');

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

// Pure mapping function — unit-tested by scripts/postinstall.test.js.
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

function binaryPath() {
  return path.join(BIN_DIR, binaryName(process.platform));
}

function releaseUrl(version, asset) {
  return `https://github.com/${REPO}/releases/download/v${version}/${asset}`;
}

// version() reads package.json's version, but allows an env override for
// testing against an already-released version before a new one is cut.
function version() {
  if (process.env.ROUTSI_BINARY_VERSION) return process.env.ROUTSI_BINARY_VERSION;
  const pkg = require(path.join(PKG_ROOT, 'package.json'));
  return pkg.version;
}

function isOffline() {
  if (process.env.npm_config_offline === 'true') return true;
  if (process.env.npm_config_prefer_offline === 'true' && process.env.ROUTSI_ALLOW_STALE) return true;
  return false;
}

// GET with a bounded number of redirect hops. Returns a Buffer via callback.
function fetchBuffer(url, redirectsLeft, cb) {
  const req = https.get(url, { headers: { 'User-Agent': 'routsi-npm-launcher' } }, (res) => {
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
async function verifyChecksum(ver, assetBuf, asset) {
  let checksumsBuf;
  try {
    checksumsBuf = await fetchBufferAsync(releaseUrl(ver, 'checksums.txt'));
  } catch (err) {
    console.warn('routsi: could not fetch checksums.txt (' + err.message + '), skipping verification');
    return;
  }
  const text = checksumsBuf.toString('utf8');
  const line = text.split('\n').find((l) => l.trim().endsWith(asset));
  if (!line) {
    console.warn('routsi: no checksum entry for ' + asset + ', skipping verification');
    return;
  }
  const expected = line.trim().split(/\s+/)[0].toLowerCase();
  const actual = crypto.createHash('sha256').update(assetBuf).digest('hex');
  if (expected !== actual) {
    throw new Error(`checksum mismatch for ${asset}: expected ${expected}, got ${actual}`);
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

// ensureBinary: returns the absolute path to a working npm/bin/routsi[.exe],
// downloading + extracting the release asset if it isn't already there.
// Never touches npm install lifecycle semantics — callers decide what to do
// with a thrown error.
async function ensureBinary(opts) {
  opts = opts || {};
  const quiet = !!opts.quiet;
  const platform = process.platform;
  const arch = process.arch;
  const finalBinPath = binaryPath();

  if (fs.existsSync(finalBinPath) && fs.statSync(finalBinPath).size > 0) {
    return finalBinPath;
  }

  const ver = version();

  if (isOffline()) {
    throw new Error(
      'offline mode requested (npm_config_offline) and no binary present at ' + finalBinPath
    );
  }

  let asset;
  try {
    asset = assetName(platform, arch, ver);
  } catch (err) {
    throw new Error(
      `${err.message}. routsi ships prebuilt binaries for darwin/linux/windows on amd64/arm64.\n` +
      `Build from source instead: https://github.com/${REPO}`
    );
  }

  if (!quiet) {
    console.error(`routsi: fetching native binary v${ver} for ${platform}-${arch}...`);
  }

  const url = releaseUrl(ver, asset);
  let assetBuf;
  try {
    assetBuf = await fetchBufferAsync(url);
  } catch (err) {
    throw new Error(
      `failed to download ${url}: ${err.message}\n` +
      `See release assets at https://github.com/${REPO}/releases — you can also\n` +
      'download the right binary by hand and place it on your PATH.'
    );
  }

  await verifyChecksum(ver, assetBuf, asset);

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
      throw new Error(`downloaded archive ${asset} did not contain expected binary ${wantName}`);
    }

    fs.mkdirSync(BIN_DIR, { recursive: true });
    fs.copyFileSync(extracted, finalBinPath);
    fs.chmodSync(finalBinPath, 0o755);
    return finalBinPath;
  } finally {
    fs.rmSync(extractDir, { recursive: true, force: true });
  }
}

module.exports = {
  assetName,
  mapPlatform,
  mapArch,
  binaryName,
  binaryPath,
  releaseUrl,
  version,
  ensureBinary,
};
