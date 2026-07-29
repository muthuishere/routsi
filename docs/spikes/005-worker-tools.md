# Spike 005: tools through the pull-worker queue

De-risks **ADR-012**. Status: **planned** — low technical risk (all code is ours,
wire-additive), so this spike is a thin end-to-end proof, not research.

## Question

Does an agent-driven worker (Mode A, `routsi worker register|poll|answer`) round-trip
an OpenAI tool call cleanly: see the `tools` in the job, answer with `tool_calls`,
and continue when the client posts the tool result?

## Why it should work (and what could still bite)

- The worker *is* a live agent session — it understands tool schemas natively; no
  emulation layer needed (unlike ADR-011). The changes are pure plumbing:
  `queue.Job` + answer body + `result` channel (see ADR-012 for exact sites).
- Bites to check:
  1. **Old-worker compatibility** — a v0.1.4 worker polling a job that carries
     `tools` must not choke on unknown JSON fields (Go decode is lenient; verify
     the skill's jq/parse path is too).
  2. **finish_reason plumbing** — the enveloped response must say `tool_calls`,
     which lands only after ADR-008's builders; a temporary shim would lie to SDKs.
  3. **Conversation continuity** — turn 2 (tool result) is a *new job*; the worker
     must receive the full transcript including its own prior `tool_calls` turn,
     or it will re-issue the same call. Job messages must carry ADR-008's extended
     `Message` fields.
  4. **at-most-once** — if the worker dies after emitting `tool_calls`, the client
     holds a call id nothing will ever consume; confirm the fast-503 path gives the
     client a clean failure on turn 2.

## Experiment plan

1. Branch with the additive fields (job `tools`, answer `tool_calls`) behind no
   config — spike code, not committed to main (per workflow, ADR-012 is Proposed).
2. `routsi serve` + a scripted worker that answers any `tools` job with a fixed
   `get_weather` call; drive with the OpenAI Python SDK (`tools=[...]`) — the SDK
   is the oracle: `response.choices[0].message.tool_calls` must parse.
3. Post the tool result as turn 2; assert the worker's job 2 transcript contains
   the assistant `tool_calls` turn + `tool` turn; final answer reaches the SDK.
4. Re-run turn 1 against an *unmodified* v0.1.4 worker binary → must still answer
   plain text without error (compat check #1).

## Success criteria

OpenAI SDK completes a two-turn tool exchange against a queue model unmodified;
old worker binary unaffected; `GET /v1/workers` shows the `tools` capability.
