# Executor contract

An executor runs one attempt of a task. The task's mandate, thread context and execution history belong to the orchestrator. Adapters must not mutate them or infer additional authority from context.

The built-in Pi adapter uses RPC for progress and follow-ups. A live-context acknowledgement reports command acceptance only, not that the model consumed the input. Generic command adapters run one process in the task workspace, receive the mandate and pre-start context through stdin and stream stdout/stderr as output. They do not support live follow-ups; the router must report that limitation rather than silently claiming delivery.

Adapters must treat `Task.Workspace` as the execution directory they are given. For an isolated task that is a prepared Git worktree created from committed `HEAD`, and retries reuse the same worktree; an adapter must not clean, reset or relocate it. Isolation does not grant new authority or make the agent sandboxed.

Configured commands are trusted executable configuration. Invoke commands directly with explicit arguments; never interpolate prompts into shell command text. Treat output as untrusted display data. A process exiting successfully is execution success, not verification of its changes. Process startup, cancellation, output-write failure and nonzero exit must produce observable attempt outcomes.

Agent selection applies only to a queued task without an outstanding start request. It must be persisted and recorded in task history. Each attempt retains the agent that actually executed it. Configure the registry before resuming persisted start requests. Missing agents must not be silently replaced by Pi.

Future adapters should declare their capabilities through the registry and translate their native protocol in adapter-specific code. Tests should exercise protocol and lifecycle failures with fake processes, without requiring credentials.
