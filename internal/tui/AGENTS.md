# Terminal interface development

Read the root `AGENTS.md` first. The TUI displays and requests operations from the orchestrator; it must not maintain authoritative task state.

- Keep controls visible and usable in both wide and narrow terminals, and preserve keyboard access to existing actions.
- Do not block Bubble Tea's update loop on long-running agent or network work. Surface errors and uncertain states accurately.
- Preserve terminal cell boundaries when rendering Unicode and ANSI styles. Exercise small and normal terminal dimensions when changing layout.
- Capture the target task ID when opening a composer or confirmation. For review decisions, capture the displayed commit too. The task list and pending review can change before the operator submits.
- Check new shortcuts against the active mode. Router and task composers accept printable shortcuts as text. Keep the relevant footer and help topic in sync.
