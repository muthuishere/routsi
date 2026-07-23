# Research base for routsi

Four research reports (agent team, 2026-07-23) on how LLM routers are actually built:

- [routellm-deep-dive.md](routellm-deep-dive.md) — RouteLLM paper + lm-sys implementation
  (win-rate predictor + threshold α, matrix-factorization router, OpenAI-compatible server).
- [cascades-frugalgpt.md](cascades-frugalgpt.md) — FrugalGPT / AutoMix / cascade line, and
  why cascading is incompatible with a streaming proxy.
- [academic-landscape.md](academic-landscape.md) — survey taxonomy, RouterBench, UniRoute,
  IRT-Router, LLMRank, DialRouter, vLLM Semantic Router; multi-turn routing gap.
- [production-routers.md](production-routers.md) — OpenRouter, NotDiamond, Martian, Unify,
  LiteLLM, Portkey, Cloudflare AI Gateway, GPT-5 router; API-design patterns.
- [conversation-routing.md](conversation-routing.md) — within-conversation routing:
  verdict on our design, audit defects D1–D7, SAAR/CRM policies, switch economics,
  compaction + handoff plan (2026-07-23).
- [practitioner-signals.md](practitioner-signals.md) — HN + Reddit field reports (16 threads,
  2026-07-23): router security/trust, provider variance, cost-control failures, routing
  skepticism, gateway ops pain. The delta vs the academic/vendor picture.

## Consolidated conclusions (feed ADR-001..005)

1. **Pre-request routing, never mid-stream.** Cascades sink the cheap model's full
   generation cost before deciding (arXiv 2605.06350 — an embedding router beats optimal
   cascades on 4/5 benchmarks), SSE has no token-retraction primitive, and every
   production router commits before the first upstream byte. Decide on the request head,
   rewrite the `model` field, stream bytes through untouched (vLLM Semantic Router's
   Envoy/ExtProc split proves the shape).
2. **Keep the router simple and pluggable.** RouterBench: KNN over small embeddings ≈
   learned routers; no 2024 router beat the convex-hull baseline. v1 = rules/heuristics +
   optional embedding-KNN scorer behind one interface; learned (MF/BERT) routers later.
   Hot path must be a cheap classifier — never an LLM call — with graceful degradation to
   a default model on scorer failure.
3. **Stickiness: pin on turn 1, escalate-only switching.** OpenRouter's recipe: explicit
   `session_id` else conversation-prefix hash; TTL cache (~5 min inactivity); pin model +
   provider. vLLM SR issue #1439 documents the bug of classifying only the last message.
   DialRouter prices model switches (full-history re-encode, lost KV cache) — never
   downgrade mid-conversation; allow cheap→strong escalation only past a difficulty
   threshold.
4. **Two routing layers**: model selection (which logical model) separate from
   provider/deployment selection (which upstream) — OpenRouter/LiteLLM/Portkey all do this.
5. **Expose routing as a virtual model name** (`auto`) on the unchanged OpenAI API; echo
   the chosen model in the response `model` field + an `X-Selected-Model` header. Steering
   knobs the market converged on: scalar cost/quality dial + `allowed_models` wildcards.
6. **Reliability kit (LiteLLM shapes)**: per-upstream cooldown, backoff-retry on 429 only,
   priority tiers, per-error-class fallbacks, pre-call context-window fit check.
7. **Calibrate thresholds from logged traffic** (RouteLLM's budget-% → α calibration);
   log per-request router scores so recalibration and offline judges (escalation as a
   feedback loop, not an inline hop) are possible later.
8. **Eval harness**: adopt RouteLLM's PGR / APGR / CPT metrics over RouterBench-style data.
