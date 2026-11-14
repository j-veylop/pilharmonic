# Executor contract

An executor runs one attempt of a task. The mandate, thread context and history belong to the service; adapters must not mutate them or infer authority from context.

## Interfaces

- `Runner` runs one attempt; `LiveRunner` also accepts follow-ups while running. A live acknowledgement means the input was queued as the next user message, not that the model used it.
- `ProfileSupport` (`SupportsProfile(Profile) error`) declares which profile parameters the adapter honors. The service validates the recorded profile before starting, so an adapter that cannot set a model or restrict tools fails the start. Without it, only trusted instructions and default parameters are accepted.
- `CapabilityReporter` declares tool activity and structured reports (off unless declared).
- The output sink may implement `ReportRecorder` (`RecordReport(kind, message)`) and `UsageRecorder` (`RecordUsage(model, usage)`). Both are accepted only while the attempt runs, are bound to its ID, and are never derived from assistant prose. Zero-token usage is ignored; no report means no usage record.

The service, not the adapter, decides delegation credentials and integration from the recorded profile.

## Fantasy adapter

`FantasyRunner` drives providers in-process through [Fantasy](https://charm.land/fantasy). It implements all interfaces above. The service resolves the attempt's `provider/model` selector, records it on the attempt and passes it with the task; the selector resolves to an OpenAI-compatible endpoint with a key from `credentials.json` (via `/login`) or the provider's environment variable (`OPENAI_API_KEY`, `DEEPSEEK_API_KEY`, `OPENROUTER_API_KEY`, `GROQ_API_KEY`, `HYPER_API_KEY`). `bedrock` uses Fantasy's AWS provider with `AWS_BEARER_TOKEN_BEDROCK` or the AWS credential chain. An attempt with no model fails with an explanatory error. Credentials never enter task state.

It exposes seven workspace tools (`read`, `bash`, `edit`, `write`, `grep`, `find`, `ls`) and a trusted system instruction to prefer them over shell. `bash` is the only shell path and runs in the execution workspace. Unknown tool names are ignored; profile and specialist limits narrow the palette. Empty results (e.g. grep with no matches) are answers, not failures.

Reasoning deltas become display-only `[thinking]`/`[/thinking]` markers; an open block is closed before prose or tool activity. The adapter reports progress on accepting the prompt, `waiting` on provider retry, and `failed` on an error, content filter, or empty response. Usage is each stream call's `TotalUsage`.

## Command adapters

`CommandRunner` runs one trusted, configured process in the task workspace with explicit arguments (never shell interpolation). It receives the mandate, role instructions and pre-start context on stdin and streams stdout/stderr as output. It refuses tool and model selection and does not support live follow-ups. Startup, cancellation, output-write failure and nonzero exit produce observable outcomes; a zero exit is execution success, not verification.

## Workspaces and environment

`Task.Workspace` is the execution directory. For isolated tasks it is a Git worktree from committed `HEAD`, reused on retry; adapters must not clean, reset or relocate it, and must not assume it stays at its initial commit.

Agent processes receive an allowlisted environment (`PATH`, `HOME`, `LANG`, `LC_*`, `TERM`, `SHELL`, `TMPDIR`, `TMP`, `TEMP`, `ORCHESTRATOR_*`). A live attempt may receive subagent credentials (`ORCHESTRATOR_CONTROL_SOCKET`, `ORCHESTRATOR_TASK_ID`, `ORCHESTRATOR_SUBAGENT_TOKEN`, `ORCHESTRATOR_SUBAGENT_CLI`); adapters pass them to agent-run code and never log or persist the token. Adapters that cannot propagate the environment do not expose delegation.

Agent selection applies only to a queued task without an outstanding start request, is persisted in history, and each attempt records the agent that ran it. Configure the registry before resuming persisted starts; a missing agent is never silently replaced by `general`.

## Subagent helper

`pilharmonic subagent` is the only way for a running agent to delegate. It reads the child mandate from stdin (max 256 KiB), talks only to the owner-only control socket, and never executes output or a shell.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-title` | `""` | display title for the child |
| `-agent` | `""` | child executor; automatic selection, then the parent executor |
| `-profile` | `""` | child profile; defaults to the parent profile |
| `-socket` | `ORCHESTRATOR_CONTROL_SOCKET` | control socket path |
| `-no-wait` | `false` | record and start without waiting; prints the child ID |
| `-start-timeout` | `5s` | wait for a free execution slot |
| `-timeout` | `30m` | maximum wait for completion |

Exit codes: `0` success, `1` recorded failure or wait timeout, `2` usage or protocol error. Without `-no-wait` it prints the child's final status and output.

## Testing

Exercise protocol and lifecycle failures with fake processes and no credentials; `FantasyRunner` accepts an injected model factory for offline tests.
