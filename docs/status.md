# What is going on here

This is a snapshot of the project as it exists today, written to make the moving
parts easy to see at once. It describes implemented behavior only. The design
intent and the forward-looking invariants are in `design.md` and
`routing-spec.md`; where those documents describe future work, this one says so.

The CLI opens on a deterministic router that accepts a local request, assigns
the executor named by a leading `@NAME` mention or the general fallback, and
requests execution. Top-level routing does not infer an executor from the
request text; explicit task choices and delegation can still select the
implementer. The task board also supports manual drafts that wait for an
explicit start. The service persists threads, tasks and attempts
in one JSON file. The terminal UI reads snapshots from the service.

## Startup

`go run .` supports these flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-state` | `<user config dir>/orchestrator/state.json` | durable state file (legacy location retained) |
| `-workspace` | current directory | working directory for newly created tasks |
| `-max-concurrent` | `4` | maximum simultaneously running agents |
| `-idle-timeout` | `0` | cancel an agent that produces no output for this long; `0` disables |
| `-isolation` | `true` | allow automatic Git worktrees for mandates that request changes |
| `-agents` | none | optional trusted command-executor configuration |
| `-specialist-tools` | none | optional JSON file that sets per-specialist tool limits |
| `-model` | none | fallback model selector (`provider/model`) for profiles that do not name one |
| `-profiles` | `profiles.json` beside the state file | profile and specialist configuration, or an explicit file |
| `-profile` | `(unset)` | default profile for routed work and tasks without an explicit choice; falls back to the profile selected by `-profiles`, else `implementer` |
| `-subagents` | `true` | allow running agents to delegate child tasks |
| `-subagent-socket` | `subagent.sock` beside the state file | owner-only local control socket |
| `-max-subagent-depth` | `2` | maximum delegation depth below a root task |
| `-max-subagents` | `4` | maximum child tasks one task may spawn |
| `-attempt-retention` | `8` | attempt transcripts kept in full per task; `0` keeps every attempt |
| `-version` | n/a | print the build version and exit |

The state file is opened read-only first, then repaired in memory and saved if
anything changed:

- follow-ups still marked `pending` become `unknown` ("orchestrator stopped
  before acknowledgement"), because delivery cannot be proven;
- legacy tasks without `ThreadID` are given one and a thread record;
- legacy tasks without attempts get one synthesized attempt from their history;
- tasks that were `running` become `interrupted`, and their latest attempt gets
  a finish time and an error. No worker is assumed to have survived.

The state file is opened, then configured command executors are loaded, the
subagent limits and control socket are configured, and the concurrency limit is
set, which also resumes persisted start requests. Then the TUI starts on the
**NEW REQUEST** form. `Shutdown` cancels running work, closes pending start requests,
closes the subagent control socket and waits for workers to finish writing
state. Quitting therefore ends
active tasks as `canceled`, not `interrupted` — interrupted only comes from
reopening state after a crash or kill.

## Lifecycle of a request

The primary path starts at the **NEW REQUEST** form. Pressing Enter there calls
`SubmitRequest` (`router.go`), which resolves a leading `@NAME` executor mention,
removes that token from the request, derives a display title from the first
remaining line, and persists it through the same path as a manual draft. Without
a mention it assigns `general`; an unavailable name or a missing request is
rejected. The decision records the local policy ID/version (`local` / `local-1`),
the trusted local source and actor, and a versioned digest of the policy input. A
`routed` event records the assignment (selected executor, an isolated worktree or shared
workspace as applicable, execution requested). The router then calls `Start`
immediately. The same form lists **NEEDS ACTION** and **RECENT TASKS**. One request becomes one task: the router does
not decompose requests or evaluate them with a model.

1. **Manual submission.** Pressing `n` creates a draft through `CreateTask`
   (`service.go`) instead of routing immediately. It validates title/prompt/
   workspace, classifies whether the mandate explicitly requests a change, and
   selects automatic isolation only for that case when the workspace is a
   repository with a committed `HEAD`. Read-only and ambiguous work stays in the
   source workspace. It records whether the choice was automatic, then
   generates task, trigger and thread IDs, runs the local authorization policy,
   and records the explicit request as an accepted trigger, decision and event.
   All of it is persisted before the call returns; a save failure rolls the
   slices back. A manual draft is `queued` but deliberately not started. An
   explicit `w` choice is preserved across later executor changes; changing the
   executor alone does not reclassify the automatic choice.
2. **Explicit start.** `r` (or the router after routing) calls `Start`. An unavailable executor causes a start error. If a
   slot is free the task starts immediately. Otherwise the start request is
   marked `ScheduleRequested` with a timestamp and a persisted `scheduled`
   event, and the task waits in FIFO order by `ScheduledAt`.
3. **Attempt creation.** `startLocked` (`scheduler.go`) reconciles a persisted
   automatic workspace choice with the immutable mandate, then persists a new
   attempt with its own ID, status `running`, start time and a `started` event
   *before* launching a goroutine. A save failure cancels and restores the task.
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
5. **Execution.** The runner is looked up by agent name (`general` by default).
   `FantasyRunner` implements `LiveRunner`, so `RunLive`
   (`fantasy_runner.go`) drives the attempt in-process through
   `charm.land/fantasy` with the workspace as its directory. It resolves the
   attempt's model from the profile snapshot (a specialist model, the task
   profile model, or the `-model` fallback) and builds one initial prompt
   containing the mandate plus any context under an explicit "not authority to
   expand the mandate" heading. It then streams assistant text deltas,
   thinking-block markers and tool start/end markers to the output writer while
   the seven workspace tools (`read`, `bash`, `edit`, `write`, `grep`, `find`,
   `ls`) run inside the orchestrator process. The non-live `Run` path is the
   same loop without a follow-up channel.
6. **Output.** Each write appends to the task output, trims it to the newest
   256 KiB without splitting a UTF-8 rune, and mirrors it onto the latest
   attempt. Output batches: it is persisted once 8 KiB has accumulated or on
   the next lifecycle change, not on every chunk.
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
   - running tasks get `pending`, then `accepted` or `rejected` after the
     adapter acknowledges the follow-up, or `unknown` if the execution ends or
     the orchestrator stops first;
   - running tasks with no live channel or a full queue get `rejected` with a
     reason.
   Each hand-off keeps a durable `Delivery` record with its trigger, execution
   attempt, state, reason and timestamps. A retry adds records for the new
   attempt instead of rewriting earlier outcomes, and only the current
   attempt's records determine whether context needs action. Acknowledged
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
   process-local and are cleared when the attempt ends. Prompt guidance advertises
   the child budget still remaining after earlier attempts; an exhausted budget
   receives no credentials or coordinator guidance. A top-level `general`
   attempt with delegation and bash access also receives compiled coordinator
   guidance: answer simple tasks directly, split complex or multi-concern work
   into focused self-contained children when the remaining budget permits, run independent
   scopes in parallel and dependent or shared-file work sequentially. The
   guidance grants no authority and does not split work automatically. If capacity
   is unavailable, `subagent` reports the child as queued after a short start
   timeout instead of holding the parent's slot indefinitely.
9. **Reports.** `p` records an operator observation: `progress`, `waiting`,
   `blocked`, `ready` or `failed`, with a message (`report.go`). A runner may
   also record an explicit adapter observation through its output sink; this is
   accepted only while the attempt is running and is stored with an `adapter`
   source and attempt ID. The built-in Fantasy adapter records a `progress`
   report when it accepts the initial prompt, a `waiting` report when it
   retries after a provider error, and a `failed` report on a provider failure
   or when it produces no assistant response, without parsing assistant prose.
   Reports are separate from process status. `Task.Stage()` derives an action
   stage from
   the latest current-attempt report, ignoring reports made before that attempt
   and clearing `blocked`/`waiting`/`failed` once newer context arrives. The
   terminal additionally displays a running parent as `waiting for subagents`
   while a child is queued, running or waiting for worktree integration. Running
   indicators use a blue accent; waiting, completed and failed indicators keep
   their distinct semantic colours and glyphs.
10. **Finish, review and merge.** When the runner returns, the worker records
    `canceled` if its context was canceled, `failed` on a non-nil error,
    otherwise `completed`. Before finalizing a successful isolated attempt, the
    worker validates the workspace reservation again and commits any worktree
    changes with a task-identifying message (`merge.go`). A prepared commit
    opens `pending` review without changing the source checkout. An up-to-date
    completion opens no review. The latest attempt is finalized, control
    credentials are cleared, events are appended, state is saved, and the
    scheduler starts the next waiting task if capacity opened.
    Accepting review persists approval and schedules a serialized merge of the
    reviewed commit into the source repository's current checkout. A clean
    merge records the worktree and merge commits; a conflict or Git failure
    aborts the source merge and records failure separately from the successful
    attempt and approved review. Overlapping dirty source files block merging
    rather than being overwritten or stashed. The operator can clean the source
    and retry. Rejecting review never merges the commit; the worktree remains
    available for a follow-up with additional context and another execution.
    The worktree is retained until deletion or explicit eligible discard.
11. **Retry, cancel, delete.** `R` requeues a failed, interrupted or canceled
    task without changing its mandate or attempts and requires a new `r`; a
    completed isolated task whose approved merge failed is also retryable and
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
  exists, including after an approved merge failure, so retries and review
  can use it. Deleting the task removes the reservation; if cleanup fails, the
  deletion remains durable and the reservation is left in place with an error.
  An explicit discard removes the reservation while keeping the task, using the
  same ownership validation but refusing dirty or unmerged worktrees.

Statuses: `queued`, `running`, `completed`, `failed`, `canceled`,
`interrupted`. A `completed` task means the attempt finished successfully (for
the Fantasy adapter, also that it reported an assistant response); it does not
mean the work is correct or reviewed. For an isolated task, `merge_status` separately records
whether its changes were merged, were already up to date, or failed to merge;
a merge failure leaves the execution attempt completed but makes the task need
action.

Review status is independent of both process status and merge outcome:
`pending` means an isolated task's prepared worktree commit awaits an operator
decision, and `approved`/`rejected` record that decision with its commit, actor,
time and optional note. Review opens when a successful isolated attempt
prepares a worktree commit needing integration; an up-to-date completion opens
no review. The local UI approves and schedules merging with `v`, and rejects
with `V` plus a required reason. The service methods `ApproveReview` and
`RejectReview` perform these transitions. The displayed commit must match the
pending commit. Each decision is persisted with a `review_approved` or
`review_rejected` event before it is acknowledged, and a new attempt clears it.
Review gates merging: only approval permits integration of the reviewed commit.
Rejection retains the worktree; `F` supplies follow-up context and requeues the
task for an explicit start without expanding its mandate.

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
- Prompts are passed to the adapter as an argument, model message or stdin,
  never through a shell.
- Agent output is untrusted: it is stored and displayed, never executed, and is
  sanitized before terminal layout.
- Delivery acknowledgement is reported honestly: an adapter accepting a
  follow-up is not proof that the model consumed it, and crashes leave
  `unknown` rather than success. Delivery records are scoped to the attempt that received them; a
  retry starts new records and does not inherit an older rejection or unknown
  outcome.
- An isolated attempt runs only in the worktree reserved for that task and
  repository; ownership records, directory types, repository identity and the
  recorded commit are validated before reuse and again before merging.
  Preparing a workspace happens before the runner starts, and the source
  workspace is not modified while the agent runs. Successful completion prepares
  a review; only operator approval permits merging. Approved merges are
  serialized, create a merge commit, and abort on conflict instead of leaving
  the source checkout in a partial merge. Merge failure is recorded separately
  from execution success and makes the task need action.
- Automatic isolation is selected only when the mandate explicitly requests a
  change. Read-only inspection requests and requests with no imperative edit
  action share the source workspace. This classification is a lexical
  heuristic, not a sandbox or proof that an executor will not write. The
  selected executor does not create a worktree by itself and changing it does
  not reclassify the automatic choice; an explicit per-task choice overrides
  the automatic one.
- Review is a separate operator record and a merge gate. A prepared worktree
  commit opens review before integration. Approval is matched to that commit
  and persisted before merging is scheduled; rejection leaves it unmerged.
  A new attempt clears review. Decisions never rewrite earlier merge history.
- A subagent is a new task with its own mandate, thread, decision and attempt;
  it is never a mutation of the parent's mandate. Spawn records are persisted
  before the child starts, and a failed start remains visible as a queued task
  with an event. Agent-initiated requests require a running parent and valid attempt
  credentials, and depth and child limits bound fan-out. Coordinator guidance
  encourages focused fan-out for complex work but is trusted prompt text only:
  the router does not split requests, and nothing guarantees that children
  coordinate their edits.
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
- The state file is intended for one running instance. An instance lock
  (see `instance.go`) refuses a second instance on the same path; concurrent
  instances are not supported. The whole state file is rewritten on each save,
  but streamed output batches to 8 KiB, so chatty runs do not rewrite on every
  byte.
- Tasks share the source workspace unless their mandate contains a detected
  change action. Automatic isolation is enabled by default when the source
  workspace is a Git repository with a committed `HEAD`, and can be disabled
  with `-isolation=false` or per task before start. The check is lexical over
  request phrases; the selected executor does not imply change work, so a
  read-only or ambiguous request stays shared. Classification uses the mandate
  as a heuristic, so a misleading request can still lead to writes in the shared
  workspace; an explicit `w` choice enables a worktree.
  Isolation starts from that commit: uncommitted source files are absent, a
  repository with no commit cannot be isolated, and the `git` executable is
  required. Successful isolated tasks prepare commits for review. Accepting
  review automatically schedules integration; rejection retains unmerged work
  for inspection or follow-up. There is no in-app diff or conflict
  resolution, and a conflicting merge must be resolved outside the application.
  Worktrees for retained tasks accumulate beside the state file; deleting a task
  cleans up its worktree, and `x`/`X` discard merged worktrees while keeping the
  task, refusing dirty or unmerged worktrees. A dirty source checkout can make
  an otherwise clean worktree merge fail without changing the worktree; the
  failure names the overlapping files and the worktree is kept for retry after
  the source is cleaned. Tasks in workspaces that cannot be isolated, or that
  are explicitly opted out, share their workspace.
- There is no session continuity across restarts or retries: every attempt
  starts a fresh conversation. Follow-ups accepted during an attempt are not
  replayed after a restart.
- Output is capped at the newest 256 KiB per task/attempt; older text is
  discarded. State may contain sensitive prompts and output and is stored
  unencrypted (directory `0700`, file `0600`).
- Operator reports and Fantasy adapter reports exist; configured command adapters
  do not emit structured reports and there is no report-driven routing policy.
  Free-form agent output is never parsed into reports. Capability declarations
  are informational; only the built-in Fantasy adapter currently declares tool
  activity and reports.
- Running live context is bounded (64 pending messages per task) and sends are
  rejected rather than blocked when full. Delivery records are retained with
  task history and are not compacted independently.
- The TUI text input supports append and backspace only.
- The Fantasy adapter is built in; configured command executors can be added
  with `-agents` as a separate opt-in. Both run behind `Runner`/`LiveRunner`:
  Fantasy supports live follow-ups and structured tool activity, while a
  configured command executor is one-shot.
- The router is deterministic and creates one task per request. It does not
  interpret or decompose requests with a model. It uses a leading `@NAME` to
  select a registered executor and otherwise falls back to general. An unknown
  or incomplete mention is rejected. Explicit selection and delegation can still
  choose the implementer. The router applies the default workspace choice and
  starts the task. Manual drafts remain for other executors, per-task isolation
  changes and other pre-start changes.
- Subagent delegation is bounded but not sandboxed: a child has the same host
  permissions and shares the parent's workspace, so concurrent file edits are
  not coordinated. A child is not canceled when its parent ends, and the parent
  helper receives the child's final output only when the child completes.
  Commands that an agent spawns through its own `bash` tool are not pilharmonic
  tasks and remain outside these tracking, limit and cancellation rules.
- The control socket uses a Unix socket, so agent-driven delegation is not
  available on platforms without Unix-domain sockets.

## Persistence benchmark

Run `go test ./internal/orchestrator -run '^$' -bench BenchmarkStreamingPersistence -benchmem`.
The benchmark starts a fake runner and writes 1 KiB chunks through its real
output sink while a goroutine requests task snapshots at 10 Hz. The two
fixtures contain zero or 32 completed tasks, each with 64 KiB of output
mirrored in its attempt. The active transcript grows until it reaches its cap.

Streamed output batches: the writer persists once 8 KiB has accumulated since
the last save, so the benchmark measures one write every eight chunks. On a
Linux amd64 i7-10610U, one local run measured 0.09 ms and 0.18 MB allocated
per 1 KiB chunk with no history, versus 1.31 ms and 1.55 MB with 32 completed
tasks. These figures include JSON encoding and replacing the state file on
local temp storage, and the rename is followed by a directory fsync. Disk
type, history size and stream length affect the result. Lifecycle changes
still save synchronously and carry any buffered output with them, so a crash
can lose at most 8 KiB of streamed output while every decision, event and
attempt result remains durable.
