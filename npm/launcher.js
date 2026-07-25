#!/usr/bin/env node
'use strict';

// Thin launcher installed as the `routsi` bin entry. Execs the native binary
// downloaded by scripts/postinstall.js into npm/bin/, passing argv/stdio/exit
// code straight through. Kept separate from the Go build's own `bin/routsi`
// output so `task build` (repo checkout) and this npm package never collide.

const path = require('path');
const fs = require('fs');
const { spawnSync } = require('child_process');

const binName = process.platform === 'win32' ? 'routsi.exe' : 'routsi';
const binPath = path.join(__dirname, 'bin', binName);

if (!fs.existsSync(binPath)) {
  console.error(
    `routsi: native binary not found at ${binPath}.\n` +
    'The postinstall step may have failed or been skipped (offline install?).\n' +
    'Try: npm install -g routsi --force\n' +
    'Or download a binary manually from https://github.com/muthuishere/routsi/releases'
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), { stdio: 'inherit' });

if (result.error) {
  console.error('routsi: failed to launch native binary: ' + result.error.message);
  process.exit(1);
}

process.exit(result.status === null ? 1 : result.status);
