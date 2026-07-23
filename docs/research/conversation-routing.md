# Within-conversation routing — research synthesis (2026-07-23)

Three-agent sweep: multi-turn literature, production practice, and an audit of our own
implementation. Verdict on the shipped design and the upgrade path.

## Verdict on the current design

**Right:** pre-request routing, sticky-by-conversation, escalate-first bias, disclosure
via X-Selected-Model, pin TTL ≈ provider cache TTL. Every production system that
silently downgraded mid-conversation (GPT-5 router Aug-2025, Claude Code Opus→Sonnet
fallback) triggered user revolt; LiteLLM (affinity TTL 3600s) and OpenRouter (5 min)
both chose pin-don't-downgrade. Two-tier (cheap default + one escalation target) is
also right: multi-stage chains add nothing over the best pair (arXiv 2605.06350).

**Wrong / was wrong:**
- ~~Cumulative-length signal~~ (FIXED): summing all history made every long
  client-managed conversation classify `high` forever — escalate-only then ratchets
  cost permanently. Router now classifies the current turn only. Length is a weak
  proxy regardless: short queries route easiest (10.1% misroute), 68+ tokens →23.3%
  (Switchcraft, arXiv 2605.07112).
- ~~Devin fingerprint leak~~ (FIXED, critical): fp- ids reached Devin session mapping —
  two users with identical first messages could share a session.
- "Never downgrade, ever" is too crude: vLLM SAAR showed −79% switches / −78.7% cost
  with *reset boundaries* where switching (incl. downgrade) is safe: idle > ~300s
  (cache already cold), topic/decision drift, momentum decay. Hard locks: never
  mid-tool-loop, never while provider-side state is live.
- Memory handoff gap (OPEN, high): on group escalation with proxy-managed memory,
  the new member's toolnexus store / Devin session is empty — history silently lost
  (audit D2/D3). Codex/copilot are amnesiac in explicit-id mode (D5); system messages
  dropped in proxy-managed mode (D6); sticky-key vs memory-key coordinate mismatch (R1).

## Key evidence

1. **Difficulty oscillates; long histories poison classification.** ≥9-turn
   conversations: 42.5% misroute vs 14–17% short (2605.07112). Feed the classifier a
   *decaying aggregate* of per-turn scores, not last-message-only (the #1439 bug) and
   not raw full history.
2. **Best escalation trigger** is calibrated model-side uncertainty (logprob margin +
   isotonic calibration — UCCI, arXiv 2605.18796): −31% cost at F1 0.91. Keywords are
   the floor, verbalized confidence the worst.
3. **CRM asymmetric filter** (vllm-sr #1458) is the concrete policy: momentum
   `m = α·c_t + (1−α)·m` with fast attack (α≈0.3, instant upward), slow release
   (0.9 decay) — escalate in one turn, de-escalate only after several easy turns.
   Anti-thrash: penalize per-session switch count (SAAR weight 0.04).
4. **Switch cost is one full uncached prefill.** 100K-token history: ~$0.03 cached
   turn vs ~10–12× on switch (Anthropic write premium 1.25×; DeepSeek hit/miss ≈50×).
   Caches die at ~5–10 min idle — pins should too (ours: 10 min, refresh-on-hit ✓).
5. **Handoff = full-history replay.** Industry-universal (GPT-5 router, Swarm, Claude
   Code); no state transfer exists. Compression compounds errors across turns
   (C-DIC, arXiv 2606.12411). Summarize-then-handoff is for window overflow and
   downgrade boundaries only — keep recent turns verbatim, summarize old ones.
6. **Routing input is adversarial** (PROMISQROUTE): phrasing may trigger escalation,
   never a downgrade below a floor.

## Upgrade plan (priority order)

1. ~~Router: current-turn signals only~~ (done) · ~~Devin fp- guard~~ (done).
2. **Escalation-with-memory rule**: while a conversation has proxy-managed memory,
   pin beats escalation (one `if` in resolveGroup) UNTIL handoff exists; then:
   handoff = replay stored transcript into the new member (seed toolnexus store /
   render into first prompt for CLI agents).
3. **Compaction** (owner-requested): per-conversation token budget
   (`compact_after`); on breach, summarize old turns via the group's low member,
   keep last N verbatim, write back to the store. Same summary doubles as downgrade
   /overflow handoff brief. Never rewrite client-managed history.
4. **Momentum router**: score each turn (rules now, calibrated-uncertainty later),
   fold into CRM-style asymmetric momentum kept in the sticky entry; add reset
   boundaries (idle>TTL already implicit, topic-shift later) and switch-count
   anti-thrash. This legitimizes bounded de-escalation.
5. **Unify coordinate systems** (audit R1): backend memory keyed convID#group like
   pins; document in CLAUDE.md contract.
6. Codex/copilot proxy-managed transcripts (D5), system-message passthrough (D6),
   TTL-expiry downgrade clamp (D7).

Full agent reports: production practice + literature are inline in this file's git
history / session log; audit findings D1–D7, R1–R4 enumerated above.
