# Routing and delivery invariants

This is a development specification, not a claim that every feature is implemented. See `design.md` for the current scope.

The deterministic local router and its unified entry point are implemented: trusted local submissions are accepted, assigned and started under policy `local-1`, with separate durable records. External sources, model-assisted evaluation and a configurable policy engine remain future work.

## Inputs and authority

An input has a stable source identity, trigger ID, thread, timestamp and payload. A retry of the same input must have one durable outcome; a reused ID with different content must fail. The trusted local UI is the only currently supported source. Before introducing an external source, define how its identity is authenticated, how policy changes affect in-flight work, and how authorization decisions are persisted. Thread membership or a related message alone must never grant authority.

For the trusted local source, the deterministic router accepts a submission and records both an assignment and an execution request; acceptance, assignment and start remain separate records. An explicit accepted request establishes an immutable mandate. A follow-up to subscribed work is contextual input and must not silently expand that mandate. A new task needs its own explicit mandate and decision. Persist the input, decision and all intended deliveries atomically before notifying executors. Delivery to an executor is separate from its acknowledgement and from the agent actually consuming the input; retain unknown outcomes across crashes rather than reporting success. Retries must not create duplicate deliveries to new subscribers that were absent from the original routing decision.

## Scheduling and execution

An explicit start request is distinct from a task that merely exists. A scheduler must enforce its configured concurrency bound even under concurrent starts and completion callbacks. It must never launch work before the relevant state is durable. Running attempts interrupted by a restart are marked interrupted; no process is assumed to have survived. A re-execution must be an explicit, auditable decision and must preserve the mandate and previous attempts. Workspace isolation is opt-in: an isolated task runs in a worktree prepared from committed HEAD, with an ownership record validated before reuse. Tasks without isolation still share their workspace, and isolation does not replace review, cleanup or recovery rules.

## Next milestones

1. Extend the current per-task pause/resume controls with richer per-ticket delivery records.
2. Add explicit authorization policies and auditable decisions before introducing external sources.
3. Add executor capabilities and adapter-generated structured reports; explicit operator reports already support waiting, blocked, ready and failed attention states.
4. Add isolated workspaces and recovery rules before running multiple tasks against the same project by default. Opt-in worktree isolation exists; default isolation, automatic cleanup and in-app review remain open.
5. Add external sources and model-assisted evaluation only after the deterministic authorization gate; evaluator output is untrusted and cannot authorize itself.

Tests should cover duplicate triggers, subscriber changes during retries, partial or uncertain delivery, process crash, restart, concurrent scheduling, save failure and shutdown. Use fake sources and runners so tests do not need model credentials.
