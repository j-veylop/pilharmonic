# Design notes

pilharmonic is a local, single-machine application. It adapts the trigger,
thread, mandate and execution concepts from DeepSeek's
[domain model](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/main/docs/001-domain.md)
and [execution design](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/main/docs/003-execution.md).

## Ownership

The CLI opens the service, configures executors and delegation, resumes pending
starts and runs the terminal. The service owns routing, scheduling, history
and workspace management. The terminal displays snapshots and submits
operator actions. Each executor owns its own invocation and protocol.
See the [executor contract](executors.md).

## Terminal vocabulary

The main view leads with a **NEW REQUEST** form, reached with `/` or on startup.
The local policy checks a submitted request before it becomes a task. `n` creates
a task directly as a queued draft without the request form's automatic routing;
its request is still checked by the local policy. The form lists
**NEEDS ACTION** and **RECENT TASKS**. Help (`?`) opens on **Concepts**, which
defines request, task, route, action and attempt. On the task board, the
`! action` hint selects the next task needing action in grouped display order
and wraps at the end. Needs
action covers blocked, waiting, awaiting review, input undelivered, merge
failed, failed and interrupted tasks.

## Requests and authority

- A trigger identifies an incoming request in a thread. A compiled-in policy
  evaluates the normalized request. An accepted task records its mandate,
  trigger, decision, policy identity and input digest. A leading `@NAME` token
  selects a registered executor and is removed from the mandate; otherwise the
  local router assigns `general` and requests a start. Top-level routing does not
  infer an executor from the opening action. The implementer is available
  through an explicit mention or task choice, and through automatic or named
  delegated selection. An unknown or incomplete mention is rejected. A manual
  draft waits for the operator.
- A related task in the same thread has its own mandate and decision. It records
  the current default profile at acceptance, just like a new manual task. A thread
  follow-up records the subscribed queued and running recipients at submission.
  Pausing a subscription affects future inputs. Resuming it does not replay
  missed inputs.
- A task-specific follow-up adds context to that task. For a terminal task it
  also requeues the task for an explicit start. Neither operation changes the
  mandate.
- A running task can delegate a bounded request through the subagent policy.
  The child gets a new thread, mandate, decision and parent link. It inherits
  the execution workspace and uses the parent executor as the fallback. A delegation-enabled
  parent receives a catalog of registered executors and their configured
  descriptions. It can select a named specialist for the child mandate. Depth and child limits
  bound delegation. An isolated parent can delegate once its execution workspace
  is ready. A child's lifecycle is independent once it exists.

The local and subagent policies are compiled in. External triggers would need
authenticated identities, stable request IDs, idempotent decisions and durable
authorization evidence. See the [routing specification](routing-spec.md).

## Attempts, delivery and storage

A start request can wait in FIFO order for a free slot. The service persists
the attempt before launching its worker. An explicit retry reuses the task
and workspace and retains earlier attempts. On restart, the service marks
running attempts interrupted and pending live deliveries unknown. Persisted
start requests resume after the executor registry and concurrency limit are
configured. Output, reports, activity and live acknowledgements are scoped to
the running attempt so a late callback cannot change a retry. An optional idle
timeout cancels an attempt whose runner produces no output for the configured
duration; every streamed chunk resets the timer, so a thinking agent is never
interrupted.

Each hand-off has a trigger and an attempt. Queued context travels with the
initial mandate. For live context, the service records whether the adapter
accepts or rejects the follow-up, or whether acknowledgement is unknown. That
acknowledgement establishes receipt only. Operator and adapter reports
can flag a task as needing action independently of process status.

The JSON state file is for one application instance. The CLI holds an
exclusive instance lock (`.lock` beside the state file) so a second process
fails to start instead of racing the first. A task keeps the transcripts of its
newest attempts in full; older attempt records keep their identity, status,
timing and error while their copied output is dropped, so retries cannot grow
the file without bound. The service protects
in-memory state with a mutex and replaces the file on save, fsyncing the file
and its parent directory so a replacement survives power loss. Lifecycle
changes persist before they are acknowledged. Streamed output batches and
persists once 8 KiB accumulates, which bounds output loss to 8 KiB while
keeping every decision, event and attempt durable. UI snapshots and
worker inputs must be independent copies wherever callers can mutate them.

The public name changed to pilharmonic without a storage migration. The default
state path remains `<user config dir>/orchestrator/state.json`, and workspaces and
the subagent socket stay beside it. Subagent environment variables keep the
`ORCHESTRATOR_` prefix, authorization digests keep
`orchestrator.authorization/v1`, and the engine package remains
`internal/orchestrator` with the module path `pilharmonic`.

## Workspaces and integration

Tasks run in the shared source workspace unless their mandate explicitly
requests a change. When the source repository has a committed HEAD and startup
isolation is enabled, an imperative edit action in the request starts the task
in a dedicated Git worktree. Read-only inspection requests and requests with no
detected edit action keep the shared workspace, so ambiguity does not reserve a
tree. The classification is a lexical request-opening heuristic, not proof of
what an executor will do; the selected executor does not establish change work
by itself, and a later executor change does not alter the automatic choice. An
explicit task choice overrides it. Before preparing a workspace, the scheduler
reconciles a persisted automatic choice with the immutable mandate, so an
ambiguous task cannot reserve a tree. The worker prepares the worktree before execution, checks the reservation before
reuse and merging, and preserves it across retries. Shared workspaces are the
default, available by startup or task choice, and the fallback when isolation is
unavailable. Delegated children inherit the parent's execution workspace instead
of being classified separately. Agents retain the host user's permissions.

After a successful isolated run, the service commits pending changes and
opens the prepared commit for operator review without merging it into the source
checkout. Accepting the review persists the decision and schedules a serialized
merge of that exact commit. A failed merge leaves the execution successful,
records the integration error and retains the worktree. Rejecting the review
leaves the source unchanged and retains the worktree for inspection or a task
follow-up with additional context; the follow-up requeues work without changing
its mandate. Review decisions must match the commit displayed in the UI. A stale
decision leaves a newer review pending. An up-to-date completion opens no review.

An operator can discard a clean, integrated worktree while keeping the task.
Deleting a task makes the deletion durable before cleaning up its reservation.
Parent deletion requires that its children have been deleted or are part of the
same bulk deletion. An inherited worktree stays reserved while any descendant
references it, including a finished task that can receive a follow-up. When
descendants are queued or running, the owner releases its process slot and
records pending integration. The scheduler revisits preparation or an approved
merge after startup and as work finishes. Preparing work after descendants finish
still requires review before merging. An unsuccessful descendant blocks
integration for inspection.

Discard saves its intent before removing anything. On restart the service
retains a surviving tree for an explicit retry, or finishes clearing the
reservation and execution path if the tree was already removed. The UI captures
the task IDs at confirmation and runs cleanup in a Bubble Tea command.

## Output and tool activity

The Fantasy adapter translates streamed agent events into assistant text,
thinking boundaries, tool markers, reports and activity records. Transcript
markers control display
only. The activity view uses recorded calls keyed by attempt and tool-call ID.
A call retains its arguments, result, status and duration when available.
Unfinished calls become interrupted when an attempt ends or the service
recovers. Each task retains up to 200 calls, with argument and result previews
capped at 16 KiB before JSON wrapping. Output retains the latest 256 KiB per
attempt.

The terminal renders assistant prose as Markdown, collapses thinking by
default and combines tool start and finish markers into status rows. The
adapter closes an open thinking block before emitting prose or tool activity to
handle late thinking-end events. Agent output is untrusted display data. Task
status uses
semantic colours and glyphs: running is blue, while a running parent whose child
is still queued, running or integrating is displayed as waiting for subagents.
This changes display state only; the parent process and its persisted status are
unchanged.

The task-board header summarises the task count, the displayed running state,
the queued process state and the derived action count. Because a running
parent with active children is displayed as waiting for subagents, it is not
counted as running. The board footer renders actions valid for the selected
task in the current mode, using the same stage and service guards as the keys:
review decisions for a pending commit, cancel and activity for a running task,
start for an unscheduled queued task, and retry for a retryable task. Modal,
composer, activity and detail-focus footers keep their own controls. Optional
hints are dropped when they would exceed two footer lines. On the board, `!`
selects the next task that needs action in grouped display order and wraps;
request and task composers retain `!` as text. When the sidebar is hidden, the
detail title prefixes the selected task's grouped position so narrow layouts
still show where the selection sits.

Mouse drag selection is limited to the detail content pane. Releasing a
nonempty selection writes its plain text as an OSC 52 clipboard sequence,
serialized with renderer output so the escape codes cannot be split. Sidebar,
header, tabs, footer and modal overlays are excluded. The copy is best effort:
terminals that disable OSC 52 ignore it, and the UI does not change task state.
A drag freezes the visible content and its cell mapping at press time; motion
and release reuse that snapshot. If new output, scrolling or a resize changes
the mapping, the pending selection is canceled rather than copying text the
pointer no longer covers.

For a walkthrough of startup, state migration, request routing, execution and
cleanup, see the [implementation notes](status.md).

The CLI registers the bundled specialists before resuming scheduled tasks. A
specialist selects the executor instructions. A profile configures execution
settings and can apply to any compatible executor. The built-in profile catalog
contains `implementer`, the routing default and compatibility key, and
`coordinator`, the top-level coordination profile selected by the shipped
configuration. Both start without tools, a model or instructions, so they
inherit adapter defaults and the compiled coordinator guidance. The terminal
omits a built-in profile name from summaries when its snapshot supplies no
overrides. Each bundled specialist uses the Fantasy adapter with role-specific
system instructions. Explicit command definitions take precedence over bundled
names.

A profiles JSON entry overlays the matching built-in definition field by field;
an omitted field keeps the compiled value and an explicitly empty field clears
it. A new name extends the catalog from an empty profile. `default_profile`
selects the routing default. Validation covers the whole file before any entry
is registered, and the service snapshots the resolved profile with the accepted
task, so later configuration edits do not change existing tasks or attempts.

An optional `-specialist-tools` JSON file can configure nonempty tool
allowlists for bundled specialists, for example
`{"reviewer":["read","grep","find","ls"]}`. Entries for non-bundled
specialists are invalid. The effective tools are the intersection of the
specialist allowlist and the task profile's tool list when both are present.
Delegated tasks retain the parent's effective tool limit, even when their
specialist or profile would otherwise allow more. Omitting the option preserves
existing tool access behavior. Tasks snapshot the specialist ceiling when assigned
and attempts record the effective tools. A newly configured limit can further
narrow a retry. Disjoint lists prevent the executor from starting.

The profiles JSON can select the default profile, modify built-in profiles and
assign models and tools to bundled specialists. Startup registers executors
before validating these settings and resuming queued work. The Fantasy adapter
resolves the model for each attempt as the specialist model, else the task
profile model, else the `-model` fallback, and fixes that selector on the
attempt independently of the inherited profile name.

A profile may add coordinator instructions for the top-level general task. The
canonical delegation workflow is compiled in, so the shipped profiles file does
not repeat it. The scheduler adds that compiled guidance plus any additional
profile instructions when the attempt is top-level, uses the general executor,
has delegation credentials and can use bash. Each attempt sees the child budget
still remaining after children created by earlier attempts. The guidance asks
the model to answer simple tasks directly; for complex or multi-concern work, to
delegate multiple
focused, self-contained children when the remaining child budget permits; to run
independent scopes in parallel and dependent or shared-file work sequentially;
and to verify child evidence without assuming coordination. An exhausted child
budget receives no delegation credentials or coordinator guidance. The guidance
grants no authority beyond the original mandate, and the router does not split
work automatically. Specialist and child prompts omit this guidance for the
coordinator role. The model decides when to call the helper. Profile instructions
and tools remain bounded by the task snapshot and delegation containment; a
child cannot widen the parent's tools, integration or delegation permissions.
