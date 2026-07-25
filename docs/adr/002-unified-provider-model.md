# ADR-002: Unified provider model ("everything is a worker")

- **Status:** Parked (2026-07-25) — owner scoped the work to **additive only, no
  architecture change**: just add the pull-worker capability + CLI + agent skill. The
  registry/refactor here is **not being done now**. Kept for reference if the fabric
  vision returns later. The pull-worker (ADR-001) is added as a plain `Backend`/config
  model type, *without* this refactor.
- **Date:** 2026-07-25
- **Supersedes intent of:** ad-hoc per-kind wiring in `server.New`

## Context

routsi already routes to many things that answer: OpenAI-style forwards, toolnexus
translators, Devin/Codex/Copilot/Claude CLIs, custom Go handlers, dynamic groups, and
(proposed) pull-worker queues. They **already share one interface** — `backend.Backend`
(`Complete`/`Stream`). But: registration is **boot-time config only**; each kind is
wired in a `switch` in `server.New`; dynamic groups and discovery bolt on separately;
nothing has a lifecycle/state.

Owner's ask: make **models, dynamic routers, and agents all "workers"** under one
concept — a distributed fabric where the proxy runs in one place and things that answer
register from anywhere.

## Decision (proposed)

Formalize a **Provider**: a named, registered entity that answers requests, with
metadata and a lifecycle state. One **Registry** is the single source of truth; config
load, discovery, dynamic registration (ADR-003), and remote workers (ADR-001) all funnel
into it. `resolve`, `/v1/models`, metrics, and the dashboard read from the Registry.

Provider **kinds** (all behind `Backend`; transport differs — keep it explicit):

| kind | transport | example |
|------|-----------|---------|
| `forward` | dial-out HTTP (sync) | OpenRouter, OpenAI |
| `translate` | dial-out HTTP via toolnexus | Anthropic/Gemini styles |
| `local-agent` | local subprocess | devin/codex/copilot/claude |
| `router` | dispatches to other providers | dynamic group / `auto` |
| `pull-worker` | remote long-poll (dial-in) | someone's opencode loop |
| `custom` | in-process Go | registered handler |

A **router is just a provider** that selects among other providers — that is the
"dynamic router as a worker" idea, already present as `type: dynamic`.

**Additive, not a replacement.** `pull-worker` is a *new* kind added alongside the
existing ones. `local-agent` (in-proxy devin/codex/copilot/claude invocation) **stays
fully supported** — an operator may run an agent locally on the proxy OR plug it in as a
remote worker, their choice per deployment. Nothing that works today is removed. (Owner
decision, 2026-07-25.)

## Alternatives considered

1. **Status quo (config-only, per-kind switch).** Works today; can't add providers at
   runtime or remotely. Rejected as the blocker for the fabric vision.
2. **Separate registries per kind.** More types, more duplication; the `Backend`
   interface already argues for one registry.
3. **Full rewrite to an actor/plugin system.** Over-engineered; the interface is enough.

## Consequences

- Registry becomes **mutable at runtime** → concurrency (RWMutex); touches `resolve`,
  `/v1/models`, discovery, metrics.
- Transport direction (dial-out vs dial-in) must stay visible per kind, or failure modes
  blur.
- Enables ADR-003 (control plane) and ADR-001 (pull-worker) to share one registration path.
- Backwards compatible: existing `models.yaml` becomes *one input* to the Registry.

## Open questions

1. Do runtime-added providers **persist** across restart, or is config the only durable
   source (workers re-register; see ADR-003)?
2. Provider **naming/identity** rules — reserve `auto`/tier names; reject collisions.
3. May a `router` provider nest other routers? (Current code rejects nested groups.)
4. Is "kind" a closed enum or an open plugin point?
