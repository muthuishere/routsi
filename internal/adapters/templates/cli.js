#!/usr/bin/env node
// Generic CLI adapter for routsi (ADR-013, `type: command`).
//
// Turns any "prompt in, text out" command-line agent into an OpenAI-addressable
// model. This one file replaces the deprecated built-in devin/codex/copilot/
// claude types (ADR-014) — pick the agent with ADAPTER_CLI.
//
//   models.yaml:
//     - name: claude-oneshot
//       type: command
//       command: ADAPTER_CLI=claude node ./examples/adapters/cli.js
//       tools: emulated          # routsi folds the tool manifest into the prompt
//
// Contract: a JSON job arrives on stdin, the answer goes to stdout. Printing
// anything that isn't a JSON object means "this is the answer text", which is
// why the last line is just a write of the CLI's own output.
//
// Zero dependencies — node stdlib only.

const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

// How to invoke each agent. `prompt` is how it wants the prompt — this is NOT
// cosmetic, it was measured (docs/spikes/007):
//   "stdin" — prompt piped in. Preferred: no size limit.
//   "file"  — prompt written to a temp file passed via `promptFlag`. Use this
//             wherever the agent supports it: it dodges both the argv ceiling
//             and any shell-quoting trouble.
//   "argv"  — prompt appended as the last argument. Last resort: subject to the
//             ~80KB argv ceiling measured in spike 006 (SIGPIPE on big
//             prompts), and real system prompts exceed it.
//
// devin additionally *panics* on an open stdin it never reads
// (repl_mode.rs:478, unwrap on None), so non-stdin modes close it.
//
// Add your own agent here (openclaw, opencode, an in-house script) — no routsi
// release required, which is the entire point of the adapter contract.
const AGENTS = {
  claude: (model) => ({
    bin: "claude",
    args: ["-p", ...(model ? ["--model", model] : [])],
    prompt: "stdin",
  }),
  copilot: (model) => ({
    bin: "copilot",
    args: [...(model ? ["--model", model] : []), "--log-level", "none", "--no-color", "-p"],
    prompt: "argv",
  }),
  codex: (model) => ({
    bin: "codex",
    args: ["exec", "--skip-git-repo-check", ...(model ? ["-m", model] : [])],
    prompt: "stdin",
  }),
  // -p/--print is devin's non-interactive switch, NOT the prompt: the prompt
  // comes from --prompt-file, which is why this is "file" and not "argv".
  devin: (model) => ({
    bin: "devin",
    args: [...(model ? ["--model", model] : []), "-p"],
    prompt: "file",
    promptFlag: "--prompt-file",
  }),
  openclaw: (model) => ({
    bin: "openclaw",
    args: ["run", ...(model ? ["--model", model] : [])],
    prompt: "stdin",
  }),
};

function readStdin() {
  return new Promise((resolve) => {
    let buf = "";
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (d) => (buf += d));
    process.stdin.on("end", () => resolve(buf));
  });
}

function run({ bin, args, prompt: mode, promptFlag }, prompt) {
  let argv = args;
  let tmp = null;
  if (mode === "argv") {
    argv = [...args, prompt];
  } else if (mode === "file") {
    tmp = path.join(
      os.tmpdir(),
      `routsi-prompt-${process.env.ROUTSI_JOB_ID || process.pid}.txt`,
    );
    fs.writeFileSync(tmp, prompt, "utf8");
    argv = [...args, promptFlag, tmp];
  }

  return new Promise((resolve, reject) => {
    const child = spawn(bin, argv, { stdio: ["pipe", "pipe", "pipe"] });
    let out = "";
    let err = "";
    child.stdout.on("data", (d) => (out += d));
    child.stderr.on("data", (d) => (err += d));
    const done = (fn) => (arg) => {
      if (tmp) {
        try {
          fs.unlinkSync(tmp);
        } catch {
          /* best effort */
        }
      }
      fn(arg);
    };
    child.on("error", done(reject));
    child.on("close", (code) =>
      code === 0
        ? done(resolve)(out.trim())
        : done(reject)(new Error(`${bin} exited ${code}: ${err.trim().slice(0, 500)}`)),
    );
    if (mode === "stdin") {
      child.stdin.write(prompt);
    }
    // Always close: devin panics on an open stdin it never reads.
    child.stdin.end();
  });
}

(async () => {
  const job = JSON.parse(await readStdin());

  const name = process.env.ADAPTER_CLI || "claude";
  const build = AGENTS[name];
  if (!build) {
    console.error(`unknown ADAPTER_CLI=${name}; known: ${Object.keys(AGENTS).join(", ")}`);
    process.exit(2);
  }

  // upstream_model is routsi's `upstream_model:` / the variant suffix, i.e. the
  // agent's own --model. Empty means "whatever the agent defaults to".
  const spec = build(job.upstream_model || "");

  try {
    // job.prompt is the rendered transcript — in `tools: emulated` mode it also
    // carries the tool manifest, so nothing extra is needed here.
    const text = await run(spec, job.prompt);
    process.stdout.write(text);
  } catch (e) {
    console.error(String(e.message || e));
    process.exit(1);
  }
})();
