# ADR-003: Control plane & remote CLI

- **Status:** Parked (2026-07-25) — owner scoped to additive only; a general
  add/remove-any-provider control plane is **out of scope for now**. The *only* runtime
  registration we build is a worker joining its own queue (ADR-001), not a general admin
  API. Kept for reference.
- **Date:** 2026-07-25
- **Depends on:** ADR-002 (registry as source of truth)

## Context

Today the `routsi` CLI only acts locally (`serve`, `install`, `token`, `certs`) and the
catalog is a static file read at boot. Owner wants: **proxy runs in one place, CLI runs
somewhere else**, and workers/providers register remotely — a control plane over the
data plane (`/v1/*`).

## Decision (proposed)

A running proxy exposes an **admin API** (mutates the ADR-002 Registry), and the `routsi`
CLI can target a **remote** proxy.

Admin API (guarded by a **dedicated admin token** — see Security):
- `GET    /admin/providers` — list providers + state.
- `POST   /admin/providers` — add a provider (kind + config).
- `DELETE /admin/providers/{name}` — remove one.

CLI, local or remote:
```
routsi provider list   --proxy https://host:8080 --token $ADMIN
routsi provider add     ...    # forward/agent/worker/router
routsi provider remove  <name> ...
```
Data plane (`/v1/chat/completions`) is unchanged. **One authoritative proxy**; the CLI is
a thin client. No multi-proxy consensus in v1.

## Alternatives considered

1. **Config file + reload signal (SIGHUP).** Simple, but not remote and not per-provider;
   edit-a-file-and-reload doesn't fit "register from another machine."
2. **Full multi-node control plane (raft/etcd).** Massive over-scope for a single binary.
3. **Only dynamic worker registration (ADR-001), no general admin API.** Covers the
   headline use but not "add a forward/router remotely." Could be the v1 cut (see Babu's
   sequencing) — decide in discussion.

## Consequences

- **New privileged surface.** Whoever holds the admin token can inject a provider that
  sees routed prompts, or delete real models. Three token roles now: API (call `/v1`),
  worker (answer, ADR-001/005), admin (mutate catalog). Mutations should be **audit-logged**.
- **Persistence:** runtime-added durable providers either write back to a state file or
  are ephemeral. Proposal: **workers ephemeral** (they re-register), **forwards/routers
  optionally persisted** to `~/.config/routsi/providers.json`.
- Registry concurrency (ADR-002) is a prerequisite.

## Open questions

1. Ship the **general admin API now**, or only worker-registration (ADR-001) first and
   generalize later? (Babu leans: worker-first.)
2. **Persist** runtime changes or ephemeral-only?
3. Admin token: new `auth.admin_tokens_env`, or mTLS-client-cert-only for admin?
4. Any **RBAC** (who can add what), or single all-powerful admin token for v1?
