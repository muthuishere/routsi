# ADR-007: External, user-writable decider for `auto` routing

- **Status:** Accepted (2026-07-25)
- **Date:** 2026-07-25
- **Deciders:** owner + routsi

## Context

The built-in `Rules` scorer (`internal/router`) classifies a request into
low/medium/high with a fixed heuristic (code fences, keyword hints, char
counts). It's deliberately dumb and fast, but it's also fixed — a user who
wants their own routing brain (a learned classifier, a call to another model,
a domain-specific heuristic) currently has to fork routsi.

## Decision

Add an optional **external decider**: a subprocess routsi shells out to per
`auto` request. Configured via a new top-level `decider:` config block:

```yaml
decider:
  command: "node decider.js"   # optional; any executable/runtime, run via `sh -c`
  timeout: 3s                  # optional, default 3s
  cwd: ""                      # optional, default process cwd
```

When `decider.command` is empty, routing is byte-for-byte the built-in
`Rules` — nothing changes for existing configs.

`router.External` implements the existing `Router` interface unchanged
(`Pick(req *api.ChatRequest) string`) so wiring is a one-line swap in
`cmd/routsi/main.go`. Per request it:

1. Marshals a JSON request to the decider's stdin:
   ```json
   {
     "model": "auto",
     "conversation_id": "…",
     "messages": [{"role":"user","content":"…"}],
     "levels": ["low","medium","high"],
     "tiers": {"low":"gpt-cheap","high":"gpt-strong"}
   }
   ```
   (`tiers` is the proxy's global `tiers:` map, fixed at construction — the
   resolved names `auto` can route to. `levels` is always the three level
   names.)
2. Runs the command (`sh -c "<command>"`) with a timeout, capturing stdout.
3. Parses `{"level": "low|medium|high"}` from stdout.
4. On ANY failure — spawn error, timeout, non-zero exit, empty/malformed
   output, unknown level — falls back to the built-in `Rules` decision for
   that request and logs a one-line warning (never the prompt text). The
   proxy must never fail a request because a decider misbehaves.

### `{"model": "..."}` extension — not implemented

The brief allowed the decider to optionally name a concrete model directly.
Skipped: honoring it would mean `server.resolve` special-cases the decider's
raw output instead of just consuming a level through the existing
`Router.Pick() string` contract, which is invasive for what's meant to be a
drop-in swap. The level contract already lets a decider point every level at
whatever member it wants via the group's own `tiers:`/`levels:` map, which
covers the practical case. Revisit if a real use case needs it.

## Consequences

- **Per-request spawn latency.** v1 spawns a fresh process on every `auto`
  request — fine for scripts with sub-second startup (Node, Python), a real
  cost for slow interpreters or JVM-style startup. A persistent
  process/socket protocol is the natural v2 if this matters in practice.
- **No new failure mode for the client.** The fail-safe fallback means a
  broken or slow decider degrades to today's behavior (Rules), not an error
  response.
- **Examples ship in `examples/`** (`decider.js`, `decider.py`) as copyable
  starting points, documented in `examples/README.md`.

## Alternatives considered

- **In-process plugin (Go plugin / cgo)** — rejected: language-locked to Go,
  fragile across Go versions, and defeats "any executable/runtime" reach.
- **HTTP callout to a user-run service** — more efficient (persistent
  process, no spawn cost) but adds a network dependency and a second thing to
  keep running; subprocess-per-request is the simplest thing that works and
  matches the CLI-agent backends' existing shell-out pattern in this repo.

## Open questions

- Should the decider be able to see recent sticky-pin state (already
  escalated once this conversation)? Not needed yet — level + tiers is
  enough signal for a first cut.
