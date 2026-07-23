# Academic landscape of LLM routing (beyond RouteLLM/cascades) — research notes for routsi ADR

## 1. Design-space map (from the 2026 survey, arXiv:2603.04445)

The survey "Dynamic Model Routing and Cascading for Efficient LLM Inference" (https://arxiv.org/html/2603.04445v2) frames the space on three axes: **when** the decision is made (pre-request / during inference / post-response), **what feeds it** (query features, model metadata, historical performance), and **how it's computed** (rules, classifiers, RL, cascades). Six paradigms:

1. **Difficulty-aware routing** — heuristics, learned classifiers (DeBERTa in BEST-Route, Zooter), or LLM-as-judge complexity estimation.
2. **Preference-aligned routing** — trained on human/pairwise preference data (RouteLLM family, Arch-Router).
3. **Clustering / similarity routing** — embed the query, K-means clusters or KNN over previously scored examples (UniRoute, GraphRouter).
4. **RL / bandit routing** — contextual bandits (LinUCB: MixLLM, PILOT, GreenServ), multi-armed bandits (MetaLLM), Thompson sampling on dueling feedback (arXiv:2510.00841), PPO/GRPO policies.
5. **Uncertainty-based** — route/escalate on confidence: conformal prediction (CP-Router), hidden-state probes, logit calibration.
6. **Cascading** — sequential try-cheap-then-escalate with a quality verifier.

Survey's production recommendation: a 3-stage pipeline — (a) low-cost pre-router on query features, (b) post-generation quality/uncertainty verifier, (c) escalation policy. Latency numbers are sparse in the literature; GreenServ reports **8 ms routing overhead/query**; Arch-Router's 1.5B model is flagged as heavy vs classifier routers.

## 2. Individual mechanisms worth knowing

### RouterBench (arXiv:2403.12031, Martian + Berkeley)
405k precomputed inference outcomes, 11 LLMs, 7 tasks (https://arxiv.org/html/2403.12031v2, code: https://github.com/withmartian/routerbench). Evaluated **KNN router** (40 neighbors over all-MiniLM-L12-v2 embeddings), **MLP** (2×100 hidden), cascades, and a "Zero router" baseline (convex-hull interpolation between models). Metric: **AIQ** = normalized area under the cost-quality curve. Sobering findings: KNN ≈ MLP ≈ best single LLM at lower cost, but **no learned router significantly beat the Zero-router baseline**, while the Oracle is far ahead — i.e., router headroom is real but 2024-era routers barely captured it. Cascades win only when the quality-judge error rate ≤ 0.1; degrade fast above 0.2.

### LLMRank (arXiv:2510.01234)
Prompt-aware **neural ranking model over interpretable, hand-engineered features** — task type, reasoning patterns, complexity indicators, syntactic cues, plus signals from a **lightweight proxy solver** (a tiny model attempts the task; its behavior is a feature). Predicts per-model utility scores, argmax routes. Trained/evaluated on RouterBench (36,497 prompts, 11 benchmarks, 11 LLMs); reaches **~89.2% of oracle utility** while staying explainable (per-feature attributions). Key idea for an ADR: feature-driven scoring beats opaque embeddings when you need to debug WHY a request went to model X.

### IRT-Router (arXiv:2506.01048, ACL 2025)
Item Response Theory: jointly learns latent **query difficulty/attribute vectors** and **per-LLM ability vectors**; predicted P(correct) = IRT link function of (ability − difficulty). Interpretable (you get an explicit difficulty score per query and skill profile per model). Online **warm-up via semantic similarity**: new queries borrow parameters from similar seen queries. 20 LLMs / 12 datasets; beats most baselines. Attractive because adding a new model = fitting one ability vector, not retraining the router.

### UniRoute (arXiv:2502.08773, Google)
Solves the "**unseen model at test time**" problem: represent each LLM as a feature vector of its **prediction errors on ~a small set of representative prompts**; learn a router over (query embedding, model feature vector) pairs. Two instantiations: cluster-based routing (per-cluster observed accuracy per model) and a learned cluster map. Proves **KNN routing is a special case**; adding a new model needs only running it on the representative prompt set — no retraining. This is the cleanest academic recipe for a proxy where upstream models churn.

### Arch-Router (arXiv:2506.16655, katanemo/archgw)
The most proxy-shaped paper: a **1.5B generative router model** that matches a request against operator-written **routing policies in natural language** (domain/action descriptions like "code generation", "legal questions") — preference-aligned rather than benchmark-score-maximizing. Ships inside the Arch gateway (Envoy-based). Evaluated on multi-turn coding sessions. Cost: a 1.5B forward pass per request — real latency; the survey explicitly calls this suboptimal for latency-sensitive paths vs a small classifier.

### RL/bandit routers
MixLLM/PILOT/GreenServ use **LinUCB contextual bandits** over query embeddings with online reward = observed quality/cost — no offline label set needed, adapts as traffic and model lineup drift. GreenServ: 8 ms overhead, +22% accuracy vs random. Practical note: bandits need a reward signal your proxy usually doesn't have (thumbs-up, regeneration events, task success) — a v2 concern.

### Benchmarks/platforms beyond RouterBench
- **LLMRouterBench** (arXiv:2601.07206): 400K+ instances, 21 datasets, 33 recent models, 10 routing baselines, unified framework — the current SOTA evaluation harness.
- **RouterArena** (arXiv:2510.00202), **RouteJudge** (arXiv:2606.18774): open comparison platforms, preference-aware evaluation.

## 3. Conversation-level / multi-turn routing — mostly a gap, two real data points

The survey **does not cover multi-turn routing at all**; virtually all papers evaluate single queries. Honest status: stickiness is an engineering concern the academy has barely touched. What exists:

### DialRouter (arXiv:2604.12385, "From Myopic Selection to Long-Horizon Awareness")
The one real multi-turn routing paper. Frames routing as sequential decision-making: state s_t = [dialogue history, current turn], augmented with a **retrieved similar future state** from training data. Training: **MCTS (K=10 simulations) with an LLM user-simulator + checklist reward model** generates expert trajectories → **behavior-clone** into a lightweight policy (encoder → gated fusion with retrieved future → classification head). Crucially its cost model prices **model switching explicitly: same-model consecutive turns get KV-cache-discounted token pricing; a switch re-encodes the whole history at full price** — so the learned policy is naturally sticky but can switch strategically. Results: 83.31% dialogue success, +5.67pp over best single LLM, +3.54pp over myopic per-turn routing. Routing latency **0.03 s/decision** (0.01 s of that is retrieval). Only 750 dialogues/dataset of training data.

### vLLM Semantic Router issue #1439 (https://github.com/vllm-project/semantic-router/issues/1439)
Production confirmation of the failure mode: the router classified **only the last user message**, so "looks good, commit it" after a hard coding turn routed to a cheap model → incoherent conversations. Root causes named: last-message-only classification, no session persistence per request, SessionID/UserID/ConversationHistory fields defined but never populated. Proposed fixes (P1, v0.3): **session-sticky routing keyed on a session-id header — remember the model that served turn 1, keep it for the session**; and/or classify over all user messages, not the last. This is exactly the conversation-id stickiness routsi plans.

Supporting evidence that switching/multi-turn is dangerous: "LLMs Get Lost in Multi-Turn Conversation" (arXiv:2505.06120) — avg **39% performance drop** in multi-turn vs single-turn across top models; arXiv:2603.11394 shows a similar "conversation tax". Neither is about routing per se, but both argue for conservative, sticky defaults and against gratuitous mid-conversation model changes (also: switching destroys upstream prompt/KV caches — DialRouter is the only paper that prices this).

## 4. Serving/proxy architecture reference: vLLM Semantic Router
(https://blog.vllm.ai/2025/09/11/semantic-router.html, https://vllm-semantic-router.com/docs/overview/architecture/system-architecture/)
**Go ExtProc service behind Envoy**: Envoy intercepts the OpenAI-compatible HTTP request, streams it over gRPC to the Go processor, which runs a classification pipeline (Rust candle-based **ModernBERT/mmBERT intent classifiers via CGO**, ~307M params; also jailbreak/PII classifiers and a semantic cache) and returns the backend cluster + rewritten `model` field. Streaming is Envoy's problem — the router only touches the request head. This validates: classify-once-per-request in a Go sidecar/proxy, mutate the model field, let the data plane stream.

## 5. Best quality/simplicity picks for a v1

1. **Embedding + KNN/cluster router (RouterBench/UniRoute recipe)** — embed the first user message (small local model, e.g. MiniLM/bge-small), KNN over a labeled set of prompts with per-model observed win/quality, route to cheapest model above a quality threshold. RouterBench shows KNN matches learned routers; UniRoute shows the same structure absorbs new upstream models without retraining. Overhead ≈ embedding forward pass (few ms) + ANN lookup.
2. **Sticky-by-conversation-id with escalate-only switching** — per vllm-sr #1439 + DialRouter's switch-cost logic: first turn decides the model; later turns re-classify but only override stickiness upward (cheap→strong) when detected difficulty jumps a threshold, never downward mid-conversation. This is the whole practical content of the multi-turn literature; anything fancier (MCTS/BC like DialRouter) is v3.
3. **Tiny intent/difficulty classifier as the alternative head (vllm-sr / LLMRank direction)** — a fine-tuned small BERT-class model mapping request → category → model pool, optionally with LLMRank-style interpretable features for debuggability. More work than KNN (needs labels) but gives explainable routing and constant ~10 ms latency.

Avoid for v1: 1.5B generative routers (Arch-Router — latency), bandits/RL (no reward signal yet), cascades (double-inference latency + judge-error sensitivity per RouterBench).

## Key takeaways
- Survey taxonomy (arXiv:2603.04445): six routing paradigms; production recipe = cheap pre-router + quality verifier + escalation policy; reported routing overheads ~8-30 ms.
- RouterBench (arXiv:2403.12031): KNN over MiniLM embeddings (k=40) matches MLP and learned routers; no 2024 router significantly beat the convex-hull 'Zero router' baseline — keep v1 simple.
- UniRoute (arXiv:2502.08773): represent each upstream model as an error vector on a small representative prompt set + cluster/KNN routing — new models absorbed with zero router retraining.
- LLMRank (arXiv:2510.01234): interpretable hand-engineered features (task type, complexity, syntax, proxy-solver signals) into a neural ranker hits ~89% of oracle utility and is debuggable.
- IRT-Router (arXiv:2506.01048): explicit query-difficulty and model-ability vectors; adding a model = fit one ability vector; semantic-similarity warm-up for cold queries.
- Multi-turn routing is a genuine research gap: the survey ignores it entirely; DialRouter (arXiv:2604.12385) is the only real paper — MCTS+behavior-cloned sequential policy, prices model switches via lost KV-cache, 0.03 s/decision, +3.5pp over per-turn myopic routing.
- vLLM semantic-router issue #1439 documents the exact bug we must avoid: classifying only the last message bounces conversations between models; fix = session-id sticky routing + full-context classification.
- Sticky default is evidence-backed: LLMs already lose ~39% in multi-turn (arXiv:2505.06120), and switching re-encodes history at full cost — v1 rule: first turn picks the model, later turns may only escalate (never downgrade mid-conversation).
- Proxy architecture precedent: vLLM Semantic Router is a Go ExtProc behind Envoy — classify once on the request head, rewrite the model field, let the data plane handle streaming; routing never touches the response stream.
- Best v1 quality/simplicity picks: (1) embedding+KNN router with per-model quality table, (2) conversation-id stickiness with threshold-gated escalate-only switching, (3) optional tiny BERT intent classifier; skip 1.5B generative routers, RL/bandits, and cascades for v1.

## Sources
- https://arxiv.org/html/2603.04445v2
- https://arxiv.org/abs/2403.12031
- https://arxiv.org/html/2403.12031v2
- https://github.com/withmartian/routerbench
- https://arxiv.org/abs/2510.01234
- https://arxiv.org/abs/2506.01048
- https://arxiv.org/abs/2502.08773
- https://arxiv.org/pdf/2506.16655
- https://arxiv.org/html/2604.12385
- https://arxiv.org/abs/2505.06120
- https://arxiv.org/abs/2603.11394
- https://github.com/vllm-project/semantic-router/issues/1439
- https://blog.vllm.ai/2025/09/11/semantic-router.html
- https://vllm-semantic-router.com/docs/overview/architecture/system-architecture/
- https://arxiv.org/html/2601.07206v1
- https://arxiv.org/html/2510.00202v1
- https://arxiv.org/pdf/2606.18774
- https://arxiv.org/pdf/2510.00841
