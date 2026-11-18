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

The `openai-subscription` provider implements ChatGPT subscription access behind
the same Fantasy model interface as API-key providers. The installed official
Codex CLI handles login outside the Bubble Tea update loop, with terminal
ownership handed to the child process. File-backed subscription credentials
use a dedicated Codex home under the user config directory, separate from the
user's normal Codex login and provider API keys. The provider refreshes and
atomically persists rotated tokens, and owns subscription request adaptation.
Routing, specialist tool palettes, model
selection, streaming and follow-up delivery keep the existing engine behavior.
Subscription selectors explicitly enable Fantasy's Responses API and classify
all subscription models as Responses models, including newly listed model IDs.
They never fall back to chat completions or the billed OpenAI API endpoint.
The model menu fetches the subscription backend's catalog with the signed-in
account's credentials, using the same token-refresh path as inference. Catalog
requests follow [Crush's OpenAI subscription implementation](https://github.com/charmbracelet/crush/blob/49a3b96a487efea9666ec083efcd4fb51f510015/internal/oauth/openai/models.go):
the Codex backend's models endpoint, a client version requesting the full catalog,
and only entries whose visibility is `list`. The existing ten-minute catalog
cache and explicitly labelled failure fallback apply.
Keyring-only credentials are not supported.

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
- A task's title is presentation metadata, not authority. Acceptance records
  the deterministic first-line title so the board is readable immediately, then
  one bounded, best-effort call with the configured small model (otherwise the
  task's resolved model) may replace it.
  An explicit caller title is kept, and a missing model, provider failure or
  unusable answer keeps the derived title. The mandate, decision digest,
  attempts and later context are never changed by titling.
- A related task in the same thread has its own mandate and decision and
  snapshots the default profile at acceptance. A thread follow-up records the
  subscribed queued and running recipients at submission. Pausing a subscription
  affects future inputs; resuming does not replay missed ones.
- A task follow-up adds context; a running task receives it live in the same
  conversation, and a terminal task is requeued and resumed immediately. The
  next attempt replays recent context and prior assistant output, so work
  continues instead of restarting. Neither changes the mandate. Later context
  never widens a mandate.
- A running task can delegate through the same authorization step (subagent
  source). The child gets a new thread, mandate, decision and parent link,
  inherits the execution workspace and the parent profile's tool list, and uses
  the parent executor as fallback. Depth and child limits bound delegation; a
  child's lifecycle is independent once it exists.

External sources would need authenticated identities, stable request IDs,
idempotent decisions and durable rejection records; none exist yet.

## Attempt context

A Fantasy system prompt is assembled stable-first and volatile last: tool
guidance, workflow, verification and output policy, operator and profile
instructions, the skill index, and per-attempt delegation guidance, followed by
the environment block (execution workspace, platform, date, isolated-worktree
note and a read-only Git snapshot bounded by a two-second timeout) and project
context. Project instruction files are bounded (32 KiB each, 64 KiB total) and
memoized by path, size and modification time. For an isolated task they are read
from the source checkout, not the worktree: an agent must not author the
instructions a later attempt reads. Output policy is operator-facing for a
top-level task and tersely parent-facing for a delegated child.

The live conversation sent to the provider is capped at 96 KiB. When a call
would exceed it, the oldest exchanges are dropped and a user message states that
earlier context was omitted; the original mandate and durable history are not
rewritten. A run that ends with tool activity and no closing prose is recorded
as a progress report rather than a failure, because its durable tool turns
already hold the work.

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

Skills are operator-owned instruction packs loaded at startup from the `skills`
directory beside the state file or an explicit path. Only their names and
descriptions enter the system prompt; the agent reads a body through the skill
tool. Loading a skill adds instructions, never authority: the mandate, profile
tool palette and orchestrator policies still bound the task. A missing default
directory is empty; malformed files, duplicate names and a missing explicit
directory fail startup. The loader is a leaf package so the executor can use a
catalog without importing the service.

The operator enables or disables installed skills globally or per agent with `/skills`.
The opt-out lists are durable in `skills.json` beside the state file; global
disables override agent settings. The service gives each Fantasy runner its
filtered catalog, so a disabled skill leaves the
system-prompt index and the skill tool for the next attempt. Running attempts
keep the prompt they started with. Enabling a skill restores its instructions;
neither direction changes the mandate or the profile's tool palette.

The service resolves a Fantasy attempt's model as the task profile model, else
the agent-specific custom model (`models.json`) if set, then its assigned or
default class model (`general` is big, specialists small), else the specialist
model (`profiles.json`), else `-model`. A profile or
specialist model of `big` or `small` stands for that class's model, and a
specialist alias also sets its class; an alias whose class is unset is skipped,
never sent to a provider. It fixes the selector on the attempt and the adapter
runs exactly that selector. The provider prefix selects the endpoint
and credential.
A delegated child inherits the parent's resolved model when its executor can
select one, unless a concrete selector is pinned for the child by `profiles.json`
or `models.json`. An executor that cannot select a model keeps its own defaults
and does not fail the child's start.

The compiled coordinator guidance is added only for a top-level `general`
attempt with delegation credentials, bash, and remaining child budget. It grants
no authority beyond the mandate, and the router never splits work.

Every composed attempt prompt also carries a compiled output policy: report the
outcome and findings first, keep one line per finding with its file reference,
quote only decisive code, and skip narration, repeated context and closing
summaries. Specialist role prompts state the compact report each role returns.
The policy shapes output only and grants no authority.

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
apply at that moment). Prompt tokens are uncached input plus cache reads and
writes; cache reads and writes are recorded separately from the provider's
counters and priced at their own rates, and a request-wide tier is selected from
the whole prompt. The component costs and the total are fixed at recording time,
so a later rate change cannot rewrite history. Unpriced models report tokens
without a cost. Task rows abbreviate prompt, output and cache-read tokens with
`↑`, `↓` and `⚡` glyphs and omit the executor, which routing usually sets to
`general`; task details keep both. The session total covers attempts started by
this process; the board shows each task's own total instead of a session total in
the header. Peak hours are
01:00–04:00 and 06:00–10:00 UTC on weekdays, excluding Chinese public
holidays; every other hour, including weekends and holidays, is off-peak.
While a DeepSeek model is selected, a badge beside the wordmark names peak or
off-peak in force, marked `⚠` at peak and `✓` off-peak. That badge is the only
place the window is named; the model menu and task details do not repeat it.
The window belongs to the provider, so an unpriced DeepSeek model carries the
badge without claiming a cost.

## Workspaces and integration

Tasks run in the shared source workspace unless the mandate opens with an
imperative edit action, the source has a committed HEAD and isolation is
enabled; then the task gets a detached Git worktree. This is a lexical
heuristic, not proof. An explicit task choice overrides it, and the scheduler
reconciles a persisted automatic choice with the immutable mandate. Children
inherit the parent's execution workspace. Agents keep the host user's
permissions. New worktrees refuse a source with tracked or non-ignored untracked
changes; existing reservations remain available for retry. Ignored files are not
checked and are not copied. The cleanliness check is not atomic with external
workspace edits.

After a successful isolated run the service commits pending changes and opens a
review of that exact commit. Approval persists the decision and schedules a
serialized merge; a stale decision leaves the newer review pending. A failed
merge keeps the execution successful, records the error and retains the
worktree. Git autostashes touched local edits. When reapplying them conflicts, the
merge commit is rolled back, the autostash is reapplied to the original checkout
and the merge is recorded as failed, so no success hides conflict markers.
Rejection leaves the source unchanged.

A parent with queued or running descendants on its worktree releases its slot
and records pending integration; an unsuccessful descendant blocks integration.
Deletion is durable before cleanup, and a parent can be deleted only after (or
together with) its children. Discard saves its intent first; on restart a
surviving tree is kept for retry, a removed one is finished clearing. An explicit
confirmed force discard may remove dirty or unmerged work while preserving task
history; it retains the same ownership, activity and shared-workspace checks.

Prompt composers can hand terminal ownership to `$EDITOR` using Bubble Tea's process execution; drafts travel through a private temporary file, which is retained on editor or read failure for recovery. No shell interprets the editor setting or draft.

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

Transient notices are non-blocking toasts and decisions are modal dialogs, both composited over an already-sized frame; neither is allowed to add or remove footer rows, so message traffic cannot shift the layout. The terminal derives all geometry from one layout function (header, body, optional sidebar, docked composer, status and hint rows) and renders modals over a dimmed copy of the frame. The header, task list, detail pane and subtask sidebar are borderless surfaces on a two-cell margin, mirroring Crush; rounded frames are reserved for modals, completion popups and diff cards. The task list numbers top-level rows in display order, which is also the order navigation and the narrow layout's `TASK n/N` title use. Rows group as attention, running, queued and done; done folds behind a summary while active work exists, so navigation and position counts only cover visible rows. Worktree line churn is read through the service on a throttled background command and cached per commit, never on the update loop. The request box is always docked; `router.open` means focus, not visibility, and Esc blurs it so board shortcuts stay available. The box keeps a fixed three-row draft at every terminal size and scrolls longer drafts inside it, so typing never shifts the layout. Shortcut hints appear only in the bottom footer, chosen for the active mode; panes, the output transcript and the landing page show content and status, never key listings. The transcript pending-assistant spinner uses 15 cycling characters with color cycling, like Crush. Execution attempts keep a bounded structured tool conversation (`Attempt.Turns`) so a follow-up or retry replays tool calls and results, not just a prose tail. After edits the adapter verifies detected language stacks with their formatter, diagnostics and affected tests, and feeds failures back into the conversation; `/lsp` enables or disables recognized stacks and the choice persists beside the state file. Spinner animation is a port of Crush's `ui/anim` package: deterministic pre-rendered frames cached by settings, a birth schedule, a closed blue, pink and violet gradient loop that wraps one color step at a time, plus a mirrored two-stop mode for other spinners, an ellipsis and an elapsed-time suffix, advanced by one shared 20 fps clock that runs only while visible work is live. Theme changes rebuild frames without restarting the animation. The task list and the details are separate full-screen modes, never a split pane: the list is the app's welcome view with top-level tasks only, Enter opens details, and Esc/h returns. Details route the docked input to the selected task as a follow-up and show a sidebar of every descendant when the task has subtasks. Presentation state such as focus, sidebar visibility, scroll, theme and pending file attachments never becomes task state. Attachments are read once during submission and appended to the initial request text; they are not executed.

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
