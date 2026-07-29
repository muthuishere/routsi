# routsi

One OpenAI-compatible endpoint that routes each request to the right **model or
agent** — API models (OpenRouter, OpenAI, DeepSeek, …) *and* local agent CLIs
(Devin, Codex, Copilot, Claude Code) — with per-task routing, per-conversation
stickiness, a live dashboard, and metrics. A single Go binary; point any OpenAI SDK
at it.

## Quick start

```sh
task build                       # -> bin/routsi
cp models.yaml ~/.config/routsi/ # or keep ./models.yaml
routsi serve                     # listens on :8080 by default
```

### Install via npm

```sh
npm install -g @muthuishere/routsi
```

`postinstall` best-effort prefetches the prebuilt `routsi` binary for your OS/arch
from [GitHub Releases](https://github.com/muthuishere/routsi/releases) — no Go
toolchain needed. The launcher is self-healing: if postinstall didn't run (blocked
by `allow-scripts`, offline, whatever), the first `routsi` invocation fetches the
binary itself, so the command always works either way. See
[`docs/adr/006-npm-distribution.md`](docs/adr/006-npm-distribution.md) for how it
works.

Point any OpenAI client at `http://localhost:8080/v1`. Then:

```sh
curl localhost:8080/v1/chat/completions -H 'content-type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"hello"}]}'
```

- **`model: "auto"`** — routsi classifies the task and picks a tier.
- **`model: "dynamic-1"`** — a named group you define (low/medium/high members).
- **`model: "openrouter/anthropic/claude-haiku-4.5"`** — a concrete model, no routing.
- **`model: "devin/opus"`** — an agent as a model.

#### Custom decider

`auto`/dynamic routing normally classifies with the built-in `Rules`
heuristic. To plug in your own routing brain, add a `decider:` block to
`models.yaml`:

```yaml
decider:
  command: "node examples/decider.js"   # any executable/runtime, run via `sh -c`
  timeout: 3s                            # optional, default 3s
```

routsi writes a JSON request to the command's stdin
(`model`/`conversation_id`/`messages`/`levels`/`tiers`) and reads
`{"level": "low"|"medium"|"high"}` back from stdout. Any failure — crash,
timeout, non-zero exit, malformed/empty/unknown output — falls back to the
built-in `Rules` decision, so a broken decider never fails a request. Leaving
`decider.command` empty (the default) keeps today's behavior unchanged. See
[`examples/`](examples/README.md) for the full contract and copyable Node/
Python starting points, and [ADR-007](docs/adr/007-external-decider.md) for
the design.

### Forwarding to an agent model (devin/…)

Agent CLIs expose their inner models as `<agent>/<model>` catalog entries — from
`variants:` or discovery (`GET /v1/models` lists them all). Just name one:

```sh
curl localhost:8080/v1/chat/completions -H 'content-type: application/json' \
  -H 'X-Conversation-Id: my-thread-1' \
  -d '{"model":"devin/claude-opus-4.8","messages":[{"role":"user","content":"refactor plan?"}]}'
```

`devin/claude-opus-4.8` runs `devin --model claude-opus-4.8`; the conversation id maps
to a real Devin session (`-r` resume on later turns). Bare `devin` uses the default
model. Same pattern for `codex/…`, `copilot/…`, `claude/…` (one-shot). A dynamic
group level can point at an agent too: `high: devin/claude-opus-4.8`.

## Auth (token or mTLS)

Generate tokens with the built-in CLI:

```sh
routsi token -n 2      # prints rtk_... tokens + setup instructions
```

For mTLS, generate the whole trust setup (CA + server + client certs) in one command:

```sh
routsi certs                        # -> ~/.config/routsi/certs/ (keys 0600)
routsi certs -host myhost.example   # extra server SANs
```

```yaml
auth:
  tokens_env: ROUTSI_TOKENS   # export ROUTSI_TOKENS="tok1,tok2" — enables bearer auth
tls:
  cert: server.crt            # cert+key = HTTPS
  key: server.key
  client_ca: clients-ca.crt   # + client_ca = mTLS (client certs required)
```

With tokens on: `/v1/*`, `/stats`, `/metrics` need `Authorization: Bearer <tok>`
(OpenAI SDKs send this automatically as the api_key); dashboard: `/?token=tok`.
`/health` stays open. Token values live in env vars, never in the config file.

**Secrets via `.env`:** on startup routsi layers env vars, highest priority first:
process env > `-env`/`ROUTSI_ENV_FILE` > `./.env` > `~/.config/routsi/.env`. A file
never overrides a higher source and values are never logged — so provider keys and
`ROUTSI_TOKENS` can live in whichever `.env` suits you instead of your shell profile.
Keep the file `chmod 600` and out of version control.

The chosen model is returned in the `X-Selected-Model` header. Every response
carries a `usage` block.

## Pull-workers: let anyone plug in their agent

Instead of routsi hosting an agent CLI, a **remote worker** can join and answer — run
your own opencode/codex/claude anywhere, register a queue, answer questions the proxy
routes to it. Credentials never leave your machine; the worker is outbound-only.

```sh
# on the worker machine (any machine with your agent logged in):
routsi worker run --proxy https://your-proxy:8080 --queue my-agent \
  --agent 'codex exec --skip-git-repo-check -'    # reads the question on stdin, prints the answer
```

Now `{"model":"my-agent"}` routes to that worker. It's a normal model — shows in
`/v1/models`, `GET /v1/workers` (live status: state, last-seen, served, errors), and the
dashboard Workers panel. If no worker is live, requests fast-fail with `503`. No worker
auth in v1 (reserved `workers.auth` config placeholder). `routsi worker scaffold` prints
an editable curl-only version.

**Two ways to serve a queue:**

- **Headless subprocess** (above) — `routsi worker run --agent 'cmd'` shells each
  question to `cmd` and posts back its stdout.
- **Agent-driven** — a *running* agent session (Claude Code, codex, opencode) becomes the
  worker itself and answers with its own reasoning, no subprocess in the middle, using
  three helper subcommands that share the same register/poll/answer flags:

  ```sh
  routsi worker register --proxy URL --queue NAME             # one-shot register
  routsi worker poll --proxy URL --queue NAME --wait 25        # one long-poll; prints a job or nothing
  printf '%s' "the answer" | routsi worker answer --proxy URL --queue NAME --id JOBID
  ```

  `poll` prints `{"id","prompt","messages"}` for one job and exits 0, or prints nothing on
  an idle wait window (also exit 0) — an agent loops register once, then poll → compose the
  answer itself → answer, repeating until told to stop.

Install the worker as an **agent skill** so any agent session can become a worker — the
skill walks the agent through the agent-driven loop above (and documents the headless
mode as an alternative):

```sh
routsi install --skills     # into ~/.claude/skills and ~/.codex/skills
```

## Tool calling through workers — use a live agent as an OpenAI model

The queue path speaks **OpenAI function calling**: a request's `tools` ride along in
the job, and the worker may answer with `tool_calls` instead of text —
`{"tool_calls":[{"name":"write","arguments":{...}}]}` posted to the answer endpoint
becomes a wire-correct response (`finish_reason:"tool_calls"`, string-encoded
arguments, generated call ids). The *client* executes the calls and sends the results
back as the next request; the worker only decides.

That makes a **long-lived interactive agent TUI** (devin, Claude Code, codex) a
first-class model for any OpenAI client. Real session — opencode using devin through
routsi (`examples/interactive-worker/`, evidence in `docs/spikes/006-*`):

```
you      ▶ opencode --model routsi/devin-live
you      ▶ "Refactor into separate files and extend, step by step: split CSS/JS out
            of index.html, add a dark-mode toggle with localStorage, README, verify."

devin    ◀ tool_calls: todowrite              (plans in opencode's own todo tool)
devin    ◀ tool_calls: todowrite, read        (reads index.html before touching it)
devin    ◀ tool_calls: write                  (style.css)
devin    ◀ tool_calls: todowrite, write       (app.js)
devin    ◀ tool_calls: todowrite, edit, edit  (relinks index.html — 3 calls, one round)
devin    ◀ tool_calls: edit, edit             (dark-mode toggle)
devin    ◀ tool_calls: todowrite, write       (README.md)
devin    ◀ tool_calls: todowrite, bash        (ls to verify)
devin    ◀ "Refactoring complete! All 6 steps finished successfully ✅ …"
```

Ten rounds, ~17 client-executed tool calls; opencode did every file operation in its
own workspace — devin never touched the client's files. The same pattern runs
`claude-live` and `codex-live` queues side by side on one proxy. Why interactive
beats one-shot CLI backends for this: no prompt-size ceiling (the job is handed over
as a file — one-shot `devin -p` dies near ~80KB, smaller than opencode's system
prompt), a warm session instead of a cold spawn per turn, and tool calls, which the
one-shot backends don't emit today. Setup and the reference driver:
`examples/interactive-worker/README.md`.

## Run it as a service (watchdog)

Keeps routsi up across crashes and logins — launchd on macOS, systemd-user on Linux:

```sh
routsi install     # install + start (restart-on-failure, start-at-login)
routsi status      # is it running?
routsi uninstall   # stop + remove
```

No root; everything lives under your home dir. macOS logs: `~/Library/Logs/routsi.log`.

## Dashboard & metrics

- **`http://localhost:8080/`** — live dashboard (requests, tokens, latency, routing
  split, escalations), auto-refreshing, self-contained.
- **`/stats`** — JSON snapshot.
- **`/metrics`** — Prometheus text (scrape it).

## Config (`models.yaml`)

See the commented [`models.yaml`](models.yaml). Highlights:

- `type: forward` — any OpenAI-compatible upstream (raw byte passthrough).
- `type: devin|codex|copilot|claude` — local agent CLIs (must be installed + logged in).
- `type: dynamic` — a virtual model with `levels: {low, medium, high}`.
- `variants:` / `discover_models: true` — expand one entry into many models. Forwards
  fetch upstream `GET /models`; Devin is probed live; codex/copilot/claude read
  `~/.config/routsi/known-models.json` (editable).

## How it differs from LiteLLM / OpenRouter / other gateways

Gateways route between *providers serving the same model* (load-balance, fallback).
routsi routes between *fundamentally different answerers* — API models **and full
agents** — behind one OpenAI face, choosing by task, sticky per conversation. It's a
single dependency-light binary you run yourself, not a platform.

## Design notes

Routing/conversation decisions are grounded in `docs/research/`. Not yet built:
inbound auth, tool-call passthrough on enveloped paths, escalation memory handoff,
compaction. See `CLAUDE.md` for current state.
