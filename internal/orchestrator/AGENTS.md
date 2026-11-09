# Orchestrator development

Read the root `AGENTS.md` and `docs/design.md` first. This package is the owner of task state and must not depend on the terminal UI.

- Treat router decisions and original task mandates as durable history. A later trigger is context and must not silently widen the mandate.
- Persist state transitions before acknowledging them. Keep rollback paths for synchronous persistence errors; report uncertain delivery honestly. Keep task and thread history consistent.
- Hold the service mutex for shared state and copy slices returned to callers. Never block on a subprocess or network call while holding the mutex.
- A task and an execution attempt have separate lifecycles. Cancellation, retries, recovery and shutdown must not leave an untracked worker.
- Bind adapter output, reports, tool activity and delivery acknowledgements to the running attempt ID. Exercise late callbacks after retry with a fake runner.
- Implement provider-specific protocol handling in its adapter. Keep process arguments and input free of shell interpolation; validate and test error paths as well as success.
- Test concurrent transitions, restarts, duplicate input, and any failure modes introduced by a change using deterministic fakes. Do not rely on a configured model for tests.
