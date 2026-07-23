# Production LLM Routers — Field Notes (industry beat)

## 1. OpenRouter (Auto Router + provider routing)
- **Exposure**: pure model-name aliasing on the OpenAI-compatible surface — `model: "openrouter/auto"` triggers routing; response `model` field reveals the actually-selected model. No new endpoint, no headers required. (https://openrouter.ai/docs/guides/routing/routers/auto-router)
- **Algorithm (Auto Beta)**: a *fast lightweight classifier* assigns each prompt one of **~30 fine-grained task types**; candidates are then ranked by **community spend-share over the trailing 7 days** per task type; a `cost_quality_tradeoff` int (0–10, default 9) acts as a cost-percentile ceiling filtering the candidate pool; top spend-share model wins, cheapest model always stays eligible; **graceful degradation to defaults if classification fails**. Earlier version delegated to **NotDiamond** (`openrouter/auto`). Benchmarks: 83.8% GPQA Diamond at max-quality setting.
- **Per-request steering**: `allowed_models` (wildcards like `anthropic/*`, `openai/gpt-5*`), `cost_quality_tradeoff`, and tool-call requests default to a quality-first sub-router ("Auto Exacto").
- **Conversation stickiness (directly your problem)**: "The Auto Router **pins both the selected model and provider** so subsequent requests in the same conversation route to the same place." Two mechanisms: **implicit message-hash fingerprinting** (hash of conversation prefix) or **explicit `session_id`** field; sticky cache **expires after 5 min of inactivity**. (auto-router doc above)
- **Provider routing (below model routing)**: default = filter out providers with recent outages (30 s window) → **inverse-square price weighting** (a $1/M provider gets ~9x the traffic of $3/M) → rest are automatic fallbacks. Overrides: `provider.sort` = price|throughput|latency, `provider.order` explicit list, `allow_fallbacks: false` for hard pinning; suffix sugar `:nitro` (throughput), `:floor` (price). (https://openrouter.ai/blog/insights/model-routing/)
- **Fallback/retry**: request-level `models: []` array = priority-ordered model fallback, triggered on context-length errors, moderation flags, 429, downtime; provider failover on 5xx/429 automatic. **Billing follows the model that actually ran**; failed attempts uncharged ("zero-completion insurance"). Streaming caveat: aborting a stream doesn't stop billing on some providers (Bedrock, Groq, Google, Mistral) — i.e. once tokens flow, failover is over; all routing decisions happen **before the first upstream byte**.
- **Pricing**: no router fee — you pay the chosen model's rate.

## 2. NotDiamond
- **Exposure**: **router-as-a-service, separate endpoint** — `model_select` takes `messages`, `llm_providers` (candidate list), `tradeoff` ("cost" | "latency" | None=quality), optional `preference_id` (a trained custom router); returns `session_id` + `provider.model`; the client then calls the chosen model with its own SDK/keys. Clean separation of *decision* from *inference*. (https://docs.notdiamond.ai/docs/quickstart-routing)
- **Training a custom router**: submit (prompts, candidate-model responses, **evaluation scores**) → NotDiamond trains a "meta-model" predicting per-prompt which candidate wins; custom models are described to it via `context_length`, `input_price`, `output_price`, `latency` (TTFT seconds) so the tradeoff math is explicit. (https://docs.notdiamond.ai/docs/router-training-quickstart)
- Feedback loop exists (human-in-the-loop routing docs) — router improves from post-hoc scores.

## 3. Martian
- **Disclosed mechanism**: "Model Mapping" — a mechanistic-interpretability/distillation-adjacent technique; **judge models score expert models' capabilities**, a router directs each query to the expert predicted most trustworthy; claims beating GPT-4 on cost/perf and indexing new models automatically. Little algorithmic detail public beyond this judge+predictor framing. (https://withmartian.com/post/introducing-martian---better-ai-tools-through-better-understanding, Yahoo Finance funding PR)

## 4. Unify.ai
- **Algorithm**: a **neural scoring function predicts quality per (prompt, model) ahead of time**, trained on representative benchmark data; combined with **live runtime benchmarks refreshed ~every 10 min** for each endpoint's cost and speed. Routing objective = weighted mix exposed as **three sliders: quality / cost / latency**; router string encodes the config. So: learned quality predictor + measured cost/latency telemetry → scalarized score → argmax. (Intel community writeup: https://community.intel.com/t5/Blogs/Tech-Innovation/Artificial-Intelligence-AI/Find-Your-Best-LLM-Unify-Helps-Detect-the-Right-LLM-Quickly-and/post/1622214, https://www.ycombinator.com/launches/L4t-unify-the-best-llm-on-every-prompt)

## 5. LiteLLM proxy (open-source, closest structural comp for a Go proxy)
- **Model groups**: `model_list` entries share a `model_name` alias; each deployment gets a deterministic hash `model_id`. Client asks for the alias; router picks the deployment. (https://docs.litellm.ai/docs/routing)
- **Routing strategies** (config-selected): `simple-shuffle` (weighted random by rpm/tpm/weight — recommended default, lowest overhead), `least-busy` (fewest in-flight), `usage-based-routing-v2` (lowest TPM this minute, **Redis atomic incr/mget for multi-instance correctness**), `latency-based` (lowest rolling-window latency; TTFT for streams; `lowest_latency_buffer` = anything within X% of fastest is eligible, picked randomly, to avoid hotspotting), `cost-based`, plus `CustomRoutingStrategyBase` plug-in.
- **Reliability**: per-deployment **cooldowns** (default 3 fails/min → 5 s cooldown; isolates one deployment, not the group), `num_retries` with exponential backoff only for 429s, `order` field for priority tiers (exhaust order-1 → order-2), then cross-group **fallbacks**; separate fallback lists per error class (context-window fallbacks, content-policy fallbacks). **Pre-call checks** filter deployments by context-window fit and region before selection. All shared state in Redis.
- Latency-based routing benchmarked ~38% lower p95 than round-robin on mixed workloads (https://markaicode.com/benchmarks/litellm-routing-benchmark/).

## 6. Portkey Gateway
- **Exposure**: config-first — a JSON **Config object** (referenced by ID or passed inline via `config` param/header) with `strategy.mode`: `single` | `loadbalance` | `fallback` | `conditional`; strategies **nest** (loadbalance inside fallback, conditional targets that are themselves loadbalancers). (https://docs.portkey.ai/docs/api-reference/config-object)
- **Conditional routing**: Mongo-style query DSL over `metadata.<key>` (flat custom KV sent with request), `params.<key>` (any primitive request param incl. `model`, `temperature`), `url.pathname`; operators `$eq/$ne/$in/$nin/$regex/$gt/$gte/$lt/$lte` + `$and/$or`; conditions evaluated top-down, first match wins, mandatory `default` target; missing keys evaluate false (no error). Targets can carry `override_params` (e.g. map alias "fastest" → concrete model) and input/output guardrails. (https://portkey.ai/docs/product/ai-gateway/conditional-routing)
- **Retries**: `{attempts, on_status_codes:[429,502,503]}` per config; weights on targets for canary/loadbalance.

## 7. Cloudflare AI Gateway
- **Exposure**: unified OpenAI-SDK-compatible endpoint + per-provider endpoints; **dynamic routes** are versioned flow graphs (dashboard or JSON): Start → Conditional (expressions over request body / headers / metadata like userId/plan) → Percentage split (A/B, gradual rollout) → Rate-limit / Budget-limit nodes (quota per key per period, overflow triggers fallback edge) → Model nodes (with fallback edge on error/timeout after retries) → End. Instant deploy + rollback of route versions. Gateway-level automatic retries on upstream error, no client change. (https://developers.cloudflare.com/ai-gateway/, https://developers.cloudflare.com/ai-gateway/features/dynamic-routing/)
- Notable: **budget/rate limits as routing nodes** — cost governance inside the routing graph, not just per-key middleware.

## 8. OpenAI GPT-5 real-time router
- A unified system: fast main model + deeper thinking model + a **real-time router deciding per turn** on: conversation type, complexity, tool needs, and **explicit user intent** (phrases like "think hard about this" are a routing signal). (https://openai.com/index/introducing-gpt-5/, system card https://cdn.openai.com/gpt-5-system-card.pdf)
- **Continuously trained on real signals**: user model-switches, response preference rates, measured correctness — RL-updated decision boundary. Claimed ~94% complexity-identification accuracy. Usage-limit exhaustion degrades to mini variants — i.e. **capacity is itself a routing input**.

## Cross-cutting API-design patterns to adopt in the Go proxy
1. **Route via model alias** (`auto`, or a virtual model name) on the standard `/v1/chat/completions` body — zero client changes; echo the real model in the response `model` field and/or an `X-Selected-Model` header for observability.
2. **Two-layer routing**: (a) model selection (which logical model), (b) deployment/provider selection (which upstream instance) — keep them separate components like OpenRouter/LiteLLM.
3. **Stickiness**: OpenRouter's exact recipe — explicit `session_id` (accept body field or header) with fallback to **hashing the conversation prefix** (first N messages) when absent; store choice in a TTL cache (theirs: 5 min inactivity). Sticky on model *and* provider.
4. **Cheap classifier, not an LLM call, on the hot path**: task-type classifier + precomputed rankings (OpenRouter), or predictive scoring model (Unify/NotDiamond). Always define **graceful degradation to a default model when classification fails**.
5. **Decide before first byte**: all routing/fallback happens pre-stream; once SSE tokens flow, you're committed (OpenRouter's billing caveat proves nobody re-routes mid-stream). Retries/fallback fire on connection error, 5xx, 429, context-length, moderation — *prior to* streaming output.
6. **Reliability kit** (LiteLLM shapes): per-upstream cooldown (N fails/min → T-second eviction), retry with backoff only on 429, priority `order` tiers, error-class-specific fallback lists, pre-call context-window fit check, Redis for shared counters if multi-instance.
7. **Config surface**: declarative JSON strategies that nest (Portkey) and/or a condition DSL over metadata/params (Portkey `$eq/$regex`, Cloudflare expression nodes) beats hardcoded logic; version configs with rollback (Cloudflare).
8. **Steering knobs**: a scalar cost/quality dial (0–10) and an `allowed_models` wildcard filter are the two per-request controls every commercial router converged on.

## Key takeaways
- Every commercial router exposes routing as a virtual model name (openrouter/auto) on the unchanged OpenAI chat-completions API, and reveals the chosen model in the response `model` field — adopt this exact pattern.
- OpenRouter solves sticky-per-conversation routing with explicit session_id OR implicit conversation-prefix hash fingerprinting, pinning model+provider in a cache that expires after 5 min inactivity — a direct blueprint for your conversation-id stickiness.
- Hot-path routing decisions use a cheap classifier/predictor, never an LLM call: OpenRouter classifies into ~30 task types then ranks by 7-day spend-share; Unify uses a trained neural quality predictor + live cost/latency benchmarks; always degrade gracefully to a default model on classifier failure.
- Keep two separate routing layers: model selection (which logical model for this question) and deployment/provider selection (which upstream endpoint) — OpenRouter, LiteLLM, and Portkey all structure it this way.
- All fallback/retry happens BEFORE the first streamed byte; nobody re-routes mid-SSE-stream — trigger fallback on connect error, 5xx, 429, context-length overflow, and moderation rejection, then commit once streaming starts.
- LiteLLM's reliability kit is the reference implementation: per-deployment cooldown (3 fails/min → short eviction), exponential backoff only on 429, priority-order tiers before cross-model fallback, per-error-class fallback lists, pre-call context-window checks, Redis for multi-instance counters.
- Config-first routing beats code: Portkey's nestable JSON strategies (single/loadbalance/fallback/conditional with a Mongo-style $eq/$regex query DSL over metadata and params) and Cloudflare's versioned routing graphs with instant rollback are the two DSL patterns worth copying.
- The two per-request steering knobs the market converged on: a scalar cost/quality dial (0-10 percentile ceiling) and an allowed_models wildcard filter (anthropic/*).
- NotDiamond shows the alternative architecture — routing as a separate model_select endpoint returning (session_id, chosen model) decoupled from inference — useful if you want the router reusable outside the proxy; its custom routers are trained on (prompt, candidate responses, eval scores) triples.
- OpenAI's GPT-5 router routes per turn on conversation type, complexity, tool needs, and explicit intent phrases ('think hard'), is RL-trained on user model-switches and preference/correctness signals, and treats remaining capacity/quota as a routing input.

## Sources
- https://openrouter.ai/docs/guides/routing/routers/auto-router
- https://openrouter.ai/blog/insights/model-routing/
- https://docs.notdiamond.ai/docs/quickstart-routing
- https://docs.notdiamond.ai/docs/router-training-quickstart
- https://docs.litellm.ai/docs/routing
- https://markaicode.com/benchmarks/litellm-routing-benchmark/
- https://portkey.ai/docs/product/ai-gateway/conditional-routing
- https://docs.portkey.ai/docs/api-reference/config-object
- https://developers.cloudflare.com/ai-gateway/
- https://developers.cloudflare.com/ai-gateway/features/dynamic-routing/
- https://openai.com/index/introducing-gpt-5/
- https://cdn.openai.com/gpt-5-system-card.pdf
- https://withmartian.com/post/introducing-martian---better-ai-tools-through-better-understanding
- https://community.intel.com/t5/Blogs/Tech-Innovation/Artificial-Intelligence-AI/Find-Your-Best-LLM-Unify-Helps-Detect-the-Right-LLM-Quickly-and/post/1622214
- https://www.ycombinator.com/launches/L4t-unify-the-best-llm-on-every-prompt
