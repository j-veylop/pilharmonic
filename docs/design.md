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

The main view leads with a **NEW REQUEST** form, reached with `/` and opened on
startup once a provider credential is configured; without one, startup opens the
login menu instead.
The compiled-in authorization step checks a submitted request before it becomes
a task. `n` creates a task directly as a queued draft without the request form's
automatic routing; its request is still checked by the same step. The form lists
**NEEDS ACTION** and **RECENT TASKS**, each showing up to eight rows. Tab moves
from the draft to a list, skipping an empty one, `j`/`k` select a row and Enter
opens that task on the board; the focused list scrolls inside the panel so the
selected row stays visible. Tab and Shift-Tab cycle the focus back to the
draft, which keeps its text while a list owns the keys. The help overlay is
reachable from every screen: F1 opens and closes it anywhere, including inside
the draft, an API key prompt and a custom model ID, while `?` opens it only
where that character is not part of the text being typed, so the board, the
request form's lists, the pickers, the confirmations and the login, model and
agent-class menus open it but no draft loses the character. Sending `/help` in
the request form opens the same overlay, whose **Commands** topic lists the
slash commands. Help starts on **Concepts**, which defines request, task,
route, action, attempt and review; `h`/`l` switch topics, `j`/`k` scroll, and
closing it returns to the surface it covered with the draft, selection and menu
intact. On the task board, the `! action` hint selects the next task needing
action in grouped display order and wraps at the end. Needs action covers
blocked, waiting, awaiting review, input undelivered, merge failed, failed and
interrupted tasks.

The help overlay keeps one shape while the operator pages through it. A title
row names the topic and puts its position, such as `2/7`, against the right
edge. The topic strip below it marks the active topic with `▸` in a marker
column every tab reserves, so the strip keeps its cells instead of shifting when
the reader changes topic, and a faint rule under the strip sets the reference
apart from its header. Every topic shares one shortcut column, measured across
the whole overlay, so a description never slides sideways when the topic
changes, and the scrollbar column is reserved on every topic, so a long topic
scrolls without re-wrapping the text of a short one. The frame is only as tall
as the tallest topic needs at the current width, never taller than the screen
allows, so paging never resizes it. Section headings are labels followed by a
rule to the body width, and the hint row is rendered by the bubbles help view,
which drops hints that no longer fit rather than overflowing the border. When
the frame is too narrow for the titles the strip falls back to topic positions
and then to nothing, because the title row already names the topic.

The request form and every composer render as one input box docked at the
bottom of the screen. Its label sits on the box's top border, the draft wraps
inside it and grows to at most six rows, and the footer below keeps the key
hints for that mode. The shared editor follows the usual textarea shortcuts:
`Ctrl-w` and `Alt-backspace` delete the previous word, `Alt-d` the next one,
`Ctrl-k` and `Ctrl-u` delete to the line end and the line start, `Delete` or
`Ctrl-d` removes the next character, `Ctrl-arrow` or `Alt-b`/`Alt-f` move by
word, and `Home`/`End` or `Ctrl-a`/`Ctrl-e` jump to the line bounds. Word
operations stay on their own line, so a deletion never removes a line break; at
a line boundary `Ctrl-k` and `Ctrl-u` join the neighbouring line.

## Requests and authority

- A trigger identifies an incoming request in a thread. One compiled-in
  authorization step evaluates the normalized request and writes its audit
  record. An accepted task records its mandate, trigger, decision, the recorded
  policy identity and the input digest. A leading `@NAME` token
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
- A running task can delegate a bounded request through the same authorization
  step, using the subagent source. The child gets a new thread, mandate,
  decision and parent link. It inherits
  the execution workspace and uses the parent executor as the fallback. A delegation-enabled
  parent receives a catalog of registered executors and their configured
  descriptions. It can select a named specialist for the child mandate. Depth and child limits
  bound delegation. An isolated parent can delegate once its execution workspace
  is ready. A child's lifecycle is independent once it exists.

Authorization is a single compiled-in step; the trusted local source and the
subagent source both pass through it. External triggers would need
authenticated identities, stable request IDs, idempotent decisions and durable
authorization evidence. A normalized request (source, actor, purpose,
trigger/thread/parent identity, mandate, workspace and depth) deterministically
becomes an accept or reject decision plus an audit record. Every task decision
records that policy ID/version, source, actor and a versioned SHA-256 digest of
the exact input, so a reviewer can recompute what was authorized. Rejected
requests create no task and are returned as errors rather than persisted as
audit records.

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
records the integration error and retains the worktree. Git stashes uncommitted
source files that the merge touches and reapplies them afterwards, so local
edits do not block integration; when they cannot be reapplied the merge still
completes, keeps them in an autostash entry and records a merge warning. Rejecting the review
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
default and combines tool start and finish markers into rows whose status is a
single glyph. A completion folds into its start row, and a row names the tool
before its summary, so a failure shows the tool and its message rather than the
message alone; a failure that carries only a message joins the only unfinished
call instead of becoming an orphan line, while a failure that names a tool the
block has no start row for keeps its own row so it cannot mislabel another call.
The activity panel uses the same glyph-only status for each recorded call. The
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
counted as running. It also shows the session's token usage and estimate in the
gap before the mode badge once an adapter has reported any, shortening that
segment before the counters lose their wording, so naming what needs action
always wins over a total the detail panel can show in full. The board footer
renders actions valid for the selected task in the current mode, using the same stage and service guards as the keys:
review decisions for a pending commit, cancel and activity for a running task,
start for an unscheduled queued task, and retry for a retryable task. Modal,
activity and detail-focus footers keep their own controls, while the request
form and composers keep their editing hints below the docked input box.
Optional hints are dropped when they would exceed two footer lines. Every
footer advertises help, naming `? help` on the board, in activity and in
detail focus and `f1 help` in the request form, the composers, the pickers,
the confirmations and the menus, because F1 is the help key a text field leaves
free; the help hint is dropped only when that footer has no room for it. On
the board, `!` selects the next task that needs action in grouped display
order and wraps; request and task composers retain `!` as text. When the
sidebar is hidden, the detail title prefixes the selected task's grouped
position so narrow layouts still show where the selection sits.

Mouse drag selection is limited to the detail content pane. Releasing a
nonempty selection writes its plain text as an OSC 52 clipboard sequence,
serialized with renderer output so the escape codes cannot be split. Sidebar,
header, tabs, footer and modal overlays are excluded. The copy is best effort:
terminals that disable OSC 52 ignore it, and the UI does not change task state.
A drag freezes the visible content and its cell mapping at press time; motion
and release reuse that snapshot. If new output, scrolling or a resize changes
the mapping, the pending selection is canceled rather than copying text the
pointer no longer covers.

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
Delegated tasks inherit the parent profile's tool list, even when their
specialist or profile would otherwise allow more. Omitting the option preserves
existing tool access behavior. Tasks snapshot the specialist ceiling when assigned
and attempts record the effective tools. A newly configured limit can further
narrow a retry. Disjoint lists prevent the executor from starting.

The profiles JSON can select the default profile, modify built-in profiles and
assign models and tools to bundled specialists. Startup registers executors
before validating these settings and resuming queued work. Each Fantasy agent
belongs to a model class, `big` or `small`; only `general` defaults to `big`,
the specialists to `small`. The operator picks a selector per class and can
override the class of any Fantasy agent; both persist in `models.json` beside
the state file. The Fantasy adapter resolves the model for each attempt as the
class model, else the runner's configured model (a specialist model from
`profiles.json`, or the `-model` fallback when the specialist has none), else
the task profile model, and fixes that selector on the attempt independently of
the inherited profile name. The provider prefix of that selector picks a
registry entry; every entry but `bedrock` is an OpenAI-compatible endpoint with
an API-key environment variable, while `bedrock` is built by Fantasy's AWS
provider so the AWS credential chain can supply credentials instead of a stored
key. Bedrock resolves its region through the AWS SDK, which is why that provider
is the one entry with a dependency beyond the OpenAI-compatible client. A
provider that has no HTTP model list (Bedrock) reports that instead of failing,
and the model menu falls back to its curated catalog.

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
child cannot widen the profile's tool list, integration or delegation
permissions beyond what it inherits.
## Usage and cost accounting

Token usage is execution metadata, not authority. The Fantasy adapter records the
total one stream call reported (`AgentResult.TotalUsage`, which already folds the
retried attempts of a step) through the attempt's output sink, so the tokens land
on the attempt that ran and a late or stale report is refused like any other
write scoped to a running attempt. A report is attributed to the model selector
the adapter asked for and priced when it arrives. An adapter that reports nothing
leaves the attempt without a usage record, rather than implying the run cost
nothing.

Costs are estimated at recording time, because a provider with time-of-day
pricing charges the rate in force then, not the rate in force when the operator
reads the total. The price table is a curated snapshot in US dollars per 1M
tokens. It is advisory bookkeeping, not billing: it covers the OpenAI and
DeepSeek models listed in the model catalog, and a selector without an entry is
left unpriced. A session that counted an unpriced model reports its tokens with
that model named in `Unpriced` instead of a dollar figure, so no surface prints a
cost it cannot support. DeepSeek discounts a daily off-peak window (00:30–08:30
Beijing time, which is 16:30–00:30 UTC), so its entries carry the discounted pair
as well and `ModelPrice.Rate` names the window in force.

A session is one orchestrator process. `Service.SessionUsage` sums the usage
recorded by attempts that started after the process opened, which includes
delegated children because they are tasks of the same service; restarting starts
a new total. Durable attempt records keep their own usage, so a task's history
and its own total survive restarts even though the session total does not. The
terminal only formats this data — the header's session segment, the detail
panel's per-task line and per-1M price, and the history rows — and never derives
a cost or a rate itself.

## Open work

These are milestones, not claims about the current implementation.

1. Add explicit authorization and auditable decisions for external sources
   before introducing them. Rejected-request records and external-source
   authentication remain open.
2. Extend structured reports to configured command adapters and richer
   assignment policy. Fantasy capabilities and adapter reports exist; operator
   reports already support waiting, blocked, ready and failed attention states.
3. Extend isolated-worktree handling. Review-gated merging and worktree cleanup
   on deletion exist, and explicit discard is available for clean, integrated
   worktrees. An in-app diff and conflict resolution remain open.
4. Add external sources and model-assisted evaluation only after the
   deterministic authorization gate; evaluator output is untrusted and cannot
   authorize itself.

Tests should cover duplicate triggers, subscriber changes during retries,
partial or uncertain delivery, process crash, restart, concurrent scheduling,
save failure and shutdown. Use fake sources and runners so tests do not need
model credentials.
## Startup, flags and recovery

`go run .` accepts these flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-state` | `<user config dir>/orchestrator/state.json` | durable state file (legacy location retained) |
| `-workspace` | current directory | working directory for newly created tasks |
| `-max-concurrent` | `4` | maximum simultaneously running agents |
| `-idle-timeout` | `0` | cancel an agent that produces no output for this long; `0` disables |
| `-isolation` | `true` | allow automatic Git worktrees for mandates that request changes |
| `-agents` | none | optional trusted command-executor configuration |
| `-specialist-tools` | none | optional JSON file that sets per-specialist tool limits |
| `-model` | none | model selector for executors with no model from `profiles.json` |
| `-profiles` | `profiles.json` beside the state file | profile and specialist configuration |
| `-profile` | unset | default profile for routed work and unchosen tasks |
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
- legacy tasks without attempts get one synthesized attempt from their first
  `started` event;
- tasks that were `running` become `interrupted`, and their latest attempt gets
  a finish time and an error. No worker is assumed to have survived.

`Shutdown` cancels running work, closes pending start requests, closes the
subagent control socket and waits for workers to finish writing state. Quitting
therefore ends active tasks as `canceled`, not `interrupted`; interrupted only
comes from reopening state after a crash or kill.
