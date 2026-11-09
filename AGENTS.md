# Agent instructions

This repository is a small, local-first agent orchestrator. Read `docs/design.md` before changing routing, lifecycle, persistence, or executor behavior. Keep the implementation understandable and prefer the Go standard library; Bubble Tea is the intentional terminal UI dependency.

- Preserve the distinction between an incoming request, the decision to accept it, its original mandate, and an execution attempt. Later context must not silently expand a mandate.
- Treat agent output as untrusted data. Do not execute output as shell commands. Pass prompts as process arguments or input, never through a shell.
- Preserve durable history and make state changes safe under concurrent UI and worker activity. Do not claim features or safety guarantees that are not implemented.
- Keep executor-specific behavior behind a narrow interface so additional agents can be added without coupling them to the UI or router.
- Test meaningful routing, lifecycle, persistence, and failure behavior with fake executors; tests must not require credentials or a live agent.
- Run `go test ./...` and `go vet ./...` for Go changes. Update the README and design notes when user-visible behavior or architectural constraints change.
- Do not add GitHub integration or broad dependencies without a concrete need.
