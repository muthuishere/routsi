# RouteLLM — deep notes (paper arXiv:2406.18665 + lm-sys/RouteLLM repo)

## 1. Problem framing
RouteLLM reduces LLM serving cost by routing each query between exactly **one strong model** (paper: `gpt-4-1106-preview`) and **one weak model** (`mixtral-8x7b-instruct-v0.1`). The router is a learned **win-rate predictor**: it estimates `P(strong wins | query)` from human preference data, then a scalar **cost threshold α ∈ [0,1]** converts that probability into a decision — route to weak iff `P(strong wins|q) < α`, else strong. All quality/cost trade-off is in α; the predictor itself is threshold-free and trained once. (Paper: https://arxiv.org/abs/2406.18665, html v3: https://arxiv.org/html/2406.18665v3)

## 2. Metrics (worth copying verbatim for our ADR)
- **PGR** (performance gap recovered): `(r(router) − r(weak)) / (r(strong) − r(weak))` — normalizes benchmark score between the two endpoints.
- **APGR**: PGR averaged over 10 evenly spaced cost levels (integral of the cost-quality curve) — one number per router.
- **CPT(x%)**: minimum % of calls sent to the strong model to reach x% PGR. E.g. CPT(80%)=36% ⇒ you hit 80% of GPT-4-level quality while calling GPT-4 on only 36% of traffic.

## 3. Training data
- **Chatbot Arena** preference battles: 80k raw → 65k after pruning short prompts. Sparsity fix: cluster the ~dozens of arena models into **10 Elo tiers**; tiers 0–1 ≙ "strong", tier 2 ≙ "weak" — labels become tier-vs-tier outcomes, not model-vs-model. This tier abstraction is *why routers transfer to unseen model pairs*.
- **Augmentation** (big win in results): (a) golden-label ~1.5k MMLU validation questions; (b) **LLM-judge**: ~120k Nectar prompts labeled by GPT-4-as-judge for ~$700. Cheap synthetic preference data materially improved CPT (MT-Bench CPT(50%) dropped to 23.2%).
- Loss: binary cross-entropy / MLE over preference labels (`max_θ Σ log P_θ(l|q)`), i.e. Bradley-Terry-style win prediction.

## 4. The four routers (mechanisms)
Source: `routellm/routers/routers.py`; all implement `calculate_strong_win_rate(prompt) -> float` on the **last user turn only**; `route()` is just a threshold compare. Registry `ROUTER_CLS = {random, mf, sw_ranking, bert, causal_llm}`.
1. **sw_ranking (similarity-weighted Elo)** — no trained params. Embed prompt with OpenAI `text-embedding-3-small`; cosine-sim against pre-embedded arena battles; weight each battle `10 * 10**(sim/max_sim)`; run weighted Elo MLE (`compute_elo_mle_with_tie(arena_df, sample_weight=weightings)`); win rate = logistic Elo `1/(1+10**((elo_w−elo_s)/400))`. Per-request cost: one embedding call + an MLE fit ⇒ slowest/most expensive router (~$37/1M requests, ~0.4% of GPT-4 spend) and needs the whole arena dataset in memory.
2. **mf (matrix factorization — the recommended one)** — bilinear model `MFModel`: learned per-model embeddings (num_models=64, hidden 128) × projected prompt embedding (OpenAI embedding, text_dim=1536); `pred_win_rate(strong_id, weak_id, prompt)` scores the pair. Tiny (runs on CPU/GPU trivially), pretrained checkpoint on HF (`routellm/mf_gpt4_augmented`). Best cost/quality/latency balance.
3. **bert** — `AutoModelForSequenceClassification`, **3 classes** (strong-wins / tie / weak-wins); `binary_prob = softmax[-2:].sum()` (tie+weak mass), win rate `= 1 − binary_prob`. No external embedding API needed.
4. **causal_llm** — fine-tuned `meta-llama/Meta-Llama-3-8B` scoring the prompt against label tokens `[[1]]..[[5]]` (5-way score head, instruction-format via `system_ft_v5.txt`/`classifier_ft_v5.txt` pulled from HF Hub); win rate `= 1 − binary_prob`. Heaviest; needs a GPU; marginal gains over MF.
- Throughput: GPU routers 42–155 req/s; paper argues router latency/cost is negligible vs generation.

## 5. Threshold calibration (α)
Thresholds are **not** interpretable across routers, so they calibrate offline against a reference distribution: `python -m routellm.calibrate_threshold --routers mf --strong-model-pct 0.5` sweeps the Chatbot Arena prompt set and returns the α that sends ~50% of that traffic to the strong model (e.g. `mf @ 0.11593`). I.e. **you pick a strong-model budget %, not a quality bar**, and calibration maps budget→α. Caveat they state: calibrate on *your* traffic distribution if it differs from Arena.

## 6. Serving layer (repo)
- **Controller** (`routellm/controller.py`) — SDK-style drop-in for the OpenAI client: `Controller(routers=["mf"], strong_model=..., weak_model=...)` exposes `chat.completions.create()`-compatible `completion`/`acompletion`. Routing decision is *encoded in the model name*: `model="router-mf-0.11593"` → parsed into (router, α) per request, validated (`RoutingError`), then `_get_routed_model_for_completion` takes the **last message content**, calls `routers[r].route(prompt, α, model_pair)`, increments `model_counts[router][model]`, and forwards **all kwargs to LiteLLM** `completion/acompletion` (which handles provider fan-out: OpenAI/Anthropic/Together/Bedrock/local via `--alt-base-url`). Also `batch_calculate_win_rate` over a pandas Series for offline eval.
- **OpenAI-compatible server** (`routellm/openai_server.py`) — FastAPI + uvicorn, default `0.0.0.0:6060`. Endpoints: `POST /v1/chat/completions`, `GET /health`. Single global `CONTROLLER` built in FastAPI lifespan from CLI args + YAML config (checkpoint paths per router). **Streaming**: sets `stream=True` through to LiteLLM and wraps as SSE — `StreamingResponse(stream_response(res), media_type="text/event-stream")` yielding `data: {chunk.model_dump_json()}\n\n` … `data: [DONE]\n\n`. Routing happens **before** the upstream call, so streaming is trivial: decide → open one upstream stream → relay chunks verbatim. Errors → 400 JSON. **No inbound auth, no rate limiting, no conversation state.**
- Repo layout: `routellm/` (routers, controller, server, calibrate_threshold), `benchmarks/` (MMLU/GSM8K/MT-Bench with cached model responses), `examples/`.

## 7. Results (why this matters)
MT-Bench (augmented MF/causal): CPT(50%) 23.2%, CPT(80%) 36.0%, APGR 0.759 (+51.8% vs random), **3.66× cost saving at 95% of GPT-4 quality** (blog claims up to 85% cost cut / 95% quality). MMLU: CPT(50%) 35.4%, APGR 0.603. GSM8K: CPT(50%) 33.6%, APGR 0.622. **Generalization without retraining** to Claude-3-Opus/Llama-3-8B: CPT(50%) 30–40%, APGR +25–42% vs random — routers learn "query difficulty", not model identities.

## 8. Copy / skip for routsi (Go)
**Copy:**
- The core shape: *win-rate predictor + tunable threshold*, decided **before** the upstream call → streaming stays a dumb byte relay (SSE passthrough), no mid-stream switching.
- Router selection + threshold via the `model` field (`router-<name>-<alpha>`) — zero protocol changes, per-request tunability; or make α a server config with per-request override.
- Offline **threshold calibration against a traffic sample** (budget-% → α) instead of hand-picked magic numbers; log `P(strong)` per request so you can recalibrate from your own logs.
- MF-style tiny scorer (or an embedding + logistic head) as the default: an embedding call + a dot product is the whole per-request overhead; BERT-class model if you want zero external embedding dependency.
- `/health`, per-router routing counters (`model_counts`), LiteLLM-equivalent provider abstraction (in Go: per-upstream client config, not a library).
- Their metrics (PGR/APGR/CPT) as the eval harness for any router we build.
**Skip / do differently:**
- `sw_ranking` and `causal_llm`: operationally heavy (dataset-in-memory + per-request Elo fit; 8B GPU model) for marginal gain — MF/BERT dominate in practice.
- Their server is a demo: no auth, no timeouts/retries/fallback-on-upstream-error, single global controller, no multi-tenant config — we must add all of that.
- **No conversation stickiness anywhere**: routing is per-request on the last user turn only; multi-turn conversations can flip models turn-to-turn. Our conversation-id sticky layer (first-turn decision cached per conversation id, with optional escalation-only override) is genuinely additive — RouteLLM gives no prior art here.
- Binary strong/weak pair only; N-way routing needs the model-embedding trick (MF's 64 model slots hint at it) or a different formulation (cf. Arena-tiers idea: predict difficulty tier, map tier→model).
- Dependence on OpenAI embeddings at request time (mf/sw_ranking) is a latency + availability + privacy coupling; prefer a local embedding model in the proxy.


## Key takeaways
- Core pattern: a learned P(strong wins|query) predictor + scalar threshold alpha; the decision is made BEFORE the upstream call, so streaming is a plain SSE passthrough — copy this shape.
- Their best-value router (matrix factorization) is just prompt-embedding (1536-d) dot learned model embeddings — per-request overhead is one embedding + a dot product (~0.4% of GPT-4 cost, 42–155 req/s).
- Threshold is calibrated offline: pick a strong-model budget % on a traffic sample and solve for alpha (router-mf-0.11593 style); log win-rate scores in the proxy so you can recalibrate from your own traffic.
- Router + alpha ride in the OpenAI `model` field (`router-mf-0.11593`) — zero protocol change, per-request tunable; a clean trick for our Go proxy.
- Training data = 65k Chatbot Arena battles collapsed into 10 Elo tiers (tier-vs-tier labels), + ~120k GPT-4-judged synthetic prompts for ~$700; the tier abstraction is why routers transfer to unseen model pairs without retraining.
- Their FastAPI server is a demo: no inbound auth, no retries/fallback/timeouts, single global controller — the proxy-hardening layer is entirely ours to build.
- RouteLLM routes ONLY on the last user turn with zero conversation state — our conversation-id sticky routing has no prior art in RouteLLM and is a genuine differentiator; decide on first turn, cache per conversation id.
- It is strictly binary (one strong, one weak); N-way routing needs a different formulation (per-model embeddings or difficulty-tier -> model mapping).
- Evaluate any router with their metrics: PGR, APGR, and CPT(x%) (min % of strong-model calls to reach x% quality) — headline: 3.66x cost cut at 95% GPT-4 quality, CPT(80%)=36% on MT-Bench.
- Avoid request-time dependence on OpenAI embeddings (their mf/sw_ranking do this): couple latency/availability/privacy to a third party — use a local embedding model in the Go proxy.

## Sources
- https://arxiv.org/abs/2406.18665
- https://arxiv.org/html/2406.18665v3
- https://github.com/lm-sys/RouteLLM
- https://raw.githubusercontent.com/lm-sys/RouteLLM/main/routellm/controller.py
- https://raw.githubusercontent.com/lm-sys/RouteLLM/main/routellm/routers/routers.py
- https://raw.githubusercontent.com/lm-sys/RouteLLM/main/routellm/openai_server.py
