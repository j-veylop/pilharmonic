# Executor contract

An executor runs one attempt of a task. The task's mandate, thread context and execution history belong to the service. Adapters must not mutate them or infer additional authority from context.

The built-in Fantasy adapter (`FantasyRunner` in `internal/orchestrator/fantasy_runner.go`) drives providers in-process through [Fantasy](https://charm.land/fantasy); there is no external agent binary to install. It implements `Runner`, `LiveRunner`, `ProfileSupport` and `CapabilityReporter`, so it accepts live follow-ups between turns, declares tool activity and structured reports, and honours a profile's tools and model. A model is a `provider/model` selector (for example `deepseek/deepseek-flash`) resolved against an OpenAI-compatible endpoint, and the credential is read from the owner-only `credentials.json` entry stored by `/login <provider>` and otherwise from the environment variable named by the provider prefix (`OPENAI_API_KEY`, `DEEPSEEK_API_KEY`, `OPENROUTER_API_KEY`, `GROQ_API_KEY`, `HYPER_API_KEY`). The one exception is `bedrock`, which resolves through Fantasy's AWS Bedrock provider instead: its model part is a foundation-model or inference-profile ID (the selector splits at the first `/`, so an inference-profile ARN stays intact), it has no OpenAI-compatible model list to fetch, and it authenticates with `AWS_BEARER_TOKEN_BEDROCK` or, when no token is stored or exported, the AWS default credential chain (environment, shared profile or SSO, container, or an attached role), with the region taken from `AWS_REGION`/`AWS_DEFAULT_REGION` or the shared AWS config and defaulting to `us-east-1`. Bedrock therefore resolves without any pilharmonic credential on a machine already authenticated to AWS; the forced-login gate treats it as configured when a token, a static key pair, a web-identity or container credential source, or a shared AWS file with credential material is present, without probing instance metadata, and a profile that only names a region does not count. A specialist model from `profiles.json`, or `-model` when the specialist has none, takes precedence over the task profile model; an attempt with no model at all fails with an explanatory error. Credentials are never written to task state.

The adapter converts provider reasoning deltas into display-only `[thinking]` / `[/thinking]` boundaries in the output transcript; the terminal hides their content by default and lets the operator toggle it. It closes an open thinking block before prose or tool activity because some providers report `thinking_end` after the text stream, so the answer cannot be buried in the block. A live-context acknowledgement reports acceptance as the next user message only, not that the model consumed the input. Because the loop is in-process, a Fantasy attempt shares the orchestrator's process rather than a sandboxed child; the same rules still apply: agent output is untrusted data, tool output is bounded, and `bash` is the only shell path and runs in the execution workspace. Generic command adapters run one process in the task workspace, receive the mandate and pre-start context through stdin and stream stdout/stderr as output. They do not support live follow-ups; the router must report that limitation rather than silently claiming delivery.

The Fantasy adapter exposes seven workspace tools (`read`, `bash`, `edit`, `write`, `grep`, `find`, `ls`) through `fantasy_tools.go`, implemented inside the orchestrator process, and appends a trusted system-prompt instruction to prefer them: `read` for inspection, `edit`/`write` for changes, `grep`/`find`/`ls` for searching and listing, and `bash` only for commands such as builds and tests. Without that instruction the model tends to read, search and edit through shell commands, which made delegated tasks hard to police. A tool name the adapter does not implement is ignored rather than failing the start, and a profile or specialist limit can narrow the palette further. The instruction grants no authority and is part of the trusted system prompt, never interpolated into shell text. The tools also treat empty results as answers rather than failures: `grep` searches the whole workspace recursively by default, resolves its optional path inside the workspace like `read`, prints `file:line:text`, skips Git metadata and binary files, and reports "no matches" as a normal result because grep exits `1` for an empty search. A real error (an invalid pattern, a missing path, a failed command) is still reported as a tool failure.

An adapter may declare the profile parameters it can translate by implementing
`ProfileSupport` (`SupportsProfile(Profile) error`). The service validates the
recorded profile against the executor before it starts a task, so an executor
that cannot set a model or restrict a tool palette fails the start instead of
ignoring the request. A runner that does not implement `ProfileSupport` accepts
only trusted profile instructions and the default parameters. The Fantasy
adapter maps tools and the model selector to its in-process agent and appends
role instructions to the system prompt; `CommandRunner` carries role
instructions on stdin and refuses tool and model selection. The service, not the adapter, decides delegation credentials and
automatic integration from the recorded profile.

An adapter may declare optional capabilities by implementing `CapabilityReporter`; the registry checks the `LiveRunner` interface directly for live follow-ups. Tool activity and structured reports default to disabled unless declared. An adapter that can produce an explicit structured observation may type-assert its `io.Writer` output sink to `ReportRecorder` and call `RecordReport(kind, message)`. Such reports are accepted only while the task's attempt is running, are stored with an `adapter` source and the attempt ID, and are never derived from assistant prose. If the sink does not support reporting, the adapter simply omits the observation. The Fantasy adapter declares live context, tool activity and structured reports, and records a progress report when it accepts the initial prompt, a `waiting` report when it retries after a provider error, and a `failed` report when a step ends with an error or content-filter finish reason or produces no assistant response.

Adapters must treat `Task.Workspace` as the execution directory they are given. For an isolated task that is a prepared Git worktree created from committed `HEAD`, and retries reuse the same worktree; an adapter must not clean, reset or relocate it. The service may commit remaining worktree changes after a successful attempt and merge the prepared commit into the source repository only after operator review approval, so an adapter must not assume the worktree stays at its initial commit. Isolation does not grant new authority or make the agent sandboxed.

A live attempt may receive process-scoped subagent credentials (`ORCHESTRATOR_CONTROL_SOCKET`, `ORCHESTRATOR_TASK_ID`, `ORCHESTRATOR_SUBAGENT_TOKEN`, and `ORCHESTRATOR_SUBAGENT_CLI`). Adapters must pass this environment to the code the agent runs (for the Fantasy adapter, the tools it invokes) and must not persist or log the token as task output. Agent processes receive an allowlisted environment (`PATH`, `HOME`, `LANG`, `LC_*`, `TERM`, `SHELL`, `TMPDIR`, `TMP`, `TEMP`, and any `ORCHESTRATOR_*` variable) rather than the operator's full environment, so cloud credentials and unrelated tokens do not reach model-directed code. The built-in adapters append pilharmonic's delegation guidance to the initial prompt. The environment variable names retain the `ORCHESTRATOR_` prefix for compatibility. An agent requests a child only through the local owner-only control socket, using `pilharmonic subagent`; the resulting child is a separate task with its own mandate. The helper never executes agent output. Adapters that cannot propagate the environment simply do not expose delegation.

## Subagent helper

The `pilharmonic subagent` helper is the only supported way for a running agent to
delegate a child task. It reads the child mandate from stdin (bounded to 256 KiB)
and speaks only to the local control socket named in the environment; it never
executes agent output or a shell.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-title` | `""` | short display title for the child task |
| `-agent` | `""` | executor for the child; automatic selection, then the parent executor |
| `-profile` | `""` | profile for the child; defaults to the parent profile |
| `-socket` | `ORCHESTRATOR_CONTROL_SOCKET` | path to the local control socket |
| `-no-wait` | `false` | record and start the child without waiting for it |
| `-start-timeout` | `5s` | how long to wait for a free execution slot |
| `-timeout` | `30m` | maximum time to wait for completion |

It exits `0` on success, `1` on a recorded failure or a wait timeout, and `2` on
a usage or protocol error. With `-no-wait` it prints the recorded child ID and
returns without waiting; otherwise it blocks until the child reaches a terminal
state or the timeout elapses, printing the child's final status and output.

Configured commands are trusted executable configuration. Invoke commands directly with explicit arguments; never interpolate prompts into shell command text. Treat output as untrusted display data. A process exiting successfully is execution success, not verification of its changes. Process startup, cancellation, output-write failure and nonzero exit must produce observable attempt outcomes.

Agent selection applies only to a queued task without an outstanding start request. It must be persisted and recorded in task history. Each attempt retains the agent that actually executed it. Configure the registry before resuming persisted start requests. Missing agents must not be silently replaced by the general executor.

Future adapters should declare their capabilities through the registry and translate their native protocol in adapter-specific code. Tests should exercise protocol and lifecycle failures with fake processes, without requiring credentials (the built-in Fantasy adapter accepts an injected model factory, so its tests never need a network connection or an API key).
