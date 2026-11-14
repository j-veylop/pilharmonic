# Design notes

pilharmonic is a local, single-machine application. It adapts the trigger,
thread, mandate and execution concepts from DeepSeek's
[domain model](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/main/docs/001-domain.md)
and [execution design](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/main/docs/003-execution.md).
This file keeps invariants and boundaries; the README is the user guide.

## Ownership

The CLI opens the service, configures executors and delegation, resumes pending
starts and runs the terminal. The service owns routing, scheduling, history and
workspaces. The terminal displays snapshots and submits operator actions; it
holds no authoritative task state. Each executor owns its own invocation and
protocol behind a narrow interface (see the [executor contract](executors.md)).
Git worktree operations, provider resolution, the agent workspace tools and the
state-file lock live in leaf packages under `internal/` that never import the
service; the service passes them plain values, not tasks.

## Requests and authority

- A trigger identifies an incoming request in a thread. One compiled-in
  authorization step turns a normalized request (source, actor, purpose,
  trigger/thread/parent identity, mandate, workspace, depth) into an accept or
  reject decision. An accepted task records its mandate, trigger, decision,
  policy ID/version and a versioned SHA-256 digest of the exact input
  (`orchestrator.authorization/v1`). Rejected requests create no task and are
  returned as errors.
- A leading `@NAME` selects a registered executor and is removed from the
  mandate; otherwise the router assigns `general`. Top-level routing never infers
  an executor from the request text. An unknown or incomplete mention is
  rejected. A held request (**Ctrl-s** in the request box) is
  accepted queued and waits for the operator.
- A related task in the same thread has its own mandate and decision and
  snapshots the default profile at acceptance. A thread follow-up records the
  subscribed queued and running recipients at submission. Pausing a subscription
  affects future inputs; resuming does not replay missed ones.
- A task follow-up adds context; for a terminal task it also requeues it.
  Neither changes the mandate. Later context never widens a mandate.
- A running task can delegate through the same authorization step (subagent
  source). The child gets a new thread, mandate, decision and parent link,
  inherits the execution workspace and the parent profile's tool list, and uses
  the parent executor as fallback. Depth and child limits bound delegation; a
  child's lifecycle is independent once it exists.

External sources would need authenticated identities, stable request IDs,
idempotent decisions and durable rejection records; none exist yet.

## Profiles and models

A profile (tools, model, instructions, integration, delegation) is orchestrator
data. The service snapshots the resolved profile when a task is accepted and
records it on each attempt; later configuration edits do not change existing
tasks. A `profiles.json` entry overlays the matching built-in field by field
(omitted keeps, explicitly empty clears); the whole file validates before any
entry registers.

Specialist tool limits intersect with the task profile's tools; disjoint lists
prevent the start. An executor that cannot honor a profile's tools or model
fails the start instead of ignoring them. A profile is not a sandbox.

The service resolves a Fantasy attempt's model as the task profile model, else
the agent's class model (`models.json`; `general` is big, specialists small),
else the specialist model (`profiles.json`), else `-model`. A profile or
specialist model of `big` or `small` stands for that class's model, and a
specialist alias also sets its class; an alias whose class is unset is skipped,
never sent to a provider. It fixes the selector on the attempt and the adapter
runs exactly that selector. The provider prefix selects the endpoint
and credential.

The compiled coordinator guidance is added only for a top-level `general`
attempt with delegation credentials, bash, and remaining child budget. It grants
no authority beyond the mandate, and the router never splits work.

## Attempts, delivery and storage

- A start request waits in FIFO order for a free slot. The attempt is persisted
  before its worker launches. Retry reuses the task and workspace and keeps
  earlier attempts.
- Output, reports, tool activity, usage and live acknowledgements are bound to
  the running attempt ID, so a late callback cannot change a retry.
- An optional idle timeout cancels an attempt with no output for the configured
  duration; every streamed chunk resets it.
- Live follow-up delivery is recorded as accepted, rejected or unknown.
  Acknowledgement establishes receipt only.
- Lifecycle changes persist before they are acknowledged, with rollback on
  synchronous save failure. The JSON state file is replaced atomically with
  file and directory fsync. A background save failure and startup warnings are
  surfaced on the terminal notice line.
- Streamed output persists every 8 KiB. Output keeps the latest 256 KiB per
  attempt; older attempts beyond `-attempt-retention` drop their output but keep
  identity, status, timing and error. Each task keeps up to 200 tool calls with
  16 KiB previews.
- One process per state file, enforced by an exclusive `.lock` beside it.
- UI snapshots and worker inputs are independent copies. Shared state is
  guarded by the service mutex, never held across subprocess or network calls.

Token usage is execution metadata, priced when recorded (time-of-day rates
apply at that moment). Unpriced models report tokens without a cost. The
session total covers attempts started by this process.

## Workspaces and integration

Tasks run in the shared source workspace unless the mandate opens with an
imperative edit action, the source has a committed HEAD and isolation is
enabled; then the task gets a detached Git worktree. This is a lexical
heuristic, not proof. An explicit task choice overrides it, and the scheduler
reconciles a persisted automatic choice with the immutable mandate. Children
inherit the parent's execution workspace. Agents keep the host user's
permissions.

After a successful isolated run the service commits pending changes and opens a
review of that exact commit. Approval persists the decision and schedules a
serialized merge; a stale decision leaves the newer review pending. A failed
merge keeps the execution successful, records the error and retains the
worktree. Git autostashes touched local edits; if reapplying fails the merge
completes with a recorded warning. Rejection leaves the source unchanged.

A parent with queued or running descendants on its worktree releases its slot
and records pending integration; an unsuccessful descendant blocks integration.
Deletion is durable before cleanup, and a parent can be deleted only after (or
together with) its children. Discard saves its intent first; on restart a
surviving tree is kept for retry, a removed one is finished clearing.

## Output

Agent output is untrusted display data: it is never executed, and prompts are
passed as process arguments or stdin, never through a shell. Transcript markers
control display only. Unfinished tool calls become interrupted when an attempt
ends or the service recovers.

## Startup and recovery

`go run . -help` lists the flags. The state file is opened read-only, repaired
in memory and saved if anything changed:

- pending follow-ups become `unknown`, because delivery cannot be proven;
- legacy tasks without a thread or attempts get them synthesized;
- `running` tasks become `interrupted`; no worker is assumed to survive.

`Shutdown` cancels running work, closes pending starts and the control socket,
and waits for workers to finish writing, so quitting yields `canceled`, not
`interrupted`.

The public name changed without a storage migration: state stays under
`<user config dir>/orchestrator/`, subagent variables keep the `ORCHESTRATOR_`
prefix, and the engine package remains `internal/orchestrator`.

## Open work

1. Authenticated external sources and durable rejection records.
2. Structured reports for command adapters; richer assignment policy.
3. In-app diff and conflict resolution for worktrees.
4. Model-assisted evaluation only behind the deterministic authorization gate.
