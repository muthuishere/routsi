# Architecture Decision Records

One decision per file. Status: Proposed → Accepted → Superseded/Parked. No code
embodying a Proposed decision is committed (see CLAUDE.md workflow).

## Scope (owner, 2026-07-25): additive only

**No architecture change.** Everything that works today stays. We only *add*: a way for
remote workers to join and supply answers, a CLI capability to run one, and an agent
skill so anyone can become a worker. The broader "unified registry + control plane"
refactor is **parked** — not being built now.

| ADR | Title | Status | In scope? |
|-----|-------|--------|-----------|
| [001](001-pull-worker-queue.md) | Pull-worker queue | Proposed | ✅ the broker so workers can answer |
| [005](005-agent-skill-worker.md) | Agent-skill worker | Proposed | ✅ CLI `routsi worker` + the agent skill |
| [004](004-provider-status-health.md) | Provider status & health | Proposed | ➖ **minimal slice only** — worker liveness + failure visibility for the new queue path |
| [002](002-unified-provider-model.md) | Unified provider model | **Parked** | ❌ architecture refactor — not now |
| [003](003-control-plane-remote-cli.md) | Control plane & remote CLI | **Parked** | ❌ general admin API — not now |

Live work = **001 + 005 + a minimal 004** (worker status only). The pull-worker is added
as a plain `Backend` + config `type`, plus one dynamic-register endpoint so a worker can
join without editing config — nothing else in the proxy changes.

Grounding: [`docs/research/`](../research/). Spike: [`docs/spikes/`](../spikes/).
