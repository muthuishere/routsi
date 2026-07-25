#!/usr/bin/env node
'use strict';

// Tiny assertion check for the pure platform->asset mapping in postinstall.js.
// Run with: node scripts/postinstall.test.js

const assert = require('assert');
const { assetName, binaryName, releaseUrl } = require('./postinstall.js');

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

console.log('\nall postinstall mapping checks passed');
