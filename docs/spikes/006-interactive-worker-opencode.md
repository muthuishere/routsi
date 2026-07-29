# Spike 006: opencode driving an interactive devin worker — full tool-call loop

Run live 2026-07-29 (ghostty-sendkeys automation). Validates the ADR-008/012
minimal slice end-to-end with a real OpenAI client, and kills `devin -p` as the
per-turn mechanism.

## Why `devin -p` per turn is dead

- opencode's system prompt (it embeds every skill description on the machine) is
  far beyond what the devin CLI accepts as a prompt: measured ceiling **~60KB ok,
  ~80KB dies with SIGPIPE** — identically via argv and via `--prompt-file`, so
  it's devin-internal, not ARG_MAX. Piping stdin panics outright
  (`Option::unwrap() on None`).
- Every turn cold-spawns the CLI and re-renders the whole transcript.
- routsi symptom: `signal: broken pipe`, 6/6 errors, opencode retry-looping.

## The better way (owner's call): interactive devin joins as a pull-worker

One long-lived `devin` TUI (ghostty session, `--permission-mode dangerous`,
accept-edits engaged) + a ~70-line driver that:

1. long-polls `GET /v1/workers/devin-live/jobs?wait=25`
2. writes the job (system prompt + transcript + **the 11 opencode tool schemas**)
   to `job-<id>.md` — file handoff, no size limit
3. injects one short line into the TUI ("Read job-….md and follow HOW TO ANSWER")
4. waits for devin to write `answer-<id>.json` — either `{"content": …}` or
   `{"tool_calls":[{"name","arguments"}]}` — re-nudging every 45s (free-plan
   "high demand" throttle needs it)
5. POSTs it to `/v1/workers/devin-live/jobs/<id>`; the server normalizes to
   OpenAI shape (synthesized `call_<id>_<i>`, arguments as JSON string)

## What happened (verbatim sequence)

| # | job | devin's answer | opencode's action |
|---|-----|----------------|-------------------|
| 1 | msgs 2, tools 11 | text greeting | (stray restored message) |
| 2 | msgs 4, tools 11 | `tool_calls: write` (index.html, full 150-line app) | **executed the write in its own cwd** |
| 3 | msgs 6 (incl. tool result), tools 11 | text: "Created index.html with … add, complete, delete … localStorage persistence" | rendered as final answer, turn 1m36s |

The file landed in **opencode's** workdir (devin's cwd is elsewhere) — proof the
proxy client executed the call, not the worker. Wire check earlier in the day:
non-stream response had `finish_reason:"tool_calls"`, string-encoded arguments —
OpenAI SDK-parseable.

## Findings / operational gotchas

1. **The queue path + Result plumbing works with a real client first try** — the
   only failures all night were environmental (below), never the wire format.
2. **Single-threaded driver loses concurrent jobs**: opencode fires title-gen +
   main request together; while the driver babysits one, the other expires at the
   broker's 5-min maxWait. Fix direction: poll-while-waiting, or one queue per
   concurrency slot; interim: the client's retry covers it.
3. Devin free plan throttles ("high demand — message to retry"): the 45s re-nudge
   is mandatory. `--permission-mode dangerous` did not fully suppress edit
   prompts; selecting "accept edits mode" once in-TUI did.
4. TUI automation traps that cost real time: `pkill -f opencode` matched routsi's
   `…/opencode-e2e/models.yaml` argv and killed the server; text+Enter must be
   verified in the box (opencode's update modal silently ate keystrokes);
   sessions restore stray unsent messages.
5. opencode model stickiness: last-used model overrides config `model:`; launch
   with `--model routsi/devin-live`.

## Run 2: sustained multi-round loop (6-step refactor, same day)

Owner wanted ≥5 distinct changes to rule out a one-shot fluke. Task: split the
todo app into style.css/app.js, relink index.html, add a persisted dark-mode
toggle, write README.md, verify with ls — "each step as its own change."

Result: **10 consecutive tool-call rounds (~17 individual calls) across 5 tool
types**, surviving repeated free-plan throttles mid-run via the re-nudge:

```
todowrite → todowrite,read → write → todowrite,write → todowrite,edit,edit
→ todowrite,edit → edit,edit → todowrite,write → todowrite,bash → final text
```

devin used opencode's own plan tool (`todowrite`) unprompted, batched up to 3
calls per round, read before editing, and verified with `bash ls`. All 6 changes
landed on disk (style.css 1.8K, app.js 1.7K with dark-mode + localStorage,
relinked index.html 601B, README.md 933B) and the final text enumerated each
step. The loop is not a demo artifact — it iterates.

## Run 3: codex-live and claude-live workers (same day)

Same pattern replicated for the other two CLIs — the ghostty session manager's
existing `claude`/`codex` descriptors provided the TUIs (their yolo flags baked
in), the driver script was parameterized (queue + spool + workdir args), one
queue each. Both passed the curl tool-call smoke first try, then a 5-step
opencode task ("notes app: index.html, style.css, app.js+localStorage,
README.md, ls verify") each:

| worker | rounds | tool mix | outcome |
|---|---|---|---|
| claude-live | 7 | 4×write, bash, 2×text | 1m35s/turn; **skipped style.css deliberately and said so** in the final answer ("your request had a gap") — honest, not silent |
| codex-live | 14 | 4×write, 6×todowrite, bash, text | all 5 steps incl. style.css; plan-tracked every step with opencode's todowrite; final: "Done. ls verified …" |

Combined with run 2 (devin-live, 10 rounds), **all three CLI agents work as
interactive tool-calling workers behind one routsi instance simultaneously** —
three queues, three ghostty TUIs, three drivers, one proxy.

Additional gotchas this round:

- **opencode TUIs are near-unkillable by pattern**: `pkill -f opencode` risks
  routsi (config path contains "opencode"), `ps | grep` missed the live PID
  (column truncation); the reliable kill is walking the ghostty session's
  process tree (ghostty pid → login → zsh → child). A leftover instance eats
  every injected keystroke as chat input — the #1 automation failure all night.
- **opencode "external directory" permission dialogs ignore injected keys**
  (enter/tab do nothing). Avoid triggering them: launch opencode from a real
  project root, and beware symlinked cwds (`~/oce` → /private/tmp resolved as
  external). Session state lives in `~/.local/share/opencode/opencode.db` —
  moving it aside (auth.json untouched) resets sticky model + restored sessions.
- opencode global-continues the last session even across cwds when a queued
  message is pending; `/new` can't be typed into a modal.

## Verdict

Interactive-agent-as-worker is the right architecture for CLI agents behind
routsi: no prompt-size ceiling, warm session, native context. Promote the driver
from spike script to a `routsi worker` mode (or extend the routsi-worker skill)
per ADR-005; the ADR-012 fields it needs are now shipped.
