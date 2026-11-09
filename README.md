# Orchestrator

A local terminal application for submitting and tracking coding-agent work, inspired by [DeepSeek's super-agent prototype](https://github.com/deepseek-ai/dsh-superagent-prototype). The router is the main entry point: it accepts local requests deterministically, records the decision, assigns an executor and starts the work. This is an early foundation; the full upstream authorization and model-assisted routing model is not implemented. See [design notes](docs/design.md).

## Run

Install Go 1.24 or newer and the [Pi coding agent](https://github.com/badlogic/pi-mono), and configure Pi's model credentials. From this directory:

```sh
go run .
```

To build a binary instead, run `go build` and execute `./orchestrator`.

The interface opens on the **router**. Describe the work and press `Enter`: the deterministic local router accepts the request, derives a title, assigns Pi, records the decision and starts execution, queueing it when capacity is full. The same screen lists work that needs you (blocked, waiting, awaiting review, undelivered follow-ups, failed or interrupted tasks) and recent routes. Press `Esc` for the task board, `/` to return to the router, and `?` for all controls. One request becomes one task; the router does not decompose requests or evaluate them with a model.

Use `-max-concurrent N` to set the maximum number of simultaneously running agents (default: 2). When capacity is full, started tasks and routed requests wait and run automatically in request order when a slot opens. Waiting start requests persist and resume on the next launch; tasks that have not been started stay idle. Use `-workspace /path/to/project` to choose the directory in which new tasks run (default: current directory), and `-state /path/to/state.json` to choose the state file (default: your user config directory under `orchestrator/state.json`). The interface requires an interactive terminal.

On the task board, press `n` to type a manual draft and queue it instead of routing it immediately. Press `a` to create a related task in the selected task’s thread, `f` to send context to its queued and running thread peers, and `r` to start a task. Press `w` on a queued task that has not been started to toggle Git worktree isolation. Each related task has its own mandate; tasks created later do not receive old follow-ups. The original request remains the mandate; additional context is passed separately. Use the arrow keys or `j`/`k` to select a task, `c` to cancel, `r` to start a queued task, `R` to requeue a failed, interrupted, or canceled task, `Tab` to switch between output and event history, `PgUp`/`PgDn` to page through task details, `[`/`]` to scroll one line, `Home`/`End` to jump to the top or bottom, or the mouse wheel to scroll, `d` then `y` to delete an inactive task, and `q` to quit. Deletion removes its history but leaves workspace files untouched. Quitting cancels active tasks and waits for them to stop. Status and recent output appear in the detail view; Tab shows attempt and event history. Each attempt retains its output in the state file. Retrying starts a new Pi process against the same workspace (or prepared worktree) and does not undo earlier changes. Pi streams assistant text and tool start/finish activity into the output view. Running Pi tasks receive follow-ups through RPC. The history shows whether Pi accepted or rejected a follow-up, or whether delivery is unknown; acceptance does not prove that the model consumed it. Follow-ups accepted by Pi are queued until its current work finishes. A task marked completed means its process exited successfully, not that its changes have been reviewed.

Threads, triggers, router decisions, tasks, events, and captured output are stored locally in the state file. Output is limited to the most recent 256 KiB per attempt. They may contain sensitive prompts or agent output. Tasks that were running when the application stopped are marked interrupted when the state is next opened. This initial store is intended for a single application instance; do not share a state file between concurrent instances.

Pi runs with the selected workspace as its working directory and its own configured permissions. By default tasks share a workspace; concurrent agents can edit the same files. The router starts a request immediately in that shared workspace. For an isolated run, create a manual draft with `n`, press `w` before starting it, then press `r`; an isolated task runs in a dedicated Git worktree created from the repository's committed `HEAD`, and uncommitted files are not copied. The worktree is prepared under `workspaces/` beside the state file, recorded on the task, and reused by retries. It is retained after the task finishes and after the task is deleted, and it is never cleaned up automatically: inspecting, merging or discarding it is manual. Isolation is not a sandbox; the agent keeps the host's permissions. There is no GitHub integration.

## Development

```sh
go build
go test ./...
go vet ./...
```

Read [AGENTS.md](AGENTS.md) for repository conventions and [docs/design.md](docs/design.md) for the routing model and planned boundaries. [docs/status.md](docs/status.md) is a current-state walkthrough of the code, request lifecycle, invariants and known gaps. Additional executor implementations belong behind the runner interface.

The compact interface gives most of the screen to task details. Press `Enter` to switch arrow keys between task selection and detail scrolling, `z` to expand details to the full width, and `Esc` to return. Narrow terminals show details without a sidebar; arrow keys still select tasks. Press `?` to show all controls. Press `w` to toggle Git worktree isolation for the selected queued task before it is started. Press `s` to pause or resume thread context for the selected task. Press `p` to record a report, entering a kind (`progress`, `waiting`, `blocked`, `ready`, or `failed`) followed by a message, such as `blocked Need the database credentials`. Reports are manual status updates, and appear in task details.

## Additional agents

Load trusted command definitions with `go run . -agents agents.json`. Pi remains available by default. For a credentials-free smoke test, use this configuration (the `echo-test` executor simply prints its input):

```json
{
  "agents": [
    {"name": "echo-test", "command": "cat", "args": []}
  ]
}
```

The router always assigns Pi. To use another agent, press `n` for a manual draft, press `A` to choose its agent, then press `r`. Agent selection is available only before a start has been requested. A real command agent must accept a prompt through stdin and run once; configure its executable and fixed arguments in the same format. Commands run directly in the task workspace, with no shell substitution. Command agents stream stdout/stderr but do not accept live follow-ups; their capability is shown in the chooser. Provide the same configuration on later launches so persisted tasks can find their selected executor. See [executor contract](docs/executors.md).


Vim navigation: `h`/`l` focus tasks/details; `j`/`k` move in the focused pane; `gg`/`G` jump to the first/last task or top/bottom of details. Use `Ctrl-u`/`Ctrl-d` for half pages, `Ctrl-b`/`Ctrl-f` for full pages, and `Ctrl-y`/`Ctrl-e` for individual detail lines. Motions accept counts such as `5j` or `3k`. `A` opens the agent chooser (moved from `g` to reserve `gg`). Input composers keep these keys as normal text; Esc exits input or returns to task navigation.

Press `t` to open tool activity for the selected task. In the output view, tool calls appear as compact, indented rows with a status-coloured rail and glyph, visually separate from Markdown-rendered assistant prose; a completed call replaces its running row instead of repeating it. The activity view remains the place to inspect arguments, results and durations. In the focused detail pane, `j`/`k` select a call and Enter expands its arguments and result; Esc collapses it. Expanded calls retain their selection while new activity arrives. Mouse wheel and the existing page/line scrolling controls read long payloads. Each call shows its execution attempt, and large retained payloads carry a `truncated` marker. Tool summaries show commands, file paths or search queries when Pi supplies those arguments. Activity captured before this feature is not reconstructed from old start/stop output.

In the activity view (`t`), press `v` to cycle through all calls, the latest attempt, and errors only. Calls show timestamps and available duration; the detail header shows the current running tool action. Enter expands the selected call, and Esc returns to the list.

Agent output is rendered as Markdown using Glamour, including headings, lists, tables and code blocks. Long content still uses the existing scrolling controls. Press `?` for a centered shortcut-help popup; `h`/`l`, Left/Right or Tab change help topics; `j`/`k`, page keys and the wheel scroll within a topic, and `Esc` or `?` closes it. While help is open, task shortcuts are captured by the popup.

Help is grouped into horizontal Navigation, Tasks, Activity, and General tabs. Use `h`/`l`, Left/Right, or Tab/Shift-Tab to change topics. Use `j`/`k`, PgUp/PgDn or the wheel to scroll inside the current topic. Home/End selects the first/last topic.
