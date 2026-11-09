# What is going on here

This is a snapshot of the project as it exists today, written to make the moving
parts easy to see at once. It describes implemented behavior only. The design
intent and the forward-looking invariants are in `design.md` and
`routing-spec.md`; where those documents describe future work, this one says so.

## In one paragraph

Orchestrator is a local, single-machine Go application with a Bubble Tea
terminal UI. A person submits a task in the UI, explicitly starts it, and a
scheduler launches at most a configured number of Pi (or configured command)
subprocesses at a time. Requests, decisions, threads, tasks, execution attempts,
events, reports, tool activity and captured output are stored in one JSON state
file that is rewritten atomically. Related tasks, thread follow-ups, manual
status reports, retries, deletion and opt-in Git-worktree isolation are
supported locally. Output is rendered as Markdown, and the UI exposes a
tool-activity view, a shortcut-help popup and an agent chooser. There is no
external input source, no authorization policy engine and no GitHub integration.
Tasks can opt into a dedicated Git worktree; otherwise they share their source
workspace.

## Repository status (2026-11-09)

- The repository starts from a single baseline commit on `main`.
- `go test ./...` and `go vet ./...` pass.
- Roughly 3.5k lines of implementation and 2.3k lines of tests. Dependencies
  are Bubble Tea, Lip Gloss, Glamour and `charmbracelet/x/ansi`; everything else
  is the Go standard library.
- Packages: the module root (entry point), `internal/orchestrator` (state,
  scheduling, routing, workspace preparation and Pi adapters) and `internal/tui`
  (view and controls).

## Startup

`go run .` parses four flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-state` | `<user config dir>/orchestrator/state.json` | durable state file |
| `-workspace` | current directory | working directory for newly created tasks |
| `-max-concurrent` | `2` | maximum simultaneously running agents |
| `-agents` | none | optional trusted command-executor configuration |

The state file is opened read-only first, then repaired in memory and saved if
anything changed:

- follow-ups still marked `pending` become `unknown` ("orchestrator stopped
  before acknowledgement"), because delivery cannot be proven;
- legacy tasks without `ThreadID` are given one and a thread record;
- legacy tasks without attempts get one synthesized attempt from their history;
- tasks that were `running` become `interrupted`, and their latest attempt gets
  a finish time and an error. No subprocess is assumed to have survived.

The state file is opened, then configured command executors are loaded and the
concurrency limit is configured, which also resumes persisted start requests.
Then the TUI starts. `Shutdown` cancels running work, closes pending start
requests and waits for workers to finish writing state. Quitting therefore ends
active tasks as `canceled`, not `interrupted` — interrupted only comes from
reopening state after a crash or kill.

## Lifecycle of a request

1. **Submission.** Pressing `n` creates a local task through `CreateTask`
   (`service.go`). It validates title/prompt/workspace, generates task, trigger
   and thread IDs, and records the explicit request as an accepted trigger,
   decision and event. All of it is persisted before the call returns; a save
   failure rolls the slices back. A task is `queued` but deliberately not
   started.
2. **Explicit start.** `r` calls `Start`. If a slot is free and an executor is
   registered, the task starts immediately. Otherwise the start request is
   marked `ScheduleRequested` with a timestamp and a persisted `scheduled`
   event, and the task waits in FIFO order by `ScheduledAt`.
3. **Attempt creation.** `startLocked` (`scheduler.go`) persists a new attempt
   with its own ID, status `running`, start time and a `started` event *before*
   launching a goroutine. A save failure cancels and restores the task.
4. **Workspace preparation (isolated tasks only).** The worker calls
   `prepareExecution` (`workspace.go`) before the runner starts. It creates a
   reservation at `workspaces/<task-id>/` beside the state file, writes
   `owner.json` (task, source repository, common Git directory and initial
   commit), and creates a detached Git worktree at `tree/` from committed
   `HEAD`. It records `ExecutionWorkspace` and `IsolationCommit`; on reuse it
   validates the reservation's directory type, ownership record and Git
   worktree before the attempt runs. The runner's working directory is the
   matching subdirectory of the worktree. Preparation failures fail the attempt
   and the source workspace is not modified.
5. **Execution.** The runner is looked up by agent name (`pi` by default).
   `PiRunner` implements `LiveRunner`, so `RunLive` (`pi_rpc.go`) starts
   `pi --mode rpc --no-session` in the execution workspace. It sends one initial
   prompt containing the mandate plus any context under an explicit
   "not authority to expand the mandate" heading, then streams assistant text
   deltas and tool start/end markers to the output writer. The non-live `Run`
   path uses `pi --mode json --` and is kept for other runners/tests.
6. **Output.** Each write appends to the task output, trims it to the newest
   256 KiB, mirrors it onto the latest attempt, and saves the whole state file.
   The TUI sanitizes output before display (strips ANSI, expands tabs, drops
   control characters), so agent text is rendered as data.
7. **Thread work.** `a` creates a related task in the selected task's thread
   with its own mandate and decision (`thread.go`). `f` sends one follow-up
   trigger to all subscribed queued and running tasks in that thread.
   `s` toggles a per-task subscription; pausing affects only future follow-ups.
   Delivery is recorded per recipient:
   - queued tasks get `at_start`;
   - running tasks get `pending`, then `accepted` or `rejected` after Pi
     acknowledges the RPC command, or `unknown` if the execution ends or the
     orchestrator stops first;
   - running tasks with no live channel or a full queue get `rejected` with a
     reason.
   Acknowledged follow-ups are not replayed and do not alter the mandate.
8. **Reports.** `p` records a manual observation: `progress`, `waiting`,
   `blocked`, `ready` or `failed`, with a message (`report.go`). Reports are
   separate from process status. `Task.Stage()` derives an attention stage from
   the latest report, ignoring reports made before the current attempt and
   clearing `blocked`/`waiting`/`failed` once newer context arrives.
9. **Finish.** When the runner returns, the worker records `canceled` if its
   context was canceled, `failed` on a non-nil error, otherwise `completed`.
   The latest attempt is finalized, a terminal event is appended, state is
   saved, and the scheduler starts the next waiting task if capacity opened.
10. **Retry, cancel, delete.** `R` requeues a failed, interrupted or canceled
    task without changing its mandate or attempts and requires a new `r`; an
    isolated task reuses its prepared worktree. `c` cancels a queued task
    immediately or signals a running one. `d` then `y` deletes an inactive
    task; if no other task shares its thread, the thread history is deleted too.
    The task's worktree is retained. Workspace files are never touched.

## Data model

`state.json` has two top-level slices: `tasks` and `threads`.

- **Thread** holds the ordered triggers and routing decisions for a
  conversation. A task's original trigger and decision are mirrored there.
- **Task** holds the immutable `Prompt` (the mandate), the original
  `Trigger`/`Decision`, `Context` (follow-ups with delivery state), `Attempts`,
  `Reports`, `Activities`, `Events`, scheduling flags, workspace, agent, status,
  timestamps, the isolation choice, the prepared execution workspace and its
  initial commit, latest output and error.
- **Attempt** is one execution: agent, status, start/finish, output and error.
  Task status and attempt status have separate lifecycles that are updated
  together today.
- **Event** is the append-only narrative shown in the history tab.
- **Workspace reservation** lives beside the state file at
  `workspaces/<task-id>/`: `owner.json` records the task, source repository,
  common Git directory and initial commit, and `tree/` is the detached Git
  worktree. It is not part of `state.json` and is retained after the task is
  deleted.

Statuses: `queued`, `running`, `completed`, `failed`, `canceled`,
`interrupted`. A `completed` task means the process exited successfully (for
Pi, also that it reported an assistant response); it does not mean the work is
correct or reviewed.

## Invariants the code tries to keep

- The original request, the accept decision, the mandate and each attempt are
  distinct records; follow-ups are context, never a new mandate.
- State transitions are persisted before they are acknowledged, with
  compensating rollback on synchronous save errors.
- At most `-max-concurrent` runners execute at once, including when starts and
  completions race; capacity changes do not interrupt running work.
- A new execution starts only after its attempt is durable in the state file.
- Prompts are passed as an argument or RPC input, never through a shell.
- Agent output is untrusted: it is stored and displayed, never executed, and is
  sanitized before terminal layout.
- Delivery acknowledgement is reported honestly: Pi accepting a command is not
  proof that the model consumed it, and crashes leave `unknown` rather than
  success.
- An isolated attempt runs only in the worktree reserved for that task and
  repository; ownership records, directory types and the recorded commit are
  validated before reuse. Preparing a workspace happens before the runner
  starts, and the source workspace is not modified.
- Slices returned by the service are copies; the UI is not the source of truth.

## Known limits and current gaps

- Only the trusted local UI is a source. Every request is accepted
  deterministically; there is no policy engine, external trigger authentication
  or model-assisted routing.
- The state file is intended for one running instance. Concurrent instances
  sharing a path would clobber each other. Whole-file rewrites happen on every
  output chunk, so large or chatty runs rewrite frequently.
- Isolation is opt-in and starts from committed `HEAD`: uncommitted source files
  are absent, a repository with no commit cannot be isolated, and the `git`
  executable is required. Worktrees are never cleaned up automatically, so they
  accumulate beside the state file; there is no in-app diff, review, merge or
  discard action. Tasks without isolation still share their workspace.
- There is no Pi session continuity across restarts or retries; every attempt is
  a fresh process. Follow-up RPC messages accepted by Pi are queued inside Pi
  and are not replayed after a restart.
- Output is capped at the newest 256 KiB per task/attempt; older text is
  discarded. State may contain sensitive prompts and output and is stored
  unencrypted (directory `0700`, file `0600`).
- Reports are manual. Free-form Pi output is never parsed into reports, and
  there are no adapter-generated reports or capability selection yet.
- Running live context is bounded (64 pending messages per task) and sends are
  rejected rather than blocked when full.
- The TUI text input supports append and backspace only.
- Pi is built in; configured command executors can be added with `-agents`. They
  run behind `Runner`/`LiveRunner`. Only Pi supports live follow-ups and
  structured tool activity.

## Tests

Tests use fake runners and temporary state files; none needs model credentials.

- `service_test.go`: accept/run/reload, persistence rollback, cancellation,
  interruption on reopen, context idempotency and mandate isolation.
- `scheduler_test.go`: concurrency bound under race, FIFO scheduling, persisted
  waiting requests and cancellation while waiting.
- `thread_test.go`: related tasks, thread follow-up fan-out, subscription
  pause/resume, idempotent retries, delivery outcomes.
- `report_test.go`: report validation, current-attempt scoping and stages.
- `activity_test.go`: tool-activity persistence, snapshot copying and payload
  truncation.
- `agents_test.go`: agent configuration validation, selection, capability
  reporting and command-runner stdin composition.
- `delete_test.go`: deletion of tasks and orphaned thread history, refusal while
  running.
- `workspace_test.go`: isolated worktree preparation from committed `HEAD`,
  subdirectory workspaces, retry reuse, ownership and symlink rejection,
  non-repository failure, retention after deletion and toggle rules.
- `pi_test.go`, `pi_rpc_test.go`: event-stream translation, prompt composition,
  acknowledgement, failure and shutdown paths using fake commands.
- `internal/tui/*_test.go`: scrolling, layout, Markdown rendering, help
  navigation, agent picker, activity filters, isolation controls, deletion flow
  and thread controls.
