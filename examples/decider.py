#!/usr/bin/env python3
"""Example external decider for routsi's `auto` routing (docs/adr/007).

Contract:
  stdin  <- one JSON object:
    {
      "model": "auto",
      "conversation_id": "...",
      "messages": [{"role": "user", "content": "..."}, ...],
      "levels": ["low", "medium", "high"],
      "tiers": {"low": "gpt-cheap", "high": "gpt-strong"}  # the auto tiers map, for context
    }
  stdout -> exactly one JSON object:
    {"level": "low" | "medium" | "high"}

Any crash, timeout, or malformed output makes routsi fall back to its
built-in Rules scorer for that request -- so it's safe to iterate on the
heuristic below without risking a broken proxy.

Point routsi at this file:
  decider:
    command: "python3 examples/decider.py"
    timeout: 3s

Stdlib only -- no dependencies to install.
"""
import json
import re
import sys

HIGH_HINTS = re.compile(
    r"\b(prove|derive|theorem|refactor|architect|debug|optimi[sz]e|"
    r"step[- ]by[- ]step|reason|analy[sz]e|why does|explain in detail)\b",
    re.IGNORECASE,
)
MEDIUM_HINTS = re.compile(
    r"\b(explain|compare|summari[sz]e|implement|design|plan|review|translate|convert)\b",
    re.IGNORECASE,
)


def decide(req: dict) -> str:
    """Mirrors the spirit of routsi's built-in Rules
    (internal/router/router.go) so this is a real starting point:
      - a code fence or "debug/refactor/prove/optimize"-style language
        usually means real work -> high
      - "explain/compare/summarize/implement"-style asks are mid-weight
        -> medium
      - sheer length is a weak-but-useful signal on both ends
      - everything else (short chit-chat, simple factual asks) -> low

    Rewrite this function freely -- it's the whole point of the decider hook.
    """
    messages = req.get("messages") or []
    last_user = ""
    for msg in reversed(messages):
        if msg.get("role") == "user":
            last_user = str(msg.get("content", ""))
            break

    if "```" in last_user or HIGH_HINTS.search(last_user) or len(last_user) > 1500:
        return "high"
    if MEDIUM_HINTS.search(last_user) or len(last_user) > 400:
        return "medium"
    return "low"


def main() -> None:
    level = "low"  # safe default if anything below throws
    try:
        req = json.loads(sys.stdin.read())
        level = decide(req)
    except Exception as exc:  # noqa: BLE001 - keep the decider alive no matter what
        # Falls through to "low" -- routsi treats any bad output as a
        # decider failure anyway, so this is just for a clean local run.
        print(f"decider.py: {exc}", file=sys.stderr)
    sys.stdout.write(json.dumps({"level": level}))


if __name__ == "__main__":
    main()
