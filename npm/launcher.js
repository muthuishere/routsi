#!/usr/bin/env node
'use strict';

// Thin launcher installed as the `routsi` bin entry. Guarantees the native
// binary is present (self-healing — downloads it on demand via
// npm/resolve-binary.js if postinstall didn't run or was blocked, e.g. by
// `allow-scripts`), then execs it, passing argv/stdio/exit code straight
// through. Kept separate from the Go build's own `bin/routsi` output so
// `task build` (repo checkout) and this npm package never collide.

const { spawn } = require('child_process');
const { ensureBinary } = require('./resolve-binary');

(async function main() {
  let binPath;
  try {
    binPath = await ensureBinary({ quiet: false });
  } catch (err) {
    console.error('routsi: ' + (err && err.message ? err.message : String(err)));
    process.exit(1);
    return;
  }

  const child = spawn(binPath, process.argv.slice(2), { stdio: 'inherit' });

  const forwardSignal = (signal) => {
    if (child.killed === false) {
      try {
        child.kill(signal);
      } catch (_) {
        // ignore — child may have already exited
      }
    }
  };
  process.on('SIGINT', forwardSignal);
  process.on('SIGTERM', forwardSignal);

  child.on('error', (err) => {
    console.error('routsi: failed to launch native binary: ' + err.message);
    process.exit(1);
  });

  child.on('exit', (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code === null ? 1 : code);
  });
})();
