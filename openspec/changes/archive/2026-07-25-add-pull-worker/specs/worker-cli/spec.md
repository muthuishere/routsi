## ADDED Requirements

### Requirement: `routsi worker run` loop

The routsi CLI SHALL provide a `worker run` subcommand that turns any prompt-in/answer-out
agent into a queue worker. It SHALL accept `--proxy <url>`, `--queue <name>`, and
`--agent '<command>'` (and an optional `--token`, accepted but ignored in v1). It SHALL
register the queue once, long-poll for jobs, render each job's messages to a prompt piped to
the `--agent` command on stdin, capture the command's stdout, and post it back as the answer
(ADR-005).

#### Scenario: Worker registers then serves a job
- **WHEN** `routsi worker run --proxy URL --queue NAME --agent 'cmd'` starts and a job arrives
- **THEN** it registers the queue once, receives the job on its long-poll, pipes the rendered
  prompt to `cmd` on stdin, and posts `cmd`'s stdout back to `POST /v1/workers/{name}/jobs/{id}`

#### Scenario: Token flag accepted but not required in v1
- **WHEN** `routsi worker run` is invoked with or without `--token`
- **THEN** it runs identically, since v1 imposes no worker authentication

### Requirement: Worker loop fails loud

The `routsi worker run` loop SHALL fail loudly rather than silently spin: on a hard error it
SHALL print an actionable cause and exit non-zero, and SHALL print human-readable status for
each step (registered, answered job with elapsed time) (ADR-004/005).

#### Scenario: Actionable failure and non-zero exit
- **WHEN** the loop hits a hard, non-transient error talking to the proxy (e.g. a rejected
  registration)
- **THEN** it prints the actionable cause and exits with a non-zero status instead of spinning silently

#### Scenario: Per-step human status
- **WHEN** the loop registers and then answers a job
- **THEN** it prints a registered confirmation and an answered-job line including the elapsed time

### Requirement: `routsi worker scaffold`

The routsi CLI SHALL provide a `worker scaffold` subcommand that emits an editable,
curl-only shell script implementing the same register → long-poll → answer contract, for
operators who prefer to hack the loop rather than run the built-in one (ADR-005).

#### Scenario: Scaffold emits an editable script
- **WHEN** an operator runs `routsi worker scaffold`
- **THEN** the CLI outputs a self-contained shell script (curl-only) that registers a queue,
  long-polls for jobs, runs a configurable agent command, and posts answers back
