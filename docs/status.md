# What is going on here

This is a snapshot of the project as it exists today, written to make the moving
parts easy to see at once. It describes implemented behavior only. The design
intent and the forward-looking invariants are in `design.md` and
`routing-spec.md`; where those documents describe future work, this one says so.

The CLI opens on a deterministic router that accepts a local request, assigns
a specialist or the fallback Pi executor and requests execution. The task board also supports manual drafts that
wait for an explicit start. The service persists threads, tasks and attempts
in one JSON file. The terminal UI reads snapshots from the service.

## Startup

`go run .` supports these flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-state` | `<user config dir>/orchestrator/state.json` | durable state file |
| `-workspace` | current directory | working directory for newly created tasks |
| `-max-concurrent` | `2` | maximum simultaneously running agents |
| `-isolation` | `true` | default change tasks to a Git worktree when the workspace supports it |
| `-agents` | none | optional trusted command-executor configuration |
| `-profiles` | `./profiles.json` if present | profile and specialist configuration from the launch directory, or an explicit file |
| `-profile` | `implementer` | default profile for routed work and tasks without an explicit choice |
| `-subagents` | `true` | allow running agents to delegate child tasks |
| `-subagent-socket` | `subagent.sock` beside the state file | owner-only local control socket |
| `-max-subagent-depth` | `2` | maximum delegation depth below a root task |
| `-max-subagents` | `4` | maximum child tasks one task may spawn |

The state file is opened read-only first, then repaired in memory and saved if
anything changed:

- follow-ups still marked `pending` become `unknown` ("orchestrator stopped
  before acknowledgement"), because delivery cannot be proven;
- legacy tasks without `ThreadID` are given one and a thread record;
- legacy tasks without attempts get one synthesized attempt from their history;
- tasks that were `running` become `interrupted`, and their latest attempt gets
  a finish time and an error. No subprocess is assumed to have survived.

The state file is opened, then configured command executors are loaded, the
subagent limits and control socket are configured, and the concurrency limit is
set, which also resumes persisted start requests. Then the TUI starts on the
router screen. `Shutdown` cancels running work, closes pending start requests,
closes the subagent control socket and waits for workers to finish writing
state. Quitting therefore ends
active tasks as `canceled`, not `interrupted` — interrupted only comes from
reopening state after a crash or kill.

## Lifecycle of a request

The primary path starts at the router. Pressing Enter on the router screen calls
`SubmitRequest` (`router.go`), which trims the request, derives a display title
from its first line, and persists it through the same path as a manual draft.
The decision records the local policy ID/version (`local` / `local-1`), the
trusted local source and actor, and a versioned digest of the policy input. A
`routed` event records the assignment (selected executor, an isolated worktree or shared
workspace as applicable, execution requested). The router then calls `Start`
immediately. The same screen lists tasks whose stage
needs a person and recent routes. One request becomes one task: the router does
not decompose requests or evaluate them with a model.

1. **Manual submission.** Pressing `n` creates a draft through `CreateTask`
   (`service.go`) instead of routing immediately. It validates title/prompt/
   workspace, classifies the request as change or read-only inspection, and
   selects automatic isolation for change work when the workspace is a
   repository with a committed `HEAD`. Read-only inspection work stays in the
   source workspace. It records whether the choice was automatic, then
   generates task, trigger and thread IDs, runs the local authorization policy,
   and records the explicit request as an accepted trigger, decision and event.
   All of it is persisted before the call returns; a save failure rolls the
   slices back. A manual draft is `queued` but deliberately not started. An
   executor change before start reclassifies an automatic workspace choice;
   an explicit `w` choice is preserved.
2. **Explicit start.** `r` (or the router after routing) calls `Start`. An unavailable executor causes a start error. If a
   slot is free the task starts immediately. Otherwise the start request is
   marked `ScheduleRequested` with a timestamp and a persisted `scheduled`
   event, and the task waits in FIFO order by `ScheduledAt`.
3. **Attempt creation.** `startLocked` (`scheduler.go`) persists a new attempt
   with its own ID, status `running`, start time and a `started` event *before*
   launching a goroutine. A save failure cancels and restores the task.
4. **Workspace preparation (isolated tasks).** The worker calls
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
   deltas, thinking-block markers and tool start/end markers to the output
   writer. The non-live `Run` path uses `pi --mode json --` and is kept for
   other runners/tests.
6. **Output.** Each write appends to the task output, trims it to the newest
   256 KiB, mirrors it onto the latest attempt, and saves the whole state file.
   In the output view, assistant text is rendered as Markdown while inline tool
   markers become compact status rows with a coloured rail and glyph; a
   directly paired start/finish collapses into one finished row. Thinking
   markers collapse to one `thinking hidden` row by default, and `T` toggles
   their content in a separate `THINKING` block. The adapter closes an open
   thinking block before prose or tool activity, so a late provider
   `thinking_end` cannot hide the answer. The TUI sanitizes output before
   display (strips ANSI, expands tabs, drops control characters), so agent text
   is rendered as data.
7. **Thread work.** `a` creates a related task in the selected task's thread
   with its own mandate and decision (`thread.go`). `f` sends one follow-up
   trigger to all subscribed queued and running tasks in that thread. `F`
   follows up only the selected task: a queued or running task receives
   context, while a completed, failed, canceled or interrupted task records
   at-start context, clears its previous terminal error and merge result, and
   is requeued without changing its mandate or attempts. The requeued task
   needs an explicit `r`, like a retry. `s` toggles a per-task subscription;
   pausing affects only future thread follow-ups.
   Delivery is recorded per recipient and per attempt:
   - queued tasks get `at_start` when the attempt starts;
   - running tasks get `pending`, then `accepted` or `rejected` after Pi
     acknowledges the RPC command, or `unknown` if the execution ends or the
     orchestrator stops first;
   - running tasks with no live channel or a full queue get `rejected` with a
     reason.
   Each hand-off keeps a durable `Delivery` record with its trigger, execution
   attempt, state, reason and timestamps. A retry adds records for the new
   attempt instead of rewriting earlier outcomes, and only the current
   attempt's records determine whether context needs attention. Acknowledged
   follow-ups are not replayed and do not alter the mandate.
8. **Subagent delegation.** `b` on a running task, or a running agent calling
   `"$ORCHESTRATOR_SUBAGENT_CLI" subagent`, goes through
   `SpawnSubagent` (`subagent.go`). The service validates an attempt-scoped
   token when the request came over the control socket, requires a running
   parent, and enforces depth and per-parent child limits. It persists a new
   task with its own thread, trigger, `subagent-1` decision, mandate, parent ID
   and depth, records an event on both tasks, then starts the child. The child
   uses the parent's executor as fallback and its execution workspace
   (including a prepared worktree), but not its context. Control credentials and prompt guidance are
   process-local and are cleared when the attempt ends. If capacity is
   unavailable, `subagent` reports the child as queued after a short start
   timeout instead of holding the parent's slot indefinitely.
9. **Reports.** `p` records an operator observation: `progress`, `waiting`,
   `blocked`, `ready` or `failed`, with a message (`report.go`). A runner may
   also record an explicit adapter observation through its output sink; this is
   accepted only while the attempt is running and is stored with an `adapter`
   source and attempt ID. The built-in Pi adapter records a `progress` report
   when Pi accepts the initial prompt and a `failed` report when Pi reports an
   error, without parsing assistant prose. Reports are
   separate from process status. `Task.Stage()` derives an attention stage from
   the latest current-attempt report, ignoring reports made before that attempt
   and clearing `blocked`/`waiting`/`failed` once newer context arrives.
10. **Finish and automatic merge.** When the runner returns, the worker records
    `canceled` if its context was canceled, `failed` on a non-nil error,
    otherwise `completed`. Before finalizing a successful isolated attempt, the
    worker validates the workspace reservation again, commits any worktree
    changes with a task-identifying message, and merges that commit into the
    source repository's current checkout with a merge commit (`merge.go`).
    Merges are serialized. A no-change completion is recorded as `up_to_date`;
    a clean merge records the worktree and merge commits; a conflict or Git
    failure aborts the source merge and records `merge_failed` separately from
    the successful attempt. A dirty source checkout whose changes overlap the
    worktree blocks the merge instead of being overwritten or stashed: Git
    aborts, the error names the files and asks the operator to commit or stash
    them, and a retry after the source is clean merges the retained worktree. The worktree is retained in every case until the
    task is deleted or its worktree is explicitly discarded. A merged
    commit, or a worktree commit whose merge failed, is then recorded as
    `pending` review with its commit; an up-to-date completion has no new
    commit and is not opened for review. The latest attempt is finalized,
    control credentials are cleared, terminal, merge and review events are
    appended, state is saved, and the scheduler starts the next waiting task if
    capacity opened.
11. **Retry, cancel, delete.** `R` requeues a failed, interrupted or canceled
    task without changing its mandate or attempts and requires a new `r`; a
    completed isolated task whose automatic merge failed is also retryable and
    clears the previous merge result. An isolated task reuses its prepared
    worktree. `c` cancels a queued task
    immediately or signals a running one. `d` then `y` deletes an inactive
    task; if no other task shares its thread, the thread history is deleted too.
    A task with child tasks must have them deleted first, so parent links never
    dangle. `D` then `y` deletes every completed task whose children would also
    be removed, keeps completed parents that still have unfinished children,
    and removes threads no remaining task uses. Both deletion paths remove the
    tasks' retained worktrees and reservations after the state change is
    durable. The source workspace is never touched. A canceled task keeps its
    worktree until deletion so a retry can reuse it. `x` then `y` discards the
    selected task's retained worktree without deleting the task, and `X` then
    `y` discards every task worktree recorded as merged or up to date. Discard
    refuses a running task, uncommitted changes and an unmerged worktree
    commit, removes the reservation, clears the task's recorded execution path
    and appends a `worktree_discarded` event; task history is kept.

## Data model

`state.json` has two top-level slices: `tasks` and `threads`.

- **Thread** holds the ordered triggers and routing decisions for a
  conversation. A task's original trigger and decision are mirrored there. An
  authorization decision records its outcome, reason, policy ID/version,
  source, actor and a versioned SHA-256 digest of the normalized input; a
  context decision records its outcome and reason.
- **Task** holds the immutable `Prompt` (the mandate), the original
  `Trigger`/`Decision`, optional `ParentID` and `Depth` for delegated work,
  `Context` (immutable follow-up inputs with a current delivery summary),
  `Deliveries` (per-attempt hand-off records), `Attempts`, `Reports` (operator
  or adapter observations with source and attempt),
  `Activities`, `Events`, scheduling flags, workspace, agent, status,
  timestamps, the isolation choice and whether it was automatic, the prepared
  execution workspace and its
  initial commit, merge status/error, worktree and merge commits, merge time,
  review status/commit/actor/time/note for a prepared worktree, latest output
  and error. Control credentials are not part of the stored task.
- **Attempt** is one execution: agent, status, start/finish, output and error.
  Task status and attempt status have separate lifecycles that are updated
  together today.
- **Event** is the append-only narrative shown in the history tab.
- **Workspace reservation** lives beside the state file at
  `workspaces/<task-id>/`: `owner.json` records the task, source repository,
  common Git directory and initial commit, and `tree/` is the detached Git
  worktree. It is not part of `state.json` and is retained while the task
  exists, including after an automatic merge failure, so retries and review
  can use it. Deleting the task removes the reservation; if cleanup fails, the
  deletion remains durable and the reservation is left in place with an error.
  An explicit discard removes the reservation while keeping the task, using the
  same ownership validation but refusing dirty or unmerged worktrees.

Statuses: `queued`, `running`, `completed`, `failed`, `canceled`,
`interrupted`. A `completed` task means the process exited successfully (for
Pi, also that it reported an assistant response); it does not mean the work is
correct or reviewed. For an isolated task, `merge_status` separately records
whether its changes were merged, were already up to date, or failed to merge;
a merge failure leaves the execution attempt completed but makes the task need
attention.

Review status is independent of both process status and merge outcome:
`pending` means an isolated task's prepared worktree commit awaits an operator
decision, and `approved`/`rejected` record that decision with its commit, actor,
time and optional note. Review opens when a successful isolated attempt
produces a worktree commit that was merged or whose merge failed; an up-to-date
completion opens no review. The local UI approves with `v` and rejects with `V`
plus a required reason, and the service methods `ApproveReview` and
`RejectReview` perform the same transition. The commit displayed when the operator opens the review must match the pending
commit. Each decision is persisted with a `review_approved` or `review_rejected` event before
it is acknowledged, and a new attempt clears it. Review status does not gate
automatic merging: merge outcome continues to be recorded separately, and a
rejection neither undoes a merge nor removes the retained worktree.

## Invariants the code tries to keep

- The original request, the accept decision, the mandate and each attempt are
  distinct records; follow-ups are context, never a new mandate.
- Task creation goes through the source's authorization policy. Policies return
  an explicit outcome and policy ID/version, and the decision stores a digest of
  the exact normalized input so it can be recomputed later. Model or agent
  output never supplies an authorization decision.
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
  success. Delivery records are scoped to the attempt that received them; a
  retry starts new records and does not inherit an older rejection or unknown
  outcome.
- An isolated attempt runs only in the worktree reserved for that task and
  repository; ownership records, directory types, repository identity and the
  recorded commit are validated before reuse and again before merging.
  Preparing a workspace happens before the runner starts, and the source
  workspace is not modified while the agent runs. Only a successful completion
  attempts a merge; canceled and failed attempts do not. Automatic merges are
  serialized, create a merge commit, and abort on conflict instead of leaving
  the source checkout in a partial merge. Merge failure is recorded separately
  from execution success and makes the task need attention.
- Automatic isolation is selected only for change work. Read-only inspection
  requests and inspection-role tasks with no imperative edit action share the
  source workspace. This classification is a lexical heuristic, not a sandbox
  or proof that an executor will not write. Requests matching neither an
  inspection opening nor an inspection role default to isolation, and an
  explicit per-task choice overrides the automatic one.
- Review is a separate operator record, not a merge gate. A prepared worktree
  commit that was merged or whose merge failed is opened for review once, a
  decision is matched to that commit and persisted before it is acknowledged,
  and a new attempt clears it. A decision never rewrites merge history or the
  retained worktree.
- A subagent is a new task with its own mandate, thread, decision and attempt;
  it is never a mutation of the parent's mandate. Spawn records are persisted
  before the child starts, and a failed start remains visible as a queued task
  with an event. Agent-initiated requests require a running parent and valid attempt
  credentials, and depth and child limits bound fan-out.
- Subagent control credentials exist only in process memory and the child
  process's environment. The control socket is owner-only and never writes
  agent output as commands.
- Slices returned by the service are copies; the UI is not the source of truth.

## Known limits and current gaps

- Only the trusted local UI and bounded subagent delegation are sources. The
  built-in local and subagent policies are compiled in and both accept their
  trusted source; there is no policy file, chooser or per-request policy
  selection, no persistence of rejected requests, no dedicated audit export,
  no external trigger authentication and no model-assisted routing.
- The state file is intended for one running instance. Concurrent instances
  sharing a path would clobber each other. Whole-file rewrites happen on every
  output chunk, so large or chatty runs rewrite frequently.
- Isolation is the default for new change work when the source workspace is a
  Git repository with a committed `HEAD`, and can be disabled with
  `-isolation=false` or per task before start. Read-only inspection requests
  and inspection-role tasks with no edit action share the source workspace.
  Classification is a lexical role and request-opening heuristic: a custom
  executor using an inspection role name, or a misleading mandate, can still
  write to the shared workspace; an explicit `w` choice or `-isolation=false`
  controls the default.
  Isolation starts from that commit: uncommitted source files are absent, a
  repository with no commit cannot be isolated, and the `git` executable is
  required. Successful isolated tasks merge automatically, but that is
  integration, not review: review is a recorded approve/reject decision on a
  prepared commit and does not gate merging. There is no in-app diff or conflict
  resolution, and a conflicting merge must be resolved outside the application.
  Worktrees for retained tasks accumulate beside the state file; deleting a task
  cleans up its worktree, and `x`/`X` discard merged worktrees while keeping the
  task, refusing dirty or unmerged worktrees. A dirty source checkout can make
  an otherwise clean worktree merge fail without changing the worktree; the
  failure names the overlapping files and the worktree is kept for retry after
  the source is cleaned. Tasks in workspaces that cannot be isolated, or that
  are explicitly opted out, share their workspace.
- There is no Pi session continuity across restarts or retries; every attempt is
  a fresh process. Follow-up RPC messages accepted by Pi are queued inside Pi
  and are not replayed after a restart.
- Output is capped at the newest 256 KiB per task/attempt; older text is
  discarded. State may contain sensitive prompts and output and is stored
  unencrypted (directory `0700`, file `0600`).
- Operator reports and Pi adapter reports exist; configured command adapters
  do not emit structured reports and there is no report-driven routing policy.
  Free-form agent output is never parsed into reports. Capability declarations
  are informational; only Pi currently declares tool activity and reports.
- Running live context is bounded (64 pending messages per task) and sends are
  rejected rather than blocked when full. Delivery records are retained with
  task history and are not compacted independently.
- The TUI text input supports append and backspace only.
- Pi is built in; configured command executors can be added with `-agents`. They
  run behind `Runner`/`LiveRunner`. Only Pi supports live follow-ups and
  structured tool activity.
- The router is deterministic and creates one task per request. It does not
  interpret or decompose requests with a model. It
  selects a registered specialist or falls back to Pi, applies the default workspace choice, and starts the task. Manual
  drafts remain for other executors, per-task isolation changes and other
  pre-start changes.
- Subagent delegation is bounded but not sandboxed: a child has the same host
  permissions and shares the parent's workspace, so concurrent file edits are
  not coordinated. A child is not canceled when its parent ends, and the parent
  helper receives the child's final output only when the child completes.
  Subprocesses that a Pi extension spawns on its own are not orchestrator tasks
  and remain outside these tracking, limit and cancellation rules.
- The control socket uses a Unix socket, so agent-driven delegation is not
  available on platforms without Unix-domain sockets.

## Persistence benchmark

Run `go test ./internal/orchestrator -run '^$' -bench BenchmarkStreamingPersistence -benchmem`.
The benchmark starts a fake runner and writes 1 KiB chunks through its real
output sink while a goroutine requests task snapshots at 10 Hz. The two
fixtures contain zero or 32 completed tasks, each with 64 KiB of output
mirrored in its attempt. The active transcript grows until it reaches its cap.

On a Linux amd64 i7-10610U, one local run measured 1.47 ms and 1.48 MB
allocated per write with no history, versus 14.17 ms and 17.90 MB with 32
completed tasks. These figures include JSON encoding and replacing the state
file on local temp storage. Disk type, history size and stream length affect
the result. Output and lifecycle updates still save synchronously. A future
batching proposal should specify the maximum output loss on a crash and keep
lifecycle decisions durable before acknowledgement.
