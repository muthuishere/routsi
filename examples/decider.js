#!/usr/bin/env node
// Example external decider for routsi's `auto` routing (docs/adr/007).
//
// Contract:
//   stdin  <- one JSON object:
//     {
//       "model": "auto",
//       "conversation_id": "…",
//       "messages": [{"role":"user","content":"…"}, ...],
//       "levels": ["low","medium","high"],
//       "tiers": {"low":"gpt-cheap","high":"gpt-strong"}   // the auto tiers map, for context
//     }
//   stdout -> exactly one JSON object:
//     {"level": "low" | "medium" | "high"}
//
// Any crash, timeout, or malformed output makes routsi fall back to its
// built-in Rules scorer for that request — so it's safe to iterate on the
// heuristic below without risking a broken proxy.
//
// Point routsi at this file:
//   decider:
//     command: "node examples/decider.js"
//     timeout: 3s

let raw = "";
process.stdin.on("data", (chunk) => { raw += chunk; });
process.stdin.on("end", () => {
  let level = "low"; // safe default if anything below throws
  try {
    const req = JSON.parse(raw);
    level = decide(req);
  } catch (err) {
    // Falls through to "low" — routsi treats any bad output as a decider
    // failure anyway, so this catch is just for a clean local run.
    process.stderr.write(`decider.js: ${err}\n`);
  }
  process.stdout.write(JSON.stringify({ level }));
});

// decide() mirrors the spirit of routsi's built-in Rules
// (internal/router/router.go) so this is a real starting point, not a toy:
//   - a code fence or "debug/refactor/prove/optimize"-style language usually
//     means real work -> high
//   - "explain/compare/summarize/implement"-style asks are mid-weight -> medium
//   - sheer length is a weak-but-useful signal on both ends
//   - everything else (short chit-chat, simple factual asks) -> low
//
// Rewrite this function freely — it's the whole point of the decider hook.
function decide(req) {
  const messages = Array.isArray(req.messages) ? req.messages : [];
  const lastUser = [...messages].reverse().find((m) => m.role === "user");
  const text = (lastUser && String(lastUser.content)) || "";

  const highHints = /\b(prove|derive|theorem|refactor|architect|debug|optimi[sz]e|step[- ]by[- ]step|reason|analy[sz]e|why does|explain in detail)\b/i;
  const mediumHints = /\b(explain|compare|summari[sz]e|implement|design|plan|review|translate|convert)\b/i;

  if (text.includes("```") || highHints.test(text) || text.length > 1500) {
    return "high";
  }
  if (mediumHints.test(text) || text.length > 400) {
    return "medium";
  }
  return "low";
}
