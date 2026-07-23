# Practitioner signals (HN + Reddit, 16 threads, 2026-07-23)

Delta over [README.md](README.md) conclusions. Threads cited inline; HN = news.ycombinator.com/item?id=N.

## What's new vs our research base

1. **Routers are now an attack surface — trust is the #1 adoption blocker.** UCSB/Fuzzland bought
   28 paid + 400 free LLM routers: 9 injected malicious code into responses, 17 stole AWS canary
   creds, 1 drained an ETH wallet; 401 hijacked Codex sessions ran in YOLO mode with no human
   approval. Every router is a plaintext MITM on the response path; no provider signs responses.
   Proposed client-side defenses: fail-closed policy gate, response anomaly screening, append-only
   transparency log. ("Researchers bought 28 paid and 400 free LLM routers…", r/LLMDevs,
   old.reddit.com/r/LLMDevs — arXiv 2604.08407.)
2. **Supply-chain hygiene is a selection criterion, not a footnote.** LiteLLM 1.82.7/1.82.8 on PyPI
   shipped credential-stealing malware (via an unpinned/compromised Trivy GitHub Action; the attacker
   even made the malicious tags immutable — the maintainers hadn't). Practitioner takeaway:
   "Publishing pipeline is the real evaluation criterion here, not just library features." Also:
   running the gateway as an isolated proxy process contains the blast radius vs importing it as an
   SDK. ("After the supply chain attack, here are some litellm alternatives", r/Python.)
3. **The "same" model is several different products per provider.** OpenRouter hosts for one model
   string differ in quantization (some undisclosed), honored context window (silent truncation),
   sampling-param handling (some ignore temperature), and cache pricing (DeepSeek official ≈10x
   cheaper on cache hits). "Every 'this model got nerfed' post should be read as 'my route changed'
   until proven otherwise." Users want quant level + effective context surfaced in response
   metadata. ("The same model on OpenRouter is five different products…", r/openrouter.)
4. **Cache-awareness must be *inside* the routing objective, not just stickiness.** The dominant
   objection in the 113-comment Weave thread (HN 48688700) is prompt-cache destruction. The answer
   that survived scrutiny: charge the cache-miss cost against every switch decision (switch only if
   expected saving/quality gain > re-prefill cost). Consequence practitioners predicted and the
   author confirmed: conversations converge to 1–3 models; the *free* routing win is subagents /
   fresh contexts, not mid-conversation swaps. Millwright (HN 49011806) markets "cache-aware
   concurrency" as its headline feature.
5. **Cost-control failure mode = default-model drift + auto-reload cascade.** $750/3-day burn:
   cron jobs and subagents silently inherited an expensive default model; billing auto-reloads
   ($28.96 x25) fired faster than the user could react; one 6-min cron run cost ~$120. Fixes were
   all *pinning and caps*, not routing: per-job model locks, cheap default, budget limits.
   "Avoid openrouter/auto - It's like opening your wallet and say `take it`."
   ("We burned $750 in 3 days on OpenRouter", r/openclaw.)
6. **Routing skepticism has specific, respectable shapes** (HN 48688700, 40922739, r/LocalLLM):
   (a) power users prompt differently per model, so a router breaks their calibration; (b) the
   prompt alone under-determines difficulty ("add a status icon" may hide a websocket+auth rabbit
   hole — Rice's theorem argument); (c) agent harnesses are already model-aware (subagent tiers),
   so a proxy router competes with the orchestrator; (d) harness×model symbiosis: the same Opus
   behaves differently in Claude Code vs Copilot vs OpenCode; (e) GPT-5-router backlash: "I want
   to know what model I am speaking to and what I am paying my money for" — users demand visible
   model attribution and a per-request bypass of all routing rules.
7. **Gateway ops pain is quantified, and it's why Go/Rust rewrites keep appearing.** LiteLLM in
   prod: ~8–12ms overhead, 50–80ms spikes on cache-miss/fallback, OOM pod kills, single worker
   collapsing past ~350 RPM; "7000+ line utils.py"; "the worst code I have ever read in my life."
   Measured p50 overhead (4 cores, mock upstream): agentgateway 0.37ms, Bifrost 0.73ms, Portkey
   5.99ms, LiteLLM 9.29ms. LiteLLM itself is rewriting its hot path in Rust (15x throughput, 11x
   less memory). One operator: "silent misconfigs are way scarier than latency spikes" — config
   validation ranked above speed. (HN 44650567 Any-LLM thread; "LiteLLM Rust Migration",
   r/LLM_Gateways; "We started with LiteLLM…", r/mcp.)
8. **The scope line practitioners draw: plumbing vs governance.** "When you're building governance
   (who can use what, limits, audit) instead of plumbing (retries, logging), the wrapper has become
   a platform" — plumbing is fine to own forever; governance grows without bound. Teams leave
   LiteLLM when it crosses that line, not over features. ("We started with LiteLLM…", r/mcp.)
9. **Preference/policy routing beats benchmark routing in the field.** Arch-Router (HN 44436031,
   r/LocalLLaMA 426-pt thread): route on *developer-defined usage policies* (task/domain →
   preferred model), user-overridable via headers, decoupling route selection from model
   assignment — explicitly contrasted with RouteLLM's benchmark-trained weak/strong split, whose
   benchmarks "may not capture subjective, domain-specific quality signals." A 1.5B router model
   quantizes with negligible loss (93.17→92.99) and serves at ~50ms.
10. **Small-model failure recovery is expected of a router.** Small models "stop before completion,
    throw errors and produce loops"; the credible routers ship a rescue path (bigger model takes
    over when the small one is stuck) and learn per-task small-model no-go zones. (HN 48688700.)

## Confirmations of existing conclusions

- Pre-request routing + OpenAI-compatible virtual model endpoint is the assumed shape everywhere.
- Stickiness/escalate-only matches the cache-aware-threshold consensus (README #3) — practitioners
  just make the cache cost explicit in the objective.
- Two-layer split (model selection vs provider selection, README #4) is validated hard by the
  OpenRouter-variance thread: provider pinning (`provider.order`, `allow_fallbacks:false`) is the
  #1 practitioner tip.
- Cheap classifier over LLM-judge routing (README #2): "LLM as another judge… uses more tokens
  than saves"; BERT-class/KNN routers considered sufficient.
- Fallback-on-content-filter and rate-limit failover (README #6) reconfirmed as primary real-world
  reasons people adopt routers at all (HN 40922739).

## Design implications for routsi (ADR-ready)

- **Trust posture is the product.** Self-hosted single Go binary, zero runtime deps, no telemetry,
  reproducible builds, pinned+minimal GitHub Actions, signed immutable releases, small go.mod.
  Document the release pipeline in the README — practitioners audit it before adopting.
- **Tamper-evidence:** append-only request/decision log (who routed what where, hash-chained);
  never mutate response bodies — rewrite only the request `model` field; stream bytes through.
- **Hard budget layer, fail-closed:** per-key/per-conversation/global spend caps that *block*
  (not alert) when exceeded; per-route model pinning; a visible kill switch; spend counters in a
  status endpoint. Defaults must be cheap — never let an expensive model be the silent default.
- **Cache-cost term in the switch decision:** estimated re-prefill cost of the candidate model vs
  expected gain; expose the threshold as config. Never switch mid-conversation for savings alone.
- **Provider pinning as first-class config:** logical model → ordered upstream list with
  `allow_fallbacks` per entry; record which upstream served each request.
- **Transparency + escape hatch:** echo chosen model/upstream in `model` + `X-Selected-Model` /
  `X-Selected-Provider`; honor a per-request override header that bypasses all routing.
- **Config validation before speed:** strict schema, fail-fast on unknown keys, `--check` mode.
  Then keep hot-path overhead sub-ms — the Go competitors already there set the bar.
- **Scope discipline:** ship plumbing (routing, retries, caps, logs); refuse governance
  (org RBAC, team budgets UI, audit platform) — that is the LiteLLM bloat trap.
- **Rescue path:** on small-model failure signals (truncation, loop, tool-call error), retry once
  on the escalation model; log it as router feedback.
