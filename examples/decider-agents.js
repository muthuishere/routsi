#!/usr/bin/env node
// Custom decider that routes between AGENTS (docs/opencode.md, ADR-007).
//
// Pair it with a dynamic group whose levels map to agent models/queues:
//
//   decider:
//     command: "node examples/decider-agents.js"
//   models:
//     - name: auto-agent
//       type: dynamic
//       levels:
//         low: claude-fast     # quick Q&A -> cheap one-shot claude
//         medium: codex        # code edits -> codex
//         high: devin-live     # multi-step build work -> live devin worker
//
// Same contract as decider.js: request JSON on stdin, {"level": "..."} on
// stdout, any failure falls back to routsi's built-in rules.

let raw = "";
process.stdin.on("data", (c) => { raw += c; });
process.stdin.on("end", () => {
  let level = "low";
  try {
    const req = JSON.parse(raw);
    const last = [...(req.messages || [])].reverse()
      .find((m) => m.role === "user");
    const text = (typeof last?.content === "string"
      ? last.content
      : JSON.stringify(last?.content || "")).toLowerCase();

    // Multi-step build/refactor work -> the interactive agent.
    const heavy = ["refactor", "implement", "build", "migrate", "step by step",
      "create a", "add feature", "fix the bug", "write tests"];
    // Single code edits / snippets -> codex.
    const codey = ["```", "function", "error:", "stack trace", "compile",
      "regex", "sql", "diff"];

    if (heavy.some((w) => text.includes(w)) || text.length > 2000) level = "high";
    else if (codey.some((w) => text.includes(w))) level = "medium";
  } catch (_) { /* fall through to low; routsi falls back on bad output too */ }
  process.stdout.write(JSON.stringify({ level }) + "\n");
});
