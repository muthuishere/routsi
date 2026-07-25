#!/usr/bin/env node
'use strict';

// Best-effort prefetch. This is NOT relied upon to guarantee the binary —
// npm/launcher.js does that on every `routsi` invocation (see
// npm/resolve-binary.js). This script only tries to warm the cache at
// install time so the first `routsi` run doesn't pay the download latency,
// and it must NEVER fail `npm install` — that would break under
// `--ignore-scripts`/`allow-scripts` blocking or a flaky network, both of
// which are fine: the launcher self-heals on first run regardless.

const { ensureBinary } = require('../npm/resolve-binary');

ensureBinary({ quiet: false })
  .then((binPath) => {
    console.log('routsi postinstall: binary ready at ' + binPath);
  })
  .catch((err) => {
    console.warn(
      'routsi postinstall: could not prefetch the native binary (' +
        (err && err.message ? err.message : String(err)) +
        ').'
    );
    console.warn('routsi: it will be fetched automatically on first `routsi` run instead.');
  })
  .finally(() => {
    process.exit(0);
  });
