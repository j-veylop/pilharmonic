# pilharmonic

pilharmonic is a local terminal application for submitting work to coding agents and tracking the result. Each request has a recorded acceptance decision, an immutable mandate and a history of execution attempts. Inspired by [DeepSeek's super-agent prototype](https://github.com/deepseek-ai/dsh-superagent-prototype).

## Run

Requires Go 1.27.3+ and an interactive terminal. The built-in Fantasy executor needs a provider API key (environment, e.g. `OPENAI_API_KEY`, or stored with `/login <provider>`) and a `provider/model` selector from `profiles.json`, `-model` or the model menu. OpenAI ChatGPT subscriptions use the `openai-subscription` provider without an API key; the official Codex CLI handles sign-in.

```sh
go run .                 # run
go build -o pilharmonic . # or go install .
```

pilharmonic opens on the task board with the **REQUEST** box always docked at the bottom (or the login menu when no credential exists); `/` or `i` focuses it and **Esc** leaves it while the draft stays visible. Type a request and press **Enter**: one compiled-in authorization step records the decision, and an accepted request becomes a task routed to an executor and started. A short display title is generated from the mandate with the small model when one is configured (otherwise the task's model); the first line is shown immediately and kept whenever generation is unavailable or an explicit title was supplied. **Ctrl-s** instead holds the accepted task queued so `o` can set it up before `r` starts it. The OUTPUT tab is a Crush-style chat: user turns carry a left rail, assistant turns are indented under a muted attempt line, and a running turn ends with the multi-character animated spinner and elapsed-time suffix. A follow-up replays the earlier answer so the next attempt continues rather than restarts. Start with `@NAME` to pick an executor (`@tester add regression tests`); otherwise `general` runs it. Typing `@` lists matches and **Tab** completes. Up to four agents run at once (`-max-concurrent`); further starts queue FIFO.

**F1** opens help everywhere; **?** opens it wherever it is not typed text. `/help` in the request box opens it too, and its **Commands** topic lists slash commands. **q** or **Ctrl-c** opens a quit dialog that counts the work in flight: **y** quits, **n**/**Esc** stays, **h**/**l** or **Tab** moves between its two buttons, and **Enter** presses the focused one. Quitting cancels active attempts.

Inputs use standard text editing keys (word delete, line kill, word movement, Home/End). **Ctrl-J** or **Alt-Enter** inserts a newline.

`go run . -help` lists all flags. Common ones: `-workspace` (default current directory), `-state` (default `<user config dir>/orchestrator/state.json`), `-skills` (default `skills` beside the state file), `-idle-timeout`, `-isolation`, `-attempt-retention` (default 8, `0` keeps all), `-theme auto|dark|light`, `-icons` (Nerd Font status glyphs), `-version`. One instance per state file (enforced by a lock). The state file stores prompts, recent output (up to 256 KiB per attempt), decisions and history in plain text.

## OpenAI subscription login

Install the official Codex CLI, then run `go run . login openai` to sign in with your ChatGPT account. For a headless machine use `go run . login openai --device-auth`. Expanded tool results that are unified diffs render as a coloured diff card in ACTIVITY.

In the terminal UI, select **openai-subscription** from `/login`, or enter `/login openai subscription`. Login temporarily releases the terminal to Codex's browser sign-in flow. Subscription eligibility and limits are managed by OpenAI.

After login, use `/model big` and `/model small` to choose **openai-subscription** and a model, or set a profile model to `openai-subscription/gpt-5-codex`. Submit requests normally; general and specialist agents keep the same Fantasy execution loop, tool limits, streaming and follow-ups. Subscription transport and authentication live in `internal/providers`, not a separate executor.

Login uses a dedicated file-backed Codex home at `<user config dir>/orchestrator/openai-subscription/`, separate from your normal Codex login, `credentials.json` and the `openai` API-key provider. The provider refreshes expired tokens and saves rotated credentials with owner-only permissions. Existing keyring-only or normal Codex credentials are not imported. To log out, run `codex -c 'cli_auth_credentials_store="file"' logout` with `CODEX_HOME` set to that dedicated directory; `/logout openai` removes only pilharmonic's stored API key. Subscription models report tokens without an API-price estimate.

## Task board

| Action | Keys |
| --- | --- |
| Create | `/` request box: **Enter** routes and starts, **Ctrl-s** holds. `o` sets up a held task (executor, profile, isolation); `r` starts it. |
| Run / cancel | `r` starts a held task, or retries and starts a failed, interrupted, canceled or merge-failed one; `c` cancels. |
| Share context | `a` related task in the thread; `m` message to subscribed tasks; `s` pause/resume subscription. |
| Follow up | In details, type in the docked box and press **Enter**; `f` works too. A finished task resumes immediately, a running one receives it live. |
| Inspect | `Tab`/`Shift-Tab` cycle the OUTPUT, INFO and ACTIVITY tabs; `t` thinking; `e` activity filter; **Enter** in focused ACTIVITY expands a call. |
| Navigate | `h`/`l` panes, `j`/`k` move (counts like `5j`), `gg`/`G`, `Ctrl-u`/`Ctrl-d`, `PgUp`/`PgDn`, **Enter** open details, `[`/`]` previous/next subtask, `!` next task needing action, `z` full width, `B` toggle the subtask sidebar, mouse wheel. |
| Copy | Drag in the details pane; release copies via OSC 52. |
| Report / review | `p` status report; `v` opens the prepared commit diff, `y` approves and merges, `n` rejects with a reason, `Esc` closes; **Ctrl-o** opens the review task workspace in `nvim`, **Ctrl-g** in `lazygit` while reviewing. |
| Clean up | `x` discard clean integrated worktree, `d` delete task; `X`/`D` bulk. Confirm with `y`. |
| Delegate | `b` create a child task from a running one. |
| Login / models | `/login`, `/model [big\|small]`, `/theme [auto\|dark\|light]`. |

Transient notices appear as toasts over the top-right corner, and confirmations that need a decision (review, cleanup, quit) appear as dialogs, so appearing and expiring messages never move the board, output or input. The board is the welcome screen: a standalone, full-width list of top-level tasks showing status, executor, workspace or review state and subtask counts at a glance. **Enter** opens a separate details screen; **Esc** or `h` returns to the list. Details show the conversation and, when the task has descendants, a **SUBTASKS** sidebar listing them, which `[` and `]` cycle through. With details focused, the docked box switches to **FOLLOW UP** and continues that task's conversation; in the main view it creates a new task. Running work uses Crush's animated spinner in both the transcript and status bar: pre-rendered scrambled glyphs with a gradient, staggered entrance, animated ellipsis and an elapsed-time suffix, driven by a shared 20 fps clock only while work is live. Frames are cached by settings and seeded deterministically, so identical settings render identically across processes. The board opens on a landing screen while it is empty; the status bar above the key hints shows the current mode and chosen models, and the palette follows the terminal's light or dark background. Mode and model pills use distinct tints, and headings, tabs, diff cards and chat rails share a violet-pink-teal brand ramp. Type `@` in the request box for a completion popup above the input, and `/attach <path>` to add up to five local files (64 KiB each) to the next request; they are appended as text at submission and shown as chips above the box. `/theme` switches the adaptive palette at runtime.

Status glyphs: `●` running, `✓` completed, `✗` failed/blocked/merge failed, `⊘` interrupted/canceled, `◷` queued/waiting (including a parent waiting for subagents), `◆` awaiting review. Children are indented under their parent and every row is numbered in grouped display order, matching the `TASK n/N` title in narrow layouts. The footer shows only actions valid for the selection. The header shows counts and, once reported, session token usage and estimated cost; task details show per-task usage and the current price.

Prices are a curated USD-per-1M snapshot for the catalog's OpenAI and DeepSeek models; other models show tokens without a cost. DeepSeek's off-peak window (16:30–00:30 UTC) is applied at recording time.

## Workspaces and review

In a Git repository with a committed `HEAD`, a task runs in a detached worktree under `workspaces/` beside the state file only when its mandate opens with an edit action (`fix`, `add`, `update`, `implement`, ...). Read-only and ambiguous requests run in the source workspace. Override per held task in the `o` setup popup, or disable with `-isolation=false`. Worktrees are reused on retry.

A successful isolated run commits its changes for review unless the profile sets `no_integrate`. `v` then `y` merges the displayed commit into the source checkout (serialized); `v` then `n` rejects and keeps the worktree; `f` then `r` reworks it within the mandate. Uncommitted source changes are autostashed and reapplied. If they conflict with the merge, the merge is rolled back and recorded as failed with the source checkout restored, so commit or stash those changes and retry. Untracked files that a merge would overwrite block it.

A parent waits to integrate while descendants use its worktree. Delete descendants before discarding a shared worktree.

Agents run with the host user's permissions. A running task can delegate with `b` or the `pilharmonic subagent` helper over a local control socket (defaults: depth 2, four children per parent; `-subagents=false` disables). A top-level `general` task is asked to answer simple work directly and split complex work into focused children; the guidance never widens the mandate.

## Specialists, models and profiles

Six bundled Fantasy specialists, with prompts in [internal/orchestrator/prompts](internal/orchestrator/prompts) (embedded at build time):

| Specialist | Use |
| --- | --- |
| `debugger` | Reproduce and isolate a failure. |
| `explorer` | Trace a code path or locate files and tests. |
| `implementer` | Make and verify a scoped change. |
| `tester` | Design and run focused regression tests. |
| `reviewer` | Inspect a change for bugs and missing coverage. |
| `gitter` | Git history, branch, commit and merge work. |

Select one with a leading `@name`, with **A** on a draft, or let a delegating parent choose. A command executor with the same name in `-agents` takes precedence.

A model is a `provider/model` selector. Prefixes: `openai`, `openai-subscription`, `deepseek`, `openrouter`, `groq`, `hyper`, `bedrock`. Keys go in an owner-only `credentials.json` beside the state file. `bedrock` splits at the first `/` (ARNs work), uses the AWS credential chain and region (default `us-east-1`) unless `/login bedrock` stores a bearer token.

`/model big|small` sets the two class models; the menu fetches provider catalogs (cached ten minutes; a labelled built-in list on failure). The subscription provider fetches the current catalog using your signed-in ChatGPT account; its built-in list is only a labelled fallback if that request fails. Custom IDs remain available. Choices persist in `models.json` as `{"big": "provider/model", "small": "provider/model"}`. `general` runs on `big` and specialists on `small`; a profile or `specialists[].model` may name `"big"` or `"small"` instead of a selector to use that class's model, and a specialist alias also moves it to that class. An alias whose class has no model is skipped. Resolution order: task profile model, class model, specialist model from `profiles.json`, `-model`.

Startup reads `profiles.json` beside the state file (never the current directory); `-profiles PATH` loads another, e.g. the [provided one](profiles.json), which sets the specialists' model (a `/model small` choice overrides it); pair it with `/model big provider/model` for `general`. Profiles set tools, model, instructions, `no_integrate` and `no_subagents`; the task snapshots its profile at acceptance:

```json
{
  "profiles": [
    {"name": "inspect-only", "tools": ["read", "grep", "find", "ls"], "no_integrate": true,
     "instructions": "Inspect and report; do not change files."},
    {"name": "implementer", "model": "provider/model-id"}
  ],
  "specialists": [{"name": "reviewer", "tools": ["read", "grep", "find", "ls"]}]
}
```

Built-in profiles `implementer` (routing default) and `coordinator` set nothing and inherit executor defaults. Entries overlay built-ins field by field; `default_profile` picks the routing default. Specialist tool limits (`specialists[].tools`) intersect with the task profile; disjoint lists prevent the start.

## Skills

Skills are local instruction packs that a Fantasy task can load on demand. Startup reads the `skills` directory beside the state file (`<user config dir>/orchestrator/skills`), or `-skills PATH`. Each skill is a subdirectory containing `SKILL.md` with YAML frontmatter and instructions:

```markdown
---
name: release
description: Build and publish a tagged release without skipping verification.
---
# Release

Run `scripts/build-release.sh <tag>` and verify the checksums.
```

The frontmatter supplies `name` (defaults to the directory name) and a one-line `description`. The system prompt lists installed skills and their descriptions; the agent loads a body with the `skill` tool, so unused instructions cost no context. A skill supplies instructions only and grants no authority beyond the task mandate or the profile's tool palette. Malformed skills, duplicate names and an explicitly configured missing directory fail startup; the default directory is optional.

Use `/skills` in the request box to enable or disable installed skills. Press Tab or left/right to switch between global settings and individual agents; Enter toggles the selected skill. A global disable overrides each agent's setting, shown as `global off`. Settings persist in `skills.json` beside the state file and apply to the next attempt; a running attempt keeps its original catalog. Skill selection applies to Fantasy agents, not external command executors.

## Command executors

For smoke tests or wrapping a program, run `go run . -agents agents.json`:

```json
{"agents": [{"name": "echo-test", "description": "Print the prompt.", "command": "cat", "args": []}]}
```

The command receives the mandate, queued context and role instructions on stdin; stdout and stderr appear as output. It is one-shot (no live follow-ups, tool activity or reports). Configure only trusted executables. See the [executor contract](docs/executors.md).

## Compatibility

State, workspaces and the control socket stay under `<user config dir>/orchestrator/`; subagent variables keep the `ORCHESTRATOR_` prefix; digests keep `orchestrator.authorization/v1`; merges fall back to `Orchestrator <orchestrator@localhost>`. No migration is needed.

## Releases

Tagged builds (`v1.2.3`, optional suffix) publish one self-contained binary per platform (`pilharmonic_<tag>_<os>_<arch>`, Linux/macOS amd64+arm64, Windows amd64) plus `sha256sums.txt`. `scripts/build-release.sh <tag>` builds them locally into `dist/`.

```sh
git tag -a v0.1.0 -m "pilharmonic v0.1.0" && git push origin v0.1.0
grep " ${name}\$" sha256sums.txt | sha256sum -c -   # verify a download
```

## Development

`internal/orchestrator` owns task state and execution; `internal/tui` is the Bubble Tea interface. The leaf packages `internal/worktree` (Git worktrees), `internal/providers` (model providers), `internal/agenttools` (workspace tools), `internal/skills` (skill loader) and `internal/instance` (state-file lock, atomic writes) do not depend on the engine. Tests use temporary directories, local Git repositories, Unix sockets and fake executors.

```sh
go test ./... && go vet ./...
```

See [AGENTS.md](AGENTS.md) and the [design notes](docs/design.md).
