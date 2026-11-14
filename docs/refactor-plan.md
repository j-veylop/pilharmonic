# Simplification plan

Status: Phase 1 done; service.go split (Phase 2) done without the TUI
interface split; Phase 3 screen stack deferred. Leaf packages extracted:
`internal/worktree`, `internal/providers`, `internal/agenttools`,
`internal/instance` and `internal/tui/editor`. The `-specialist-tools` flag is
gone; specialist tool limits live only in `profiles.json`. Phase 4 deferred
features are all still present. Baseline below is pre-cleanup.

Current size: 56 production files (13,710 lines), 54 test files (11,901
lines), 436 tests.

## Baseline

| Area | Lines |
| --- | --- |
| Production Go | 15,473 |
| Test Go | 17,043 |
| Total Go | 32,516 |
| Tests | 495 Go test functions, 646 cases with subtests |

The problem is not raw volume. It is that several files and types hold too many
jobs at once, optional features are mixed into the core, and the same behavior
is described in the README, the design notes and the help overlay.

## What is slop

Measured, not guessed:

- `internal/tui/tui.go` is one model with about fifteen nested UI states and a
  90-line key dispatcher. `Update` has cyclomatic complexity 47 and `boardKey`
  67.
- `internal/tui/detail.go:22` `detailContent` has complexity 55.
- `internal/orchestrator/service.go` is 1,142 lines and owns routing,
  scheduling, persistence, workspace setup, profiles, models, credentials and
  subagent control.
- The TUI `Service` interface has 41 methods. Tests hand-write a fake with about
  25 empty methods only to satisfy it (`internal/tui/commands_test.go:29`).
- Near-duplicates: `profilePickerKey`/`agentPickerKey`
  (`internal/tui/picker.go:27-70`), `profilePanel`/`agentPanel`
  (`internal/tui/panels.go:126-186`), `pickerState.move`/`moveMenuIndex`.
- Test-only production code: `helpLayout.plain` (`internal/tui/help.go:372`)
  exists only for layout assertions; `internal/tui/status_helpers_test.go`
  reimplements sidebar rendering for tests.
- Layout tests assert exact strings across hundreds of lines
  (`internal/tui/help_test.go` is 661 lines for a 731-line help screen).
- One dead store was removed during this audit (`tui.go` `esc` handler).
- Behavior is documented three times, with drift: `help.go:135` advertises
  `Ctrl-<-` but the editor handles `ctrl+left`.
- Missing shared helpers: 33 copies of `errors.New("service is shutting down")`
  and 32 hand-written `for i := range s.data.Tasks` lookup loops. Both want one
  small helper.
- Parallel acceptance and persistence paths: `createSubagent`
  (`subagent.go:74`) builds a task by hand instead of using
  `newAcceptedTaskLocked`; `CreateRelatedTask` (`thread.go:14`) repeats
  validation and rollback from `createTaskWithIsolation`.
- `failIneligibleLocked` and `cancelQueuedLocked` (`scheduler.go:236-268`) are
  near-identical, as are the bulk and single deletion rules in `delete.go`.
- Stale naming from a removed adapter: `piTools`, `piTranscript`,
  `maxPiPromptBytes`, `checkPiPromptSize` and "Pi" comments
  (`adapter_shared.go:15-100`, `fantasy_runner.go:18-88`). Nothing in the docs
  mentions Pi.
- No unreferenced top-level production functions were found. The cleanup is
  duplication and boundaries, not a graveyard of dead code.

## Target

Keep the domain invariants from `AGENTS.md`. Remove everything else from the
core and add it back only when it earns its place.

```
internal/orchestrator
  domain.go      Task, Attempt, Thread, Trigger, Decision, Event, statuses
  store.go       atomic JSON load/save, recovery, instance file rules
  service.go     accept, start, cancel, retry, review, snapshots
  policy.go      authorization and audit evidence
  scheduler.go   concurrency limit, FIFO, shutdown
  runner.go      Runner interfaces and registry
  fantasy*.go    one executor, tools in their own files
  worktree.go    worktree create, commit, merge, discard
internal/tui
  app.go         screen stack, shared refresh tick, notices
  board.go       task list and actions
  detail.go      transcript, history, scrolling
  request.go     docked request box and slash commands
  login.go       provider login
  help.go        static help from one table
```

Rules:

- A file should hold one job. Target under 400 lines per file and 60 per
  function. Split before adding.
- An interface exists only when a test or a second implementation uses it.
  Target under 15 methods.
- UI state lives in the screen that owns it. The app routes messages; it does
  not know every picker's fields.
- Tests assert state transitions and outcomes. At most one or two golden layout
  tests per screen, using stripped text, not ANSI cells.
- Docs: README is the user guide. `design.md` keeps only invariants and
  boundaries. Help is generated from the keymap where possible.

## Core v1

Ship first:

1. Provider login and stored credentials.
2. Submit request, authorization decision, deterministic routing, FIFO queue.
3. Task and attempt lifecycle with durable history, recovery, cancel, retry.
4. One executor: Fantasy with the existing workspace tools.
5. Source workspace, optional isolated worktree, review approval/rejection and
   merge.
6. Board, detail tabs, request box, quit confirmation, help.
7. Crash/restart behavior and the instance lock.

Defer, then add back feature by feature:

- profiles, specialist tool limits;
- the six-specialist catalog;
- model menus, model fetching, usage totals and pricing;
- subagents, control socket, live follow-up, thread subscriptions;
- configured command executors (`-agents`);
- activity panel, thinking toggle, tool-call collapsing, OSC52 selection,
  mouse, vim counts, fullscreen, bulk cleanup;
- Bedrock and other provider-specific branches.

Do not cut: authorization evidence, the request/mandate/attempt distinction,
durable writes before acknowledgement, concurrency safety, untrusted-output
handling, bounded payloads.

## Phases

### 0. Freeze and measure

Tag the current tree as the behavior reference. Record these tests as the
core contract: route/run/reload, cancel/recovery, concurrency limit, save
failure, review/merge, worktree discard. No rewrite may merge without them.

### 1. Delete obvious slop (small, independent commits)

- Inline or delete test-only production helpers; move test projections into
  tests.
- Replace hand-written interface stubs with embedded interfaces in fakes.
- Merge the duplicated picker handlers, menu movement and panels behind one
  generic helper each.
- Remove dead stores and branches found by `deadcode` and `staticcheck`.
- Add one task lookup and one closed-service helper, then delete the copies.
- Route related tasks and subagents through the same acceptance function, so
  audit evidence and rollback exist in one place.
- Merge the near-identical scheduler failures and deletion eligibility rules.
- Rename the `pi*` helpers after the adapter they serve, or inline them.
- Fix doc drift; make the keymap the source for help rows.

### 2. Extract the service

No behavior change. Move domain types and storage into `domain.go` and
`store.go`, then scheduler and runner registry, then worktree. Split the public
API into `TaskAPI`, `WorkspaceAPI`, `CredentialAPI` and `ModelAPI`, and make
the TUI depend only on the interfaces it uses. Target: `service.go` under 300
lines.

### 3. Rebuild the terminal shell

Introduce `app` with an explicit screen stack. Move one screen at a time:
board, detail, request, login, help. Delete the old handlers after each move.
Target: no function over 60 lines, no screen over 400 lines.

### 4. Add features back one at a time

Each feature returns with its own slice, tests and docs, in this order:

1. specialists and profiles;
2. usage tokens, then pricing;
3. activity and thinking display;
4. subagents and live context;
5. command executors and extra providers.

A feature is done when it has a behavior test, a help row if it has a key, and
no new global state in the app.

### 5. Collapse the docs

README becomes the only full user guide. `design.md` keeps ownership,
authorization, attempt and storage invariants, under about 150 lines. The help
overlay keeps short reference text. `executors.md` keeps only the contract.

## Guardrails

- Run `go test ./...`, `go vet ./...`, `staticcheck ./...` and `deadcode`.
- Enable `unused`, `gocyclo` and `dupl` in CI with thresholds, not as a
  one-time cleanup.
- Every change that adds a file or type must delete one or justify why not.
- Do not migrate state formats without a compatibility test.
- Keep tests deterministic and credential-free.

## Recommendation

Do not restart from an empty directory. The engine's persistence, authorization
and failure tests encode real invariants that are expensive to rediscover.
Rewrite the terminal shell and split the service in place, while deleting
deferred features. That removes most of the complexity without risking the
durable core.
