#!/usr/bin/env node
// Minimal native-tools adapter for routsi (ADR-013, `type: command`).
//
// Shows the whole contract in one screen: read the job, decide, answer. This
// one answers with a real OpenAI tool_call when the caller offers a matching
// tool, and with plain text otherwise.
//
//   models.yaml:
//     - name: demo-adapter
//       type: command
//       command: node ./examples/adapters/echo-tools.js
//       tools: native
//
// Answer shapes routsi accepts on stdout:
//   {"content": "..."}                                     plain answer
//   {"tool_calls": [{"name": "x", "arguments": {...}}]}     simplified
//   {"tool_calls": [{"id": "...", "type": "function",       full OpenAI shape
//                    "function": {"name": "x", "arguments": "{\"a\":1}"}}]}
//   anything not a JSON object                              taken as answer text
//
// routsi synthesizes ids and re-encodes arguments, so the simplified shape is
// enough — you never have to remember that OpenAI stores arguments as a string.

let buf = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (d) => (buf += d));
process.stdin.on("end", () => {
  const job = JSON.parse(buf);

  // job.tools is the caller's OpenAI tool array, verbatim (absent if none).
  const tools = job.tools || [];
  const weather = tools.find((t) => t?.function?.name === "get_weather");

  if (weather && /weather|temperature|forecast/i.test(job.prompt)) {
    const city = (job.prompt.match(/in ([A-Z][a-z]+)/) || [])[1] || "Paris";
    process.stdout.write(
      JSON.stringify({ tool_calls: [{ name: "get_weather", arguments: { city } }] }),
    );
    return;
  }

  // Also available without parsing the job at all:
  //   $ROUTSI_MODEL $ROUTSI_UPSTREAM_MODEL $ROUTSI_CONVERSATION_ID
  //   $ROUTSI_JOB_ID $ROUTSI_STREAM
  process.stdout.write(
    JSON.stringify({
      content:
        `adapter ${process.env.ROUTSI_MODEL} saw ${job.messages.length} message(s) ` +
        `and ${tools.length} tool(s); conversation=${job.conversation_id || "none"}`,
    }),
  );
});
