# pilharmonic

pilharmonic is a local terminal application for submitting work to coding agents and tracking the result. Each request has a recorded acceptance decision, an immutable mandate and a history of execution attempts. Inspired by [DeepSeek's super-agent prototype](https://github.com/deepseek-ai/dsh-superagent-prototype).

## Run

Requires Go 1.27.3+ and an interactive terminal. The built-in executor drives models in-process through [Fantasy](https://charm.land/fantasy), so it needs a provider API key (environment, e.g. `OPENAI_API_KEY`, or stored with `/login <provider>`) and a `provider/model` selector from `profiles.json`, `-model` or the model menu.

```sh
go run .                 # run
go build -o pilharmonic . # or go install .
```

pilharmonic opens on the **NEW REQUEST** form (or the login menu when no credential exists). Type a request and press **Enter**: one compiled-in authorization step records the decision, and an accepted request becomes a task routed to an executor. Start with `@NAME` to pick an executor (`@tester add regression tests`); otherwise `general` runs it. Typing `@` lists matches and **Tab** completes. Up to four agents run at once (`-max-concurrent`); further starts queue FIFO.

**F1** opens help everywhere; **?** opens it wherever it is not typed text. `/help` in the request form opens it too, and its **Commands** topic lists slash commands. **q** or **Ctrl-c** asks for confirmation before quitting; quitting cancels active attempts.

Inputs use standard text editing keys (word delete, line kill, word movement, Home/End). **Ctrl-J** or **Alt-Enter** inserts a newline.

`go run . -help` lists all flags. Common ones: `-workspace` (default current directory), `-state` (default `<user config dir>/orchestrator/state.json`), `-idle-timeout`, `-isolation`, `-attempt-retention` (default 8, `0` keeps all), `-version`. One instance per state file (enforced by a lock). The state file stores prompts, recent output (up to 256 KiB per attempt), decisions and history in plain text.

## Task board

| Action | Keys |
| --- | --- |
| Create | `n` queued draft without routing; `A` choose executor, `P` profile, `w` toggle isolation, `r` start. |
| Share context | `a` related task in the thread; `f` context to subscribed tasks; `s` pause/resume subscription. |
| Follow up | `F` context to one task (requeues a finished one); then `r`. |
| Inspect | `Tab` output/history, `T` thinking, `t` tool activity (`f` filter, Enter expands). |
| Navigate | `h`/`l` panes, `j`/`k` move, `!` next task needing action, `z` full width, `[`/`]` scroll, mouse wheel. |
| Copy | Drag in the details pane; release copies via OSC 52. |
| Report / review | `p` status report; `v` approve and merge, `V` reject with reason. |
| Cancel / retry | `c` cancel; `R` requeue a failed, interrupted, canceled or merge-failed task. |
| Clean up | `x` discard clean integrated worktree, `d` delete task; `X`/`D` bulk. Confirm with `y`. |
| Delegate | `b` create a child task from a running one. |
| Models | `m` or `/model [big\|small]`; `/agents` toggles agent classes. |

Status glyphs: `●` running, `✓` completed, `✗` failed/blocked/merge failed, `⊘` interrupted/canceled, `◷` queued/waiting (including a parent waiting for subagents), `◆` awaiting review. Children are indented under their parent. The footer shows only actions valid for the selection. The header shows counts and, once reported, session token usage and estimated cost; task details show per-task usage and the current price.

Prices are a curated USD-per-1M snapshot for the catalog's OpenAI and DeepSeek models; other models show tokens without a cost. DeepSeek's off-peak window (16:30–00:30 UTC) is applied at recording time.

## Workspaces and review

In a Git repository with a committed `HEAD`, a task runs in a detached worktree under `workspaces/` beside the state file only when its mandate opens with an edit action (`fix`, `add`, `update`, `implement`, ...). Read-only and ambiguous requests run in the source workspace. Override per task with `w`, or disable with `-isolation=false`. Worktrees are reused on retry.

A successful isolated run commits its changes for review unless the profile sets `no_integrate`. `v` merges the displayed commit into the source checkout (serialized); `V` rejects and keeps the worktree; `F` then `r` reworks it within the mandate. Uncommitted source changes are autostashed and reapplied; if that fails, the task records a merge warning and the stash remains. Untracked files that a merge would overwrite block it.

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

A model is a `provider/model` selector. Prefixes: `openai`, `deepseek`, `openrouter`, `groq`, `hyper`, `bedrock`. Keys go in an owner-only `credentials.json` beside the state file. `bedrock` splits at the first `/` (ARNs work), uses the AWS credential chain and region (default `us-east-1`) unless `/login bedrock` stores a bearer token.

Agents belong to a model class, `big` (default for `general`) or `small` (specialists). The model menu fetches each provider's catalog (cached ten minutes; a labelled built-in list on failure). Choices persist in `models.json`:
`{"big": "provider/model", "small": "provider/model", "agents": {"implementer": "big"}}`. Resolution order: class model, specialist model from `profiles.json`, `-model`, task profile model.

Startup reads `profiles.json` beside the state file (never the current directory); `-profiles PATH` loads another, e.g. the [provided one](profiles.json). Profiles set tools, model, instructions, `no_integrate` and `no_subagents`; the task snapshots its profile at acceptance:

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

Built-in profiles `implementer` (routing default) and `coordinator` set nothing and inherit executor defaults. Entries overlay built-ins field by field; `default_profile` picks the routing default. Specialist tool limits (also accepted standalone via `-specialist-tools`) intersect with the task profile; disjoint lists prevent the start.

## Command executors

For smoke tests or wrapping a program, run `go run . -agents agents.json`:

```json
{"agents": [{"name": "echo-test", "description": "Print the prompt.", "command": "cat", "args": []}]}
```

The command receives the mandate and queued context on stdin; stdout and stderr appear as output. It is one-shot (no live follow-ups, tool activity or reports). Configure only trusted executables. See the [executor contract](docs/executors.md).

## Compatibility

State, workspaces and the control socket stay under `<user config dir>/orchestrator/`; subagent variables keep the `ORCHESTRATOR_` prefix; digests keep `orchestrator.authorization/v1`; merges fall back to `Orchestrator <orchestrator@localhost>`. No migration is needed.

## Releases

Tagged builds (`v1.2.3`, optional suffix) publish one self-contained binary per platform (`pilharmonic_<tag>_<os>_<arch>`, Linux/macOS amd64+arm64, Windows amd64) plus `sha256sums.txt`. `scripts/build-release.sh <tag>` builds them locally into `dist/`.

```sh
git tag -a v0.1.0 -m "pilharmonic v0.1.0" && git push origin v0.1.0
grep " ${name}\$" sha256sums.txt | sha256sum -c -   # verify a download
```

## Development

`internal/orchestrator` owns task state and execution; `internal/tui` is the Bubble Tea interface. Tests use temporary directories, local Git repositories, Unix sockets and fake executors.

```sh
go test ./... && go vet ./...
```

See [AGENTS.md](AGENTS.md) and the [design notes](docs/design.md).
