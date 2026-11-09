# Design notes

Orchestrator is a local, single-machine application. It adapts the trigger,
thread, mandate and execution concepts from DeepSeek's
[domain model](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/main/docs/001-domain.md)
and [execution design](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/main/docs/003-execution.md).

## Ownership

The CLI opens the service, configures executors and delegation, resumes pending
starts and runs the terminal. The service owns routing, scheduling, history
and workspace management. The terminal displays snapshots and submits
operator actions. Each executor owns its subprocess invocation and protocol.
See the [executor contract](executors.md).

## Requests and authority

- A trigger identifies an incoming request in a thread. A compiled-in policy
  evaluates the normalized request. An accepted task records its mandate,
  trigger, decision, policy identity and input digest. The local router selects
  an executor from the opening action of the mandate and requests a start. A manual draft waits for the operator.
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
the running attempt so a late callback cannot change a retry.

Each hand-off has a trigger and an attempt. Queued context travels with the
initial mandate. For live context, the service records whether Pi accepted or
rejected the command, or whether acknowledgement is unknown. Pi's
acknowledgement establishes command receipt only. Operator and adapter reports
can flag a task as needing attention independently of process status.

The JSON state file is for one application instance. The service protects
in-memory state with a mutex and replaces the file on save. UI snapshots and
worker inputs must be independent copies wherever callers can mutate them.

## Workspaces and integration

New change work defaults to a dedicated Git worktree when the source repository
has a committed HEAD. Read-only inspection requests and tasks assigned to an
inspection specialist provided by the bundled Pi adapter that request no edit keep the shared source workspace.
The classification combines the request opening and selected role with
imperative edit actions in the mandate; requests matching neither inspection
signal default to isolation, and an explicit task choice overrides it. The
scheduler checks an automatic shared choice against the configured executor
before launch, including after a restart. The worker prepares the worktree before execution, checks the reservation before
reuse and merging, and preserves it across retries. Shared workspaces are
available by startup or task choice and are the fallback when isolation is
unavailable. Delegated children inherit the parent's execution workspace instead
of being classified separately. Agents retain the host user's permissions.

After a successful isolated run, the service commits pending changes and
attempts a serialized merge into the source checkout. A failed merge leaves
the execution successful, records the integration error and retains the
worktree. Successful integration or a failed merge with a prepared commit
opens that commit for operator review. Review records the operator's decision
against the commit displayed in the UI. A stale decision leaves a newer
review pending. An up-to-date completion opens no review.

An operator can discard a clean, integrated worktree while keeping the task.
Deleting a task makes the deletion durable before cleaning up its reservation.
Parent deletion requires that its children have been deleted or are part of the
same bulk deletion. An inherited worktree stays reserved while any descendant
references it, including a finished task that can receive a follow-up. When
descendants are queued or running, the owner releases its process slot and
records a pending merge. The scheduler revisits that merge after startup and
as work finishes. An unsuccessful descendant blocks integration for inspection.

Discard saves its intent before removing anything. On restart the service
retains a surviving tree for an explicit retry, or finishes clearing the
reservation and execution path if the tree was already removed. The UI captures
the task IDs at confirmation and runs cleanup in a Bubble Tea command.

## Output and tool activity

Pi translates structured events into assistant text, thinking boundaries,
tool markers, reports and activity records. Transcript markers control display
only. The activity view uses recorded calls keyed by attempt and tool-call ID.
A call retains its arguments, result, status and duration when available.
Unfinished calls become interrupted when an attempt ends or the service
recovers. Each task retains up to 200 calls, with argument and result previews
capped at 16 KiB before JSON wrapping. Output retains the latest 256 KiB per
attempt.

The terminal renders assistant prose as Markdown, collapses thinking by
default and combines tool start and finish markers into status rows. Pi closes
an open thinking block before emitting prose or tool activity to handle late
thinking-end events. Agent output is untrusted display data.

For a walkthrough of startup, state migration, request routing, execution and
cleanup, see the [implementation notes](status.md).

The CLI registers the bundled specialists before resuming scheduled tasks. A specialist selects the executor instructions. A profile configures execution settings and can apply to any compatible executor. The terminal omits the built-in profile from summaries when it supplies no overrides. Each bundled specialist uses the Pi adapter with role-specific system instructions. Explicit command definitions take precedence over bundled names.

An optional `-specialist-tools` JSON file can configure nonempty Pi tool
allowlists for bundled specialists, for example
`{"reviewer":["read","grep","find","ls"]}`. Entries for non-bundled
specialists are invalid. The effective tools are the intersection of the
specialist allowlist and the task profile's tool list when both are present.
Delegated tasks retain the parent's effective tool limit, even when their
specialist or profile would otherwise allow more. Omitting the option preserves
existing tool access behavior. Tasks snapshot the specialist ceiling when assigned
and attempts record the effective tools. A newly configured limit can further
narrow a retry. Disjoint lists prevent the executor from starting.

The profiles JSON can select the default profile and assign models and tools to
bundled Pi specialists. Startup registers executors before validating these
settings and resuming queued work. The Pi adapter passes the specialist model,
or the task profile model when omitted, on each execution. The attempt records
that selector independently of the inherited profile name.

A profile can supply coordinator instructions for the top-level general task. The
scheduler adds them to its delegation guidance when the attempt has delegation
credentials and shell access. Specialist and child prompts omit this guidance
for the coordinator role. The model decides when to call the helper.
