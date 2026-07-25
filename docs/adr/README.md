# Architecture Decision Records

One decision per file. Status: Proposed → Accepted → Superseded. No code embodying a
Proposed decision is committed (see CLAUDE.md workflow).

## The "everything is a provider" arc (under huddle discussion, 2026-07-25)

Reframe: **the `Backend` interface already unifies what answers** (forward, agent,
router, custom). The work is not a rewrite — it's adding a runtime registry, a control
plane, and status around that interface. These ADRs split that work:

| ADR | Title | Role | Status |
|-----|-------|------|--------|
| [002](002-unified-provider-model.md) | Unified provider model | the spine — everything is a registered Provider with a kind + state | Proposed |
| [001](001-pull-worker-queue.md) | Pull-worker queue | first new provider *kind*: remote agent answers via broker | Proposed |
| [004](004-provider-status-health.md) | Provider status & health | makes providers trustworthy (state, heartbeat, failure surfacing) | Proposed |
| [003](003-control-plane-remote-cli.md) | Control plane & remote CLI | distribution: add/remove providers on a running proxy from anywhere | Proposed |
| [005](005-agent-skill-worker.md) | Agent-skill worker | the loop that turns opencode/codex/claude into a provider | Proposed |

Likely **accept + build order**: 002 → 001 → 004 → 003 → 005 (Maya's sequencing;
Babu's "prove the worker case before generalizing").

Grounding evidence: [`docs/research/`](../research/). Spikes: [`docs/spikes/`](../spikes/).
