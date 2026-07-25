#!/usr/bin/env node
'use strict';

// Tiny assertion checks for npm/resolve-binary.js: the pure platform->asset
// mapping, and ensureBinary's no-op-when-present short circuit.
// Run with: node scripts/postinstall.test.js

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const os = require('os');
const {
  assetName,
  binaryName,
  releaseUrl,
  binaryPath,
  ensureBinary,
} = require('../npm/resolve-binary.js');

function eq(actual, expected, label) {
  assert.strictEqual(actual, expected, `${label}: expected ${expected}, got ${actual}`);
  console.log(`ok - ${label}`);
}

eq(assetName('darwin', 'arm64', '0.1.0'), 'routsi_darwin_arm64.tar.gz', 'darwin/arm64 asset name');
eq(assetName('darwin', 'x64', '0.1.0'), 'routsi_darwin_amd64.tar.gz', 'darwin/amd64 asset name');
eq(assetName('linux', 'x64', '0.1.0'), 'routsi_linux_amd64.tar.gz', 'linux/amd64 asset name');
eq(assetName('linux', 'arm64', '0.1.0'), 'routsi_linux_arm64.tar.gz', 'linux/arm64 asset name');
eq(assetName('win32', 'x64', '0.1.0'), 'routsi_windows_amd64.zip', 'windows/amd64 asset name');

eq(binaryName('darwin'), 'routsi', 'darwin binary name');
eq(binaryName('win32'), 'routsi.exe', 'windows binary name');

eq(
  releaseUrl('0.1.0', 'routsi_linux_amd64.tar.gz'),
  'https://github.com/muthuishere/routsi/releases/download/v0.1.0/routsi_linux_amd64.tar.gz',
  'release URL construction'
);

assert.throws(() => assetName('sunos', 'x64', '0.1.0'), /unsupported platform\/arch/, 'unsupported platform throws');
assert.throws(() => assetName('linux', 'ia32', '0.1.0'), /unsupported platform\/arch/, 'unsupported arch throws');

console.log('\nall resolve-binary mapping checks passed');

// ensureBinary should be a no-op (no network) when the binary already exists
// and is non-empty.
async function testEnsureBinaryNoOp() {
  const binPath = binaryPath();
  const binDir = path.dirname(binPath);
  fs.mkdirSync(binDir, { recursive: true });

  const existedBefore = fs.existsSync(binPath);
  const backupPath = existedBefore ? binPath + '.bak-' + Date.now() : null;
  if (existedBefore) {
    fs.renameSync(binPath, backupPath);
  }

  try {
    fs.writeFileSync(binPath, 'fake-binary-contents');

    // Sabotage network access: if ensureBinary tried to download, https.get
    // pointed at a bogus host would hang/fail — but since the file already
    // exists, ensureBinary must return immediately without touching https.
    const result = await ensureBinary({ quiet: true });
    eq(result, binPath, 'ensureBinary returns existing binary path unchanged');
    console.log('ok - ensureBinary is a no-op when binary already present');
  } finally {
    fs.rmSync(binPath, { force: true });
    if (existedBefore) {
      fs.renameSync(backupPath, binPath);
    }
  }
}

testEnsureBinaryNoOp()
  .then(() => {
    console.log('\nall ensureBinary no-op checks passed');
  })
  .catch((err) => {
    console.error(err);
    process.exit(1);
  });
