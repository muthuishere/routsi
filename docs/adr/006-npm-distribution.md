# 006 — npm distribution via postinstall-fetched binaries

Status: Accepted

## Context

routsi is a single Go binary; distribution today is "build from source" or a
hand-copied `~/.local/bin/routsi`. Most consumers (JS/TS-heavy teams, `npx`-style
tooling) reach for `npm install -g <tool>` and don't want to build from source.
The established pattern for shipping a native binary through npm — used by esbuild,
swc, turbo, etc. — is: a tiny npm package whose `postinstall` downloads the
platform-matching prebuilt binary from GitHub Releases, and a thin JS launcher that
`exec`s it. That avoids per-platform `optionalDependencies` packages (simpler, at the
cost of a network fetch at install time instead of pure npm-registry resolution).

## Decision

- Publish a single npm package, name **`routsi`** (confirmed available on the
  registry 2026-07-25).
- **Self-healing launcher, postinstall is optional** (revised 2026-07-25 — npm is
  trending toward blocking install scripts by default, e.g. `allow-scripts`; a
  `postinstall`-only design would leave those installs with no binary at all).
  The fetch/extract logic lives once in `npm/resolve-binary.js` (zero deps — only
  `https`/`fs`/`path`/`os`/`crypto`/`child_process`), exporting `assetName`,
  `binaryPath`, `version`, and `ensureBinary({quiet})`. `ensureBinary` returns the
  path to `npm/bin/routsi[.exe]` immediately if it's already present and non-empty;
  otherwise it downloads the matching release asset for `version()` (package.json's
  version, overridable via `ROUTSI_BINARY_VERSION` for testing against an
  already-released version), verifies it against the release's `checksums.txt`
  (best-effort — a missing checksums file warns but doesn't block, since older
  releases may predate it), unpacks it (`tar`/`unzip`, both present on
  macOS/Linux/modern Windows), and places the binary at `npm/bin/routsi[.exe]` with
  mode `0755`.
  - `package.json` `bin.routsi` points at `npm/launcher.js`, which `await
    ensureBinary({quiet:false})`s before every `routsi` invocation — so the binary
    is guaranteed on first run regardless of whether postinstall ran, was blocked,
    or failed. It then execs the binary, forwarding argv/stdio/exit code (and
    SIGINT/SIGTERM).
  - `postinstall` (`scripts/postinstall.js`) is now a thin **best-effort prefetch**:
    it calls `ensureBinary({quiet:false})` to warm the cache at install time (so the
    first `routsi` run doesn't pay download latency), but catches every error and
    always `process.exit(0)`s — a network hiccup or a blocked install script can
    never fail `npm install`.
  - Re-running either path is a no-op once the binary exists.
- **Asset naming contract** (the load-bearing string shared between
  `scripts/postinstall.js` and `.github/workflows/release.yml`):
  ```
  routsi_<os>_<arch>.tar.gz     # os in {darwin, linux}, arch in {amd64, arm64}
  routsi_windows_<arch>.zip     # arch in {amd64, arm64}
  ```
  Archives are flat — just the binary (`routsi` or `routsi.exe`), no top-level
  directory. Download URL:
  `https://github.com/muthuishere/routsi/releases/download/v<version>/<asset>`.
- `.github/workflows/release.yml` triggers on `v*` tags: builds the 5-way matrix
  (darwin/amd64, darwin/arm64, linux/amd64, linux/arm64, windows/amd64) with
  `-ldflags "-s -w -X main.version=<tag>"`, generates `checksums.txt`, publishes a
  GitHub Release via `softprops/action-gh-release`, then a follow-on job bumps
  `package.json`'s version to match the tag and runs `npm publish --access public`
  using the `NPM_TOKEN` repo secret. A missing `NPM_TOKEN` logs a workflow warning
  and exits 0 rather than failing the whole run — release-asset publishing (the part
  every user's postinstall depends on) must not be blocked by npm credentials not
  being wired up yet.
- The downloaded/launcher paths live under `npm/` specifically so they never collide
  with the Go build's own `task build` output at `<repo-root>/bin/routsi` — a
  contributor building from source and a user installing via npm never touch the
  same path.

## Alternatives considered

- **`optionalDependencies` per-platform packages** (esbuild's newer approach): avoids
  a postinstall network fetch, resolves entirely through the npm registry/lockfile.
  Rejected for now — more packages to publish and version in lockstep, more ceremony
  for a v0.1 first cut. Can be revisited if postinstall-time flakiness becomes a real
  complaint.
- **Vendor binaries directly in the npm tarball**: bloats the package with 5
  platforms' binaries every install regardless of which one is needed, and requires
  publishing after every build across every OS in one go. Rejected.
- **`npx`-only (no global install)**: doesn't fit the CLI/service use case (`routsi
  serve`, `routsi install` sets up a long-running launchd/systemd service).

## Consequences

- Installing `routsi` via npm requires network access at install time and a GitHub
  Release matching the installed package's version to already exist — the first
  `npm publish` must happen *after* the matching `vX.Y.Z` tag's release job has
  uploaded assets (the workflow's job ordering enforces this: `publish-npm` depends
  on `release`).
- Checksum verification is best-effort by design (skips rather than blocks when
  `checksums.txt` is unreachable) — a determined MITM during install could swap the
  binary if checksums.txt is also unreachable; full supply-chain hardening (e.g.
  Sigstore/cosign signing) is out of scope for v0.1.
- Offline installs (`--offline`/`--prefer-offline` without a cached binary) fail
  loudly with a pointer to manual download, rather than silently producing a broken
  `routsi` command — that check now happens in the launcher (on first run), not
  just at install time.
- Survives `allow-scripts`-style install-script blocking: with postinstall skipped
  entirely, the first `routsi` invocation still self-heals (one-time "fetching
  native binary..." line to stderr), so the npm distribution model keeps working
  without relying on install scripts running at all.

## Open questions

- Whether to add `optionalDependencies`-per-platform later to drop the postinstall
  network dependency.
- Whether to sign release binaries (cosign/Sigstore) once the npm channel sees real
  usage.
