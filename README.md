# Orchestrator

Orchestrator is a local terminal application for submitting work to coding agents and tracking the result. Each request has a recorded acceptance decision, an immutable mandate and a history of execution attempts. The project takes inspiration from [DeepSeek's super-agent prototype](https://github.com/deepseek-ai/dsh-superagent-prototype).

## Run

Use Go 1.24 or newer and an interactive terminal. For the default executor, install [Pi](https://github.com/badlogic/pi-mono) and configure its model credentials. From the repository root, run:

```sh
go run .
```

On the router screen, type a request and press **Enter**. The local router accepts it, selects an executor and requests execution. By default two agents can run at once. Additional start requests wait in FIFO order. Press **Esc** for the task board, **/** to return to the router, or **?** for the keyboard reference.

Use `go run . -help` for all startup flags. Common options are `-workspace` for the source directory, `-state` for the JSON state file and `-max-concurrent` for the process limit. The defaults are the current directory and `orchestrator/state.json` under the user config directory. Use a separate state file for each running instance. The state file contains prompts, recent output (up to 256 KiB per attempt), routing decisions and history in plain text.

## Task board

| Action | Keys and behavior |
| --- | --- |
| Draft and run | `n` creates a queued draft. Use `A` to choose its executor or `w` to toggle isolation before requesting a start with `r`. |
| Share context | `a` creates a related task with its own mandate and the default profile in effect when accepted. `f` sends context to subscribed queued and running tasks in the thread. `s` pauses or resumes the selected task's subscription. |
| Follow up on one task | `F` sends context to that task. For a finished task it also requeues it. Press `r` to start a fresh attempt with its mandate and context. |
| Inspect work | `Tab` switches output and history. `T` toggles thinking blocks. `t` opens tool activity, where `v` filters calls and Enter in the detail pane expands arguments and results. |
| Navigate | `h` and `l` focus tasks and details. `j` and `k` move within the focused pane. `z` expands details. |
| Report or review | `p` records a status report. On a task awaiting review, `v` approves the prepared commit and `V` records a rejection reason. |
| Cancel or retry | `c` cancels. `R` requeues a failed, interrupted or canceled task, or a completed task whose merge failed. Then `r` starts it. |
| Clean up | `x` discards a clean, integrated worktree and keeps task history. `d` deletes an inactive task and its worktree. `X` and `D` apply the corresponding bulk actions. Confirm with `y`. |

Quitting cancels active attempts and waits for their workers. After an unexpected stop, reopening the state file marks running attempts interrupted. Persisted start requests resume after startup configuration loads. Manual drafts stay idle.

## Workspaces and review

In a Git repository with a committed `HEAD`, new tasks default to a detached worktree under `workspaces/` beside the state file. The worktree starts from committed files and is reused on retries. Other workspaces run in place. Use `-isolation=false` to default to the shared workspace.

**Successful isolated runs automatically commit pending changes and merge into the source repository's current checkout.** Merges are serialized. If a merge fails, inspect the error and retained worktree. Overlapping local changes in the source may need to be committed or stashed before retrying. Execution status, merge outcome and operator review are separate records. Rejecting a commit in the UI records your decision after the merge attempt.

A parent waits to integrate while descendants use its worktree. Inspect any
unsuccessful descendant before retrying integration. Delete descendant tasks
before discarding the shared worktree. Cleanup runs in the background against
the confirmed task IDs. After an interrupted discard, reopening the state file
keeps a surviving tree or finishes clearing a removed one.

Agents run with the host user's permissions. A running task can delegate with `b` in the UI or the `orchestrator subagent` helper through its local control socket. Each child has an independent lifecycle and mandate and shares its parent's execution workspace. Defaults allow two levels of delegation and four children per parent. Use `-subagents=false` to disable delegation.

## Built-in specialists

Every launch registers six Pi specialists from `internal/orchestrator/specialists.go`.

| Specialist | Use it for |
| --- | --- |
| `debugger` | Reproduce a failure, isolate its cause, and report evidence for the implementer. |
| `explorer` | Trace a code path or locate the files and tests behind a feature. |
| `implementer` | Make a scoped change and verify it. |
| `tester` | Design and run focused regression tests. |
| `reviewer` | Inspect a proposed change for bugs and missing coverage. |
| `gitter` | Inspect Git history or carry out delegated branch, commit, and merge work. |

The router matches the opening action of the mandate. For example, “review the diff”
selects `reviewer`, “add tests for parsing” selects `tester`, and “add a parser”
selects `implementer`. Unmatched requests use `pi`. The executor appears on the
task board and in task details. Drafts allow a manual override before starting.

Edit the [role prompts](internal/orchestrator/prompts) to customize specialist
instructions. They are embedded at build time. Run `./orchestrator` from this directory to load the
[provided configuration](profiles.json). Startup reads `profiles.json` from
the current directory if present. Use `-profiles PATH` to select another file. It selects Astra for the default
profile and DeepSeek Flash for the explorer, implementer and tester. To
coordinate a task with Astra, create a draft and select `pi` with **A**.
The bundled profile includes `coordinator_instructions` asking a top-level
`pi` task to delegate repository work and summarize the results. These
instructions apply when delegation is enabled and the task can use bash.
Child tasks appear beneath their parent on the board, labeled by specialist.
Task details show the execution profile and the model requested for the latest
attempt when the adapter recorded an explicit model.
The specialist model takes precedence over the task profile model for both
direct and delegated work. Pi supplies its default model when both are omitted.
Use `pi --list-models` to see the provider/model IDs available locally.

Open the task board, create a draft with **n**, then press **A** to see their
descriptions. A delegating parent can select one by name. They use the same
Pi installation and credentials as the default executor. A command configured
with the same name in `-agents` takes precedence.

## Run a command executor without credentials

Save the following as `agents.json` and run `go run . -agents agents.json`. On the task board, create a draft with `n`, choose `echo-test` with `A`, then start it with `r`.

```json
{
  "agents": [
    {"name": "echo-test", "description": "Print the received prompt for smoke tests.", "command": "cat", "args": []}
  ]
}
```

Each executor can have an optional `description`. Parents with delegation enabled see the registered names and descriptions and can pass `--agent NAME` to the subagent helper. Omitting the flag selects from the child mandate, with the parent’s executor as fallback.

The command receives the mandate and queued context on stdin. Its stdout and stderr appear in the output view. Configure only trusted executables and supply the configuration again on later launches. Pi also supports live follow-ups, structured tool activity and reports. See the [executor contract](docs/executors.md).

## Profiles

A subagent is a child task. Like its parent, it runs with an executor and a
profile. The executor (for example, `implementer`) does the work. The profile
configures tools, model, extra instructions, automatic integration and delegation.
A child inherits its parent's profile unless a narrower one is requested.
The service snapshots the profile at acceptance and records it on each attempt.
Press `P` on a
queued task to choose one, or define profiles in `profiles.json`:

```json
{
  "profiles": [
    {"name": "inspect-only", "description": "Read-only reconnaissance",
     "tools": ["read", "grep", "find", "ls"], "no_integrate": true,
     "instructions": "Inspect and report; do not change files."},
    {"name": "implementer", "model": "provider/model-id"},
    {"name": "manual-review", "tools": ["read", "grep", "find", "ls", "bash"],
     "no_integrate": true, "no_subagents": true}
  ]
}
```

The terminal displays the built-in profile as **Executor defaults**. Its
configuration key is `implementer` for compatibility. Overriding that key
changes the default settings for every executor, including delegated runs that
inherit them. Tool names are executor-specific, so `inspect-only` above is
meaningful for Pi. An
executor that cannot honor a profile's tools or model fails the start rather than
silently ignoring them. A profile is not a sandbox: agents keep the host user's
permissions.

To limit a specialist's tools in the same file, add an entry such as
`{"name":"reviewer","tools":["read","grep","find","ls"]}` to `specialists`.
The service intersects these tools with the task profile and inherited limits.
Disjoint lists prevent execution. The existing `-specialist-tools` option
also accepts a standalone tool file. When both files configure the same
specialist's tools, the list in `-profiles` takes precedence.

## Development

The module root builds the CLI, `internal/orchestrator` owns task state and execution, and `internal/tui` contains the Bubble Tea interface. Run these checks from the module root. Tests use temporary directories, local Git repositories, Unix sockets and fake executors.

```sh
go build .
go test ./...
go vet ./...
```

See the [repository instructions](AGENTS.md), [design notes](docs/design.md), [lifecycle walkthrough](docs/status.md) and [persistence benchmark](docs/status.md#persistence-benchmark).
