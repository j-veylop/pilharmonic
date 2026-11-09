# Executor contract

An executor runs one attempt of a task. The task's mandate, thread context and execution history belong to the orchestrator. Adapters must not mutate them or infer additional authority from context.

The built-in Pi adapter uses RPC for progress and follow-ups. It converts Pi's thinking deltas into display-only `[thinking]` / `[/thinking]` boundaries in the output transcript; the terminal hides their content by default and lets the operator toggle it. It closes an open thinking block before prose or tool activity because some providers report `thinking_end` after the text stream, so the answer cannot be buried in the block. A live-context acknowledgement reports command acceptance only, not that the model consumed the input. Generic command adapters run one process in the task workspace, receive the mandate and pre-start context through stdin and stream stdout/stderr as output. They do not support live follow-ups; the router must report that limitation rather than silently claiming delivery.

The built-in Pi adapter requests Pi's dedicated file tools (`read`, `bash`, `edit`, `write`, `grep`, `find`, `ls`) and appends a trusted system-prompt instruction to prefer them: `read` for inspection, `edit`/`write` for changes, `grep`/`find`/`ls` for searching and listing, and `bash` only for commands such as builds and tests. Pi's default palette omits `grep`, `find` and `ls`, and its generated prompt then tells the model to use `bash` for file operations, which made delegated tasks read, search and edit through shell commands. Unknown tool names are ignored by Pi, so installations that lack one of these tools still start with the tools they have. The instruction grants no authority and is passed as process arguments, never through a shell.

An adapter may declare the profile parameters it can translate by implementing
`ProfileSupport` (`SupportsProfile(Profile) error`). The service validates the
recorded profile against the executor before it starts a task, so an executor
that cannot set a model or restrict a tool palette fails the start instead of
ignoring the request. A runner that does not implement `ProfileSupport` accepts
only trusted profile instructions and the default parameters. Pi maps tools and
model to process arguments and appends role instructions to its system prompt;
`CommandRunner` carries role instructions on stdin and refuses tool and model
selection. The orchestrator, not the adapter, decides delegation credentials and
automatic integration from the recorded profile.

An adapter may declare optional capabilities by implementing `CapabilityReporter`; the registry checks the `LiveRunner` interface directly for live follow-ups. Tool activity and structured reports default to disabled unless declared. An adapter that can produce an explicit structured observation may type-assert its `io.Writer` output sink to `ReportRecorder` and call `RecordReport(kind, message)`. Such reports are accepted only while the task's attempt is running, are stored with an `adapter` source and the attempt ID, and are never derived from assistant prose. If the sink does not support reporting, the adapter simply omits the observation. Pi declares live context, tool activity and structured reports, and records a progress report when it accepts the initial prompt.

Adapters must treat `Task.Workspace` as the execution directory they are given. For an isolated task that is a prepared Git worktree created from committed `HEAD`, and retries reuse the same worktree; an adapter must not clean, reset or relocate it. The orchestrator may commit remaining worktree changes and merge them into the source repository after a successful attempt, so an adapter must not assume the worktree stays at its initial commit. Isolation does not grant new authority or make the agent sandboxed.

A live attempt may receive process-scoped subagent credentials (`ORCHESTRATOR_CONTROL_SOCKET`, `ORCHESTRATOR_TASK_ID`, `ORCHESTRATOR_SUBAGENT_TOKEN`, and `ORCHESTRATOR_SUBAGENT_CLI`). Adapters must pass this environment to the agent process and must not persist or log the token as task output. The built-in adapters append the orchestrator's delegation guidance to the initial prompt. An agent requests a child only through the local owner-only control socket, using `orchestrator subagent`; the resulting child is a separate task with its own mandate. The helper never executes agent output. Adapters that cannot propagate the environment simply do not expose delegation.

Configured commands are trusted executable configuration. Invoke commands directly with explicit arguments; never interpolate prompts into shell command text. Treat output as untrusted display data. A process exiting successfully is execution success, not verification of its changes. Process startup, cancellation, output-write failure and nonzero exit must produce observable attempt outcomes.

Agent selection applies only to a queued task without an outstanding start request. It must be persisted and recorded in task history. Each attempt retains the agent that actually executed it. Configure the registry before resuming persisted start requests. Missing agents must not be silently replaced by Pi.

Future adapters should declare their capabilities through the registry and translate their native protocol in adapter-specific code. Tests should exercise protocol and lifecycle failures with fake processes, without requiring credentials.
