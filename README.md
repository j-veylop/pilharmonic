# pilharmonic

pilharmonic is a local terminal application for submitting work to coding agents and tracking the result. Each request has a recorded acceptance decision, an immutable mandate and a history of execution attempts. The project takes inspiration from [DeepSeek's super-agent prototype](https://github.com/deepseek-ai/dsh-superagent-prototype).

## Run

Use Go 1.27.3 or newer and an interactive terminal. The built-in executor drives models in-process through [Fantasy](https://charm.land/fantasy), so it needs a provider API key — either in the environment (for example `OPENAI_API_KEY` or `DEEPSEEK_API_KEY`) or stored with the `/login <provider>` command in the UI — and a `provider/model` selector in `profiles.json` or through `-model`. From the repository root, run:

```sh
go run .
```

To build or install the CLI from the repository root:

```sh
go build -o pilharmonic .
go install .
```

pilharmonic opens on the **NEW REQUEST** form; a first launch with no provider credential opens the login menu until a key is stored. Type a request and press **Enter**. The local policy checks the request; when accepted it becomes a task, selects an executor and requests execution. Start the request with `@NAME` to choose a registered executor, for example `@tester add regression tests`; without an explicit mention it uses `general`. Typing `@` shows the matching executors and **Tab** completes the first match. The mention is routing metadata and is not part of the recorded mandate. By default four agents can run at once. Additional start requests wait in FIFO order. The form also lists **NEEDS ACTION** and **RECENT TASKS**, up to eight rows each. **Tab** moves from the draft to a list, skipping an empty one; `j`/`k` select a row, the focused list scrolls to keep that row visible, and **Enter** opens the task on the board. Further presses of **Tab** (or **Shift-Tab**) cycle back to the draft. The draft is kept while the lists have focus. Press **Esc** for the task board, **/** to open a new request, or **?** for help; help starts with **Concepts**.

On the task board, the **! action** hint jumps to the next task needing action: blocked, waiting, awaiting review, input undelivered, merge failed, failed or interrupted. **n** creates a task directly as a draft without routing. Choose an executor with **A**, then start it with **r**.

The request form and every composer share one input box docked at the bottom of the screen: its label sits on the top border, the draft wraps and grows inside it, and the key hints stay in the footer below. Editing follows the shortcuts of a standard text input. **Ctrl-W** or **Alt-Backspace** deletes the previous word, **Alt-D** the next one, **Ctrl-K** and **Ctrl-U** delete to the end and the start of the line, **Delete** or **Ctrl-D** removes the next character, **Ctrl-Left** and **Ctrl-Right** (or **Alt-B** and **Alt-F**) move by word, and **Home**/**End** or **Ctrl-A**/**Ctrl-E** jump to the line bounds. Word deletion stays on the line you are editing. Press **Ctrl-J** (or **Alt-Enter**) to insert a newline, then **Enter** to submit. Use **PgUp** and **PgDn** to move through long input.

Use `go run . -help` for all startup flags. Common options are `-workspace` for the source directory, `-state` for the JSON state file, `-max-concurrent` for the process limit (four by default), `-idle-timeout` to cancel an agent that produces no output and `-attempt-retention` to bound how many attempt transcripts a task keeps in full (`8` by default, `0` keeps every attempt). The defaults are the current directory and `orchestrator/state.json` under the user config directory. That state location is retained for compatibility with existing installs. Each state file supports one running instance: the CLI holds an instance lock and refuses to open a state file another instance is already using. The state file contains prompts, recent output (up to 256 KiB per attempt), routing decisions and history in plain text. `-version` prints the build version.

## Install a release

Tagged builds are published as GitHub releases. Each release carries one self-contained binary per supported platform — Linux and macOS on `amd64` and `arm64`, and Windows on `amd64` — named `pilharmonic_<tag>_<os>_<arch>`, with `.exe` appended on Windows, plus `sha256sums.txt`. The binary embeds the specialist prompts and needs no other file from this repository, so a download is enough to run it. Download the binary and the checksum file for your platform from the repository's **Releases** page, then:

```sh
tag=v0.1.0
os=linux   # darwin for macOS, windows for Windows
arch=amd64 # or arm64 on Linux and macOS
name="pilharmonic_${tag}_${os}_${arch}"
if [ "$os" = windows ]; then name="$name.exe"; fi
grep " ${name}\$" sha256sums.txt | sha256sum -c -   # shasum -a 256 -c - on macOS
chmod +x "$name"
./"$name" -version
```

On Windows, `Get-FileHash -Algorithm SHA256 <file>` prints the same digest as the matching line of `sha256sums.txt`, and `-version` prints the tag and the full commit the binary was built from. A released binary still starts in the current directory, reads `profiles.json` beside its state file and needs the provider API key described above; the release only adds a build you do not have to compile.

## Compatibility with existing installs

The public project name is pilharmonic. These identifiers remain unchanged so durable state, running agents and recorded decisions stay compatible:

- the engine package remains `internal/orchestrator`, now imported as `pilharmonic/internal/orchestrator`;
- the default state, workspace and control files remain under `<user config dir>/orchestrator/`;
- subagent environment variables keep the `ORCHESTRATOR_` prefix: `ORCHESTRATOR_CONTROL_SOCKET`, `ORCHESTRATOR_TASK_ID`, `ORCHESTRATOR_SUBAGENT_TOKEN` and `ORCHESTRATOR_SUBAGENT_CLI`;
- authorization digests keep `orchestrator.authorization/v1`;
- approved merges keep the fallback Git identity `Orchestrator <orchestrator@localhost>`.

No migration is required for the rename.

## Task board

| Action | Keys and behavior |
| --- | --- |
| Create a task | `n` creates a queued task directly without routing. Use `A` to choose its executor or `w` to toggle isolation before requesting a start with `r`. |
| Share context | `a` creates a related task with its own mandate and the default profile in effect when accepted. `f` sends context to subscribed queued and running tasks in the thread. `s` pauses or resumes the selected task's subscription. |
| Follow up on one task | `F` sends context to that task. For a finished task it also requeues it. Press `r` to start a fresh attempt with its mandate and context. |
| Inspect work | `Tab` switches output and history. `T` toggles thinking blocks. `t` opens tool activity, where `f` filters calls and Enter in the detail pane expands arguments and results. |
| Navigate | `h` and `l` focus tasks and details. `j` and `k` move within the focused pane. `!` on the task board selects the next task needing action in grouped display order, wrapping at the end. `z` toggles full-width details, and `[` / `]` scroll one detail line. The mouse wheel scrolls details. |
| Copy output | Drag inside the details pane to select text. Releasing the mouse copies the selection through the terminal's OSC 52 clipboard support. Selection cannot start in or extend into the sidebar, header, footer or input box. Terminals that block OSC 52 ignore the copy. Use the terminal's normal shortcut to paste into composers. |
| Report or review | `p` records a status report. On a task awaiting review, `v` approves and merges the prepared commit; `V` rejects it with a reason. Use `F` to add follow-up context and requeue rejected work. |
| Cancel or retry | `c` cancels. `R` requeues a failed, interrupted or canceled task, or a completed task whose merge failed. Then `r` starts it. |
| Clean up | `x` discards a clean, integrated worktree and keeps task history. `d` deletes an inactive task and its worktree. `X` and `D` apply the corresponding bulk actions. Confirm with `y`. |

Tool activity in the output view and in the `t` panel carries its status as a glyph alone: `⠋` running, `✓` finished, `✗` failed and `⊘` interrupted. Each row keeps the tool name in front of its summary, so a failed call shows the tool and its failure message together instead of the message on its own; a failure that names a tool with no start marker keeps its own row rather than taking over a call that is still running.

Task-list rows show status first as a coloured glyph: `●` running, `✓` completed, `✗` failed, blocked or merge failed, `⊘` interrupted or canceled, `◷` queued, scheduled, waiting or waiting for subagents, and `◆` awaiting review. Running rows use a blue accent. A running parent with a queued or running child, or a child still integrating its worktree, is shown as `◷` waiting for subagents rather than `●` running. Child tasks are grouped directly beneath their parent and delegated children are indented by depth so the status glyph itself shows the hierarchy without an extra arrow. The executor and workspace badge follow, so the status stays visible when a narrow sidebar truncates the row. Task details still show the status word.

The header summarises the task count, tasks displayed as running, queued process state and the number of tasks that need action. Needs action covers blocked, waiting, awaiting review, input undelivered, merge failed, failed and interrupted tasks. A running parent waiting for subagents is not counted as running. The board footer offers only actions valid for the selected task and current mode, such as `v`/`V` for a pending review, `c`/`t` for a running task, `r` for an unscheduled draft and `R` for a retryable task. It also keeps help, request, new-task and quit hints as width permits. When the sidebar is hidden, the detail title starts with the selected task's grouped position, such as `TASK 2/5`.

Quitting cancels active attempts and waits for their workers. After an unexpected stop, reopening the state file marks running attempts interrupted. Persisted start requests resume after startup configuration loads. Tasks created with **n** stay idle until started.

## Workspaces and review

In a Git repository with a committed `HEAD`, a task runs in a detached worktree under `workspaces/` beside the state file only when its mandate explicitly requests a change, such as `fix`, `add`, `update` or `implement`. Read-only inspection requests (for example `review`, `explain`, `inspect`, `read` or `test`) and ambiguous requests with no detected edit action stay in the source workspace. The choice is a lexical request-opening heuristic; the selected executor does not create a worktree by itself. Use `w` on a task before it starts to override the automatic choice, or `-isolation=false` to keep every task in the source workspace. The worktree starts from committed files and is reused on retries. Other workspaces run in place.

**Successful isolated runs commit pending changes for review unless the task's profile sets `no_integrate`; merging stays blocked until you accept the review.** A profile that withholds integration skips the commit, keeps the worktree and records `integration_skipped`. Press `v` to approve the displayed commit and automatically merge it into the source repository's current checkout. Merges are serialized. Press `V` to reject it without merging; the worktree remains available. Use `F` to add context or describe remaining work within the original mandate, then `r` to start the requeued task. If an approved merge fails, inspect the error and retained worktree. Uncommitted changes in the source do not block a merge: Git stashes the files the merge touches and reapplies them afterwards, and when they cannot be reapplied cleanly the task records a merge warning and keeps them in an autostash entry you can apply or drop yourself. Untracked source files that the merge would overwrite still block it until you move or commit them. Execution status, merge outcome, merge warning and operator review remain separate records.

A parent waits to integrate while descendants use its worktree. Inspect any
unsuccessful descendant before retrying integration. Delete descendant tasks
before discarding the shared worktree. Cleanup runs in the background against
the confirmed task IDs. After an interrupted discard, reopening the state file
keeps a surviving tree or finishes clearing a removed one.

Agents run with the host user's permissions. A running task can delegate with `b` in the UI or the `pilharmonic subagent` helper through its local control socket. Each child has an independent lifecycle and mandate and shares its parent's execution workspace. Defaults allow two levels of delegation and four children per parent. Use `-subagents=false` to disable delegation. A top-level `general` task with delegation is asked to split complex or multi-concern work into focused children using the child budget remaining after earlier attempts, while answering simple tasks directly. An exhausted child budget receives no delegation credentials or coordinator guidance. Independent scopes may run in parallel; dependent or shared-file work should run sequentially. Request handling never splits work automatically, and the guidance does not widen the mandate.

## Built-in specialists

Every launch registers six Fantasy specialists from `internal/orchestrator/specialists.go`.

| Specialist | Use it for |
| --- | --- |
| `debugger` | Reproduce a failure, isolate its cause, and report evidence for the implementer. |
| `explorer` | Trace a code path or locate the files and tests behind a feature. |
| `implementer` | Make a scoped change and verify it. |
| `tester` | Design and run focused regression tests. |
| `reviewer` | Inspect a proposed change for bugs and missing coverage. |
| `gitter` | Inspect Git history or carry out delegated branch, commit, and merge work. |

A request selects an executor only through a leading `@NAME` mention. For
example, `@reviewer review the diff` selects `reviewer` and `@tester add tests
for parsing` selects `tester`. Requests without a mention use `general`,
including change requests such as “add a parser”; top-level routing never
infers a specialist from the opening action. Select `implementer` with
`@implementer`, with **A** on a draft, or let delegated work choose it
automatically or by name. The mention must be the first token, uses the exact
registered name and is removed from the mandate. An unknown or incomplete
mention is rejected instead of falling back, and the request form keeps the draft so
it can be corrected. Request hints list matching names
and **Tab** completes the first one. Draft and related-task composers accept the
same leading mention. The executor appears on the task board and in task
details.

Edit the [role prompts](internal/orchestrator/prompts) to customize specialist
instructions. They are embedded at build time. Startup reads `profiles.json`
from beside the state file (the user config directory) if present, never from
the current directory, so pass `-profiles PATH` to load a file elsewhere, for
example `-profiles profiles.json` for the
[provided configuration](profiles.json) in this repository. The provided file selects the built-in
`coordinator` profile as the routing default, applies `openai/gpt-6.1-sol` to
it and sets DeepSeek Flash for the explorer, implementer and tester. To
coordinate a task with that profile, create a draft and select `general` with **A**.
The coordinator profile is built in, and its delegation guidance is compiled
in. A top-level `general` attempt with delegation is asked to answer simple
tasks directly; for complex or multi-concern work, to delegate multiple
focused, self-contained subtasks when the child budget allows; to run
independent scopes in parallel and dependent or shared-file work sequentially;
and to verify child evidence. The guidance grants no authority beyond the
mandate, and request handling does not split work automatically. A profile can
add `coordinator_instructions` for extra trusted guidance, but the shipped
`profiles.json` does not duplicate the compiled text.
Child tasks appear beneath their parent on the board, labeled by specialist.
Task details show the execution profile and the model requested for the latest
attempt when the adapter recorded an explicit model.
The specialist model takes precedence over the task profile model for both
direct and delegated work. `-model` fills in for any executor that has no
specialist model and takes precedence over the task profile model as well, and
an attempt with no model at all fails with an explanatory error.
A model is a `provider/model` selector such as `openai/gpt-6.1-sol` or
`deepseek/deepseek-flash`; the provider prefix selects the endpoint and where
its API key is read from. Built-in prefixes: `openai`, `deepseek`, `openrouter`,
`groq`, and `hyper` (Charm's model router, e.g. `hyper/prism`). Keys are stored
with `/login <provider>` (or the matching environment variable) and are kept in
a owner-only `credentials.json` beside the state file, never in task state.

Agents run on one of two model classes, `big` (planning and heavy changes) or
`small` (fast, focused work). Open the menu with **m** or `/model [big|small]`
to pick a `provider/model` selector for each class from a curated catalog, or
type a free-form selector. **/agents** toggles each agent between the two
classes. By default only `general` runs big; `debugger`, `explorer`,
`implementer`, `gitter`, `reviewer`, and `tester` run small. The choices are
kept in `models.json` beside the state file, with shape
`{"big": "provider/model", "small": "provider/model", "agents": {"implementer": "big"}}`.
A class model overrides the profile and `-model` fallbacks; a class left empty
uses them.

Open the task board, create a draft with **n**, then press **A** to see their
descriptions. A delegating parent can select one by name. They use the same
in-process Fantasy adapter, provider credentials and model fallback as the
default executor. A command configured with the same name in `-agents` takes
precedence.

## Run a command executor without credentials

Configured command executors are a separate opt-in that runs alongside the
built-in Fantasy adapter; they are useful for smoke tests and for wrapping an
existing program. Save the following as `agents.json` and run `go run . -agents agents.json`. On the task board, create a draft with `n`, choose `echo-test` with `A`, then start it with `r`.

```json
{
  "agents": [
    {"name": "echo-test", "description": "Print the received prompt for smoke tests.", "command": "cat", "args": []}
  ]
}
```

Each executor can have an optional `description`. Prefix a request with `@echo-test` to select a configured command executor. Parents with delegation enabled see the registered names and descriptions and can pass `--agent NAME` to the subagent helper. Omitting the flag selects from the child mandate, with the parent’s executor as fallback.

The command receives the mandate and queued context on stdin. Its stdout and stderr appear in the output view. Configure only trusted executables and supply the configuration again on later launches. The built-in Fantasy adapter supports live follow-ups, structured tool activity and reports; a configured command executor is one-shot and offers none of them. See the [executor contract](docs/executors.md).

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

Built-in profiles are compiled in. `implementer` is the routing default when no
profiles file selects another profile, and its name is retained as the
compatibility key for executor defaults. `coordinator` is also built in for
top-level coordination; the provided `profiles.json` selects it as the default
and supplies its model. Both built-in definitions set no tools, model or
instructions, so they inherit the executor defaults and the compiled
coordinator guidance. The terminal displays a built-in profile with no
overrides as **Executor defaults**.

`profiles.json` modifies a built-in profile field by field or adds a new one.
Omitted fields keep their built-in values; an explicitly empty value clears one.
New names start empty. `default_profile` selects the routing default. Overriding
the built-in profile changes the default settings for every executor, including
delegated runs that inherit them. Tool names are executor-specific, so
`inspect-only` above is meaningful for the built-in Fantasy adapter. An executor that cannot honor a
profile's tools or model fails the start rather than silently ignoring them. A
profile is not a sandbox: agents keep the host user's permissions.

To limit a specialist's tools in the same file, add an entry such as
`{"name":"reviewer","tools":["read","grep","find","ls"]}` to `specialists`.
The service intersects these tools with the task profile and inherited limits.
Disjoint lists prevent execution. The existing `-specialist-tools` option
also accepts a standalone tool file. When both files configure the same
specialist's tools, the list in `-profiles` takes precedence.

## Releases

Releases are cut from tags. `scripts/build-release.sh <tag>` builds the same artifacts locally into `dist/`. The `Release` workflow runs it for any pushed `v*` tag, verifies the checksums and that the host binary reports the tag, and publishes the binaries and checksums as a GitHub release. Re-running the workflow replaces the assets of the release it published before:

```sh
git tag -a v0.1.0 -m "pilharmonic v0.1.0"
git push origin v0.1.0
```

Tags must look like `v1.2.3`; a pre-release or build suffix such as `v1.2.3-rc1` is accepted. The workflow fails instead of publishing when the tag is malformed, when it does not point at the pushed commit, or when `go test ./...` fails. Continuous integration runs the same build script on every push, so a target that stops cross-compiling fails before a tag exists. Add a platform by listing its `GOOS/GOARCH` pair in the script.

## Development

The module root builds the pilharmonic CLI, `internal/orchestrator` owns task state and execution, and `internal/tui` contains the Bubble Tea interface. Run these checks from the module root. Tests use temporary directories, local Git repositories, Unix sockets and fake executors.

```sh
go build .
go test ./...
go vet ./...
```

See the [repository instructions](AGENTS.md), [design notes](docs/design.md), [lifecycle walkthrough](docs/status.md) and [persistence benchmark](docs/status.md#persistence-benchmark).
