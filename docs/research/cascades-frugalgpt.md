# Cascade / deferral line of work — research notes for routsi ADR

Beat: FrugalGPT and successors — how cascades score answer quality, measured cost/latency trade-offs, and whether cascading survives contact with an OpenAI-compatible **streaming** proxy.

## 1. FrugalGPT (arXiv 2305.05176, Chen/Zaharia/Zou 2023)

**Mechanism.** Sequential LLM cascade: call cheap API, score its answer, accept if score ≥ threshold, else call next API in list. Three cost levers: prompt adaptation (fewer few-shot examples, query concatenation/batching), completion cache (semantic-similarity lookup of prior answers), LLM cascade.
- **Scorer g(q, a):** a **DistilBERT fine-tuned for regression**, input = (query, candidate answer), output = reliability score in [0,1]. Trained on labeled correct/incorrect examples from the target task's training split — i.e. per-task supervised training data is required.
- **Cascade construction:** constrained optimization — pick a list of m LLM APIs + per-API thresholds τᵢ to maximize expected accuracy s.t. average cost ≤ budget b. Search space pruned by discarding LLM lists with low answer disagreement; objective approximated by interpolation over a few samples. Execution stops at first API whose scored answer clears its threshold.
- **Results:** HEADLINES (finance, 10K): 98.3% cost reduction vs GPT-4 at +1.5% accuracy. OVERRULING (legal, 2.4K): 73.3% cheaper, +1%. COQA (7,982): 59.2% cheaper, matched GPT-3. Headline claim: match GPT-4 with up to 98% cost cut, or +4% accuracy at equal cost.
- **Gaps:** **zero latency measurement** (paper is cost/accuracy only); per-task scorer training; prompt-adaptation & cache evaluated only conceptually. (https://arxiv.org/abs/2305.05176)

## 2. AutoMix (arXiv 2310.12963, NeurIPS 2024)

Replaces the trained scorer with **few-shot self-verification**: the small model is re-prompted to judge its own answer (no trained verifier), and because that signal is noisy, a **POMDP router** treats verification probability as a noisy observation of latent question difficulty and decides keep-vs-escalate. Across 5 LLMs / 5 datasets: **>50% compute-cost reduction at comparable performance**. Notable for a proxy: no offline training data needed, but self-verification costs an extra LLM call per request on top of the small model's generation. (https://arxiv.org/abs/2310.12963, https://github.com/automix-llm/automix)

## 3. Cluster, Route, Escalate (arXiv 2606.27457)

Two-stage hybrid that is closest to a practical recipe:
- **Stage 1 (pre-request routing):** all-MiniLM-L6-v2 embeddings → k-means (k chosen by silhouette over 2–10; ended up 2–3 clusters). Per cluster, pick model minimizing `Error(m,c) + λ·Cost_norm(m)` after Pareto-pruning dominated models; λ* auto-selected to hit a user TPOT budget (20 ms in experiments). Inference = one embedding lookup + nearest centroid → negligible overhead.
- **Stage 2 (escalation):** **fine-tuned ModernBERT-base binary classifier** (Accept/Escalate) on (query + model output + generation length), labels from exact-match correctness; class-weighted CE loss; argmax decision (no threshold tuning). Adds **~0.52 ms per output token**.
- **Training data:** just task-correctness labels — 921 queries (AIME), 9,000 (TeleQnA).
- **Results:** AIME24: 88.4% acc @ 9.7 ms TPOT vs strongest model 89.1% — 18% lower latency. TeleQnA: 74.3% vs 76.4% baseline. Retains 97–99% of strongest-model accuracy. Serving: vLLM on 2×A100; **requires all candidate models resident in GPU memory** — an assumption an API proxy doesn't share, but the cost structure transfers.

## 4. Is Escalation Worth It? (arXiv 2605.06350, Bouchard 2026) — the sobering one

Decision-theoretic characterization of threshold cascades, 5 benchmarks (MATH, MMLU, TriviaQA, SimpleQA, LiveCodeBench), 8 models / 5 providers.
- **Confidence signal:** white-box logprobs — main signal = mean token negentropy; alternatives (length-normalized seq prob, min token prob, margin) + a logistic-regression ensemble compared. Policy: escalate when s_L(x) < τ; thresholds by grid sweep (2-model) / NSGA-II (k>2). Derives shadow-price optimality conditions; 2-model frontier is piecewise concave.
- **Findings:** (a) full fixed k-model chains **underperform the pairwise envelope** — extra intermediate stages don't help; (b) a **lightweight pre-generation router (frozen sentence-transformer embeddings, deliberately a diagnostic baseline) beats the best cascade policy on 4/5 datasets** (MMLU +3.3pp, SimpleQA +3.5pp, LiveCodeBench +3.6pp normalized gain; cascade wins only TriviaQA, −9.7pp); (c) the router wins **not** because its signal is stronger but because of **structural cost — the cascade always pays the cheap model's full generation before it can decide**. Cascades are limited by that sunk cost, not by too few stages. No latency analysis; explicitly notes latency-aware objectives could shift conclusions — but latency makes cascades look *worse*, not better. (https://arxiv.org/abs/2605.06350)

## 5. Cascades × streaming

This is the crux for an OpenAI-compatible proxy, and the literature largely ducks it:
- FrugalGPT, AutoMix, 2605.06350: all assume **post-generation** scoring of a complete answer. No streaming discussion anywhere; the routing/cascading survey (arXiv 2603.04445) confirms streaming/partial-response evaluation is simply not treated, and its own proposed architecture (pre-router → post-generation verifier → escalation policy) is still complete-response.
- **RLM-Cascade (arXiv 2606.22840)** — response-level speculative decoding for LLM *API serving* — is the one paper aimed at our setting, and it **buffers**: small model generates the full response, verifier judges it server-side, only then does the client see tokens. TTFT explicitly worsens; escalation doubles it (cheap-model gen + verify + big-model gen).
- **Speculative cascades** (Google Research; "Faster Cascades via Speculative Decoding", ICLR 2025) reconcile cascading with token streaming — big model verifies draft tokens in parallel every γ tokens, better quality-per-cost than plain speculative decoding — but require **logit-level access to both models in one serving stack**. Not implementable over opaque provider HTTP APIs.
- The unavoidable proxy dilemma: if you stream the cheap model's tokens to the client, escalation means **retracting already-delivered text** — the OpenAI SSE protocol has no retraction primitive. If you buffer until verified, you destroy TTFT, the metric streaming clients care about. Mitigations (judge on first N tokens; visible "rewrites") are hacks with no literature validation.

## 6. Verdict for routsi

**Pre-request routing is the right call; do not build cascading into the streaming path.**
1. Even ignoring streaming, 2605.06350 shows a cheap embedding router matches/beats optimal cascades on cost-quality because cascades eat the small model's sunk generation cost. And it measured only $-cost — cascade latency (serial worst-case ≈ sum of two full generations) is strictly worse.
2. Streaming is protocol-fatal for cascades over provider APIs: no token retraction in SSE, no logit access for speculative-cascade tricks, TTFT collapse if buffered (RLM-Cascade's own concession).
3. What *is* worth stealing: (a) FrugalGPT/CRE's recipe of a **tiny encoder scorer trained on correctness labels** — usable offline to label traffic and fit the router; (b) CRE's **cluster→model assignment with a λ cost knob and TPOT budget** — directly portable as the router's decision rule (embed query → nearest centroid → model), ~1 embedding lookup of overhead; (c) an **async post-hoc judge** on completed (non-streamed or logged) responses to generate the correctness labels that keep the router calibrated — escalation as a feedback loop, not an inline hop; (d) optionally a non-streaming "quality mode" endpoint where a bounded 2-model cascade is honest about latency.
4. Sticky per-conversation routing composes cleanly with pre-request routing (route on first turn, pin) and *badly* with cascades (mid-conversation escalation churns models and KV/prompt caches).


## Key takeaways
- Pre-request routing beats cascading: arXiv 2605.06350 shows even a frozen-embedding router beats optimal threshold cascades on 4/5 benchmarks, because cascades always sink the cheap model's full generation cost before deciding.
- No cascade paper solves streaming: all score complete answers post-generation; RLM-Cascade (2606.22840), the one API-serving cascade, buffers the whole small-model response before sending — TTFT dies, and SSE has no way to retract streamed tokens.
- Speculative cascades (ICLR'25) do reconcile cascading with streaming but need logit-level co-located models — impossible over opaque provider HTTP APIs, so irrelevant to a proxy.
- Quality scorers are tiny and fast: FrugalGPT uses a DistilBERT regressor on (query, answer); Cluster-Route-Escalate uses a ModernBERT accept/escalate classifier adding ~0.52 ms/token — both trained only on binary correctness labels (~1K-9K examples).
- Cost numbers are real but task-specific: FrugalGPT 59-98% cost cut at GPT-4 parity (per-task trained scorer); AutoMix >50% with zero-training few-shot self-verification + POMDP router.
- Steal CRE's router recipe: MiniLM embedding -> k-means centroid -> per-cluster model chosen by Error + lambda*Cost under a latency (TPOT) budget; inference overhead is one embedding lookup.
- More cascade stages don't help: full fixed chains underperform the best 2-model pairwise cascade (2605.06350), so any fallback logic should be at most cheap->strong.
- Escalation belongs offline: run a post-hoc judge on logged responses to produce correctness labels that recalibrate the router, instead of inline mid-request escalation.
- Sticky conversation routing composes with pre-request routing (route turn 1, pin the model) and conflicts with cascades, which would churn models and prompt/KV caches mid-conversation.
- If a cascade mode is ever wanted, make it an explicit non-streaming 'quality' endpoint with honest latency, never the default streaming path.

## Sources
- https://arxiv.org/abs/2305.05176
- https://ar5iv.labs.arxiv.org/html/2305.05176
- https://arxiv.org/abs/2310.12963
- https://arxiv.org/abs/2606.27457
- https://arxiv.org/abs/2605.06350
- https://arxiv.org/html/2605.06350
- https://arxiv.org/abs/2606.22840
- https://arxiv.org/html/2603.04445v2
- https://research.google/blog/speculative-cascades-a-hybrid-approach-for-smarter-faster-llm-inference/
- https://proceedings.iclr.cc/paper_files/paper/2025/file/6f43166f50f26e8d8f3edc5545b0749f-Paper-Conference.pdf
- https://github.com/automix-llm/automix
