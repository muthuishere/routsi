# External decider examples

routsi's `auto` routing normally uses the built-in `Rules` heuristic
(`internal/router/router.go`). You can replace it with your own routing brain
— any executable/runtime — via the `decider:` config block (see
[docs/adr/007-external-decider.md](../docs/adr/007-external-decider.md)):

```yaml
decider:
  command: "node examples/decider.js"   # any command; run via `sh -c`
  timeout: 3s                            # optional, default 3s
  cwd: ""                                # optional, default: routsi's process cwd
```

Leave `decider.command` empty (or omit the block) to keep the built-in Rules
scorer — that's the default, unchanged behavior.

## The contract

For each `auto` request, routsi spawns `decider.command`, writes one JSON
object to its **stdin**, and reads one JSON object back from its **stdout**.

**stdin:**

```json
{
  "model": "auto",
  "conversation_id": "…",
  "messages": [{"role": "user", "content": "…"}],
  "levels": ["low", "medium", "high"],
  "tiers": {"low": "gpt-cheap", "high": "gpt-strong"}
}
```

- `messages` is the current turn's message list (same signal `Rules` gets via
  `LastUserText()` — the last user message is what matters).
- `levels` is always the three level names, in escalation order.
- `tiers` is your `tiers:` config map (the models `auto` can route to), given
  as context — the decider doesn't have to resolve a model itself, just a
  level.

**stdout** — exactly one JSON object:

```json
{"level": "low"}
```

`level` must be `"low"`, `"medium"`, or `"high"`.

## Fail-safe

Any decider misbehavior — it doesn't start, times out, exits non-zero,
prints nothing, prints something that isn't valid JSON, or returns a level
routsi doesn't recognize — makes routsi fall back to the built-in `Rules`
decision for that request and log a short warning (never your prompt text,
never a secret). A broken decider degrades routing quality; it never fails
the request.

## Examples here

- [`decider.js`](decider.js) — Node, zero dependencies.
- [`decider.py`](decider.py) — Python, stdlib only.

Both implement the same heuristic as the built-in `Rules` (code fences /
keyword hints / length), heavily commented, as a real starting point to
rewrite rather than a toy. Both are executable (`chmod +x` already applied,
shebang present) if you'd rather point `command` straight at the file.

## Caveat

v1 spawns a fresh process per `auto` request — fine for a Node/Python script,
a real cost for something with slow startup (e.g. a JVM). See
[docs/adr/007](../docs/adr/007-external-decider.md) for the tradeoff and the
planned persistent-process upgrade path.
