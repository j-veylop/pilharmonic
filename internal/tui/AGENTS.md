# Terminal interface development

Read the root `AGENTS.md` first. The TUI displays and requests operations from the orchestrator; it must not maintain authoritative task state.

- Keep controls visible and usable in both wide and narrow terminals, and preserve keyboard access to existing actions.
- Do not block Bubble Tea's update loop on long-running agent or network work. Surface errors and uncertain states accurately.
- Preserve terminal cell boundaries when rendering Unicode and ANSI styles. Exercise small and normal terminal dimensions when changing layout.
