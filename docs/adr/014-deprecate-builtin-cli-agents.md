# ADR-014 — Deprecate the built-in CLI-agent types

Status: **Accepted** (owner-directed, 2026-08-05). Supersedes the CLI-agent portion of
ADR-011; ADR-011 Phase A's tool emulation survives as a *mode* of the adapter contract.

## Context

routsi ships four vendor-specific model types — `devin`, `codex`, `copilot`, `claude`
(`internal/backend/cliagent.go`, `devin.go`, `toolemu.go`, plus their branches in
`config.validate`, `server.New` and `internal/discovery`). Together, ~590 non-test lines
plus their tests.

Two findings from the 2026-08-05 competitive review make that investment a mistake:

1. **Someone else does it better.** ccproxy-api reaches Claude/Codex/Copilot through
   `claude-code-sdk` and provider OAuth, giving native tool calling, MCP servers, real
   `allowed_tools`/`disallowed_tools` permissions, and both OpenAI and Anthropic surfaces.
   routsi's equivalent is a `-p` one-shot with the transcript re-rendered every turn, no
   session mapping on codex/copilot, streaming faked off `Complete`, and tool calling by
   fenced-JSON emulation that cost three spike rounds to prove. We are second-best at
   something that is not our thesis.
2. **ADR-013 makes them unnecessary.** Every one of the four is expressible as a `command`
   adapter — the same process invocation, in a script the user owns, without a Go type.

Keeping them costs a release per upstream flag change, keeps vendor names in core, and
crowds out the inbound worker/queue direction that no competitor has.

## Decision

Deprecate `type: devin | codex | copilot | claude`. They are **re-expressed as shipped
adapter scripts** under `examples/adapters/`, and the documented route to a
subscription-backed Claude/Codex/Copilot becomes `type: forward` pointed at ccproxy-api
(or any OpenAI-compatible bridge).

Deprecation is staged — existing configs must not break:

1. **Now.** ADR-013 `type: command` ships. The four types keep working and log a one-line
   deprecation warning at startup naming the adapter replacement. README/docs stop
   presenting them as the way to reach an agent.
2. **Next minor.** Shipped adapter scripts cover all four with parity (including devin's
   per-conversation session mapping, which a stateless wrapper cannot yet reproduce — that
   is the gating item, tracked as ADR-013's open question on session handles).
3. **Later, on the owner's call.** Types removed; `internal/backend/cliagent.go`,
   `devin.go` and the CLI branches of `internal/discovery` go with them. `toolemu.go`
   stays — it becomes the `tools: emulated` mode of the adapter contract, which is where
   ADR-011 Phase A's value actually lives.

`config.CLIAgent(t)` remains the single predicate for "is this a deprecated CLI type", so
step 3 is a small, mechanical change.

## Alternatives considered

- **Remove them immediately.** Rejected: breaks live configs, and devin's session mapping
  has no adapter equivalent yet. Deprecate-then-remove is the additive convention this
  repo has used since ADR-001.
- **Keep them and invest.** Rejected: it is a race against ccproxy-api's SDK path that we
  would be running with worse tools (subprocess vs SDK) for a use case that is not the
  product's thesis.
- **Keep only `claude`.** Rejected as arbitrary — it is the one ccproxy covers best.

## Consequences

- Core stops carrying vendor names. Adding an agent stops needing a routsi release.
- ~590 lines leave the binary at step 3; `internal/discovery`'s known-models table
  (`known-models.json`) goes with the CLI branch, shrinking startup work.
- Users who want Claude/Codex/Copilot get a *better* result than routsi gave them, via a
  project that specializes in it. routsi's README recommends it explicitly — pointing at a
  better tool for a job we do not want costs nothing and buys credibility.
- The tool-calling support matrix changes: emulation becomes an adapter mode rather than a
  per-type behaviour, so it applies to any adapter, not just the four.
- Risk: an adapter script is one more file a user must have. Mitigated by shipping the
  four in `examples/adapters/` and installing them alongside the agent skill.
