package tui

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
)

// terminals tried, in order, when neither kitty nor tmux can host the shell.
var fallbackTerminals = []string{
	"x-terminal-emulator",
	"alacritty",
	"foot",
	"gnome-terminal",
	"konsole",
	"xfce4-terminal",
}

// worktreeDir resolves the live worktree a review action should open and, when
// there is none, the notice explaining why.
func worktreeDir(task orchestrator.Task) (dir, refusal string) {
	if task.ExecutionWorkspace == "" {
		return "", "This task has no worktree; it runs in the source workspace."
	}
	if info, err := os.Stat(task.ExecutionWorkspace); err != nil || !info.IsDir() {
		return "", "Task worktree is unavailable: " + task.ExecutionWorkspace
	}
	return task.ExecutionWorkspace, ""
}

// terminalCommand chooses how to open an interactive shell in dir. A kitty tab
// in the window the board already runs in comes first, then a tmux window, then
// a detached kitty OS window, then whatever terminal the system offers. Nothing
// is started here, so the choice is testable without a terminal.
func terminalCommand(dir string) (*exec.Cmd, string, error) {
	kitty, kittyErr := exec.LookPath("kitty")
	if listen := os.Getenv("KITTY_LISTEN_ON"); listen != "" && kittyErr == nil {
		return exec.Command(kitty, "@", "--to", listen, "launch", "--type=tab", "--cwd="+dir), "kitty tab", nil
	}
	if os.Getenv("TMUX") != "" {
		if tmux, err := exec.LookPath("tmux"); err == nil {
			return exec.Command(tmux, "new-window", "-c", dir), "tmux window", nil
		}
	}
	if kittyErr == nil {
		return exec.Command(kitty, "--detach", "--directory="+dir), "kitty window", nil
	}
	if fields := strings.Fields(os.Getenv("TERMINAL")); len(fields) > 0 {
		if name, err := exec.LookPath(fields[0]); err == nil {
			return terminalInDir(name, fields[1:], dir), fields[0], nil
		}
	}
	for _, name := range fallbackTerminals {
		if path, err := exec.LookPath(name); err == nil {
			return terminalInDir(path, nil, dir), name, nil
		}
	}
	return nil, "", errors.New("no terminal found: set $TERMINAL, or run pilharmonic inside kitty or tmux")
}

// terminalInDir starts a terminal that inherits dir as its working directory.
// Terminals that ignore the inherited directory still open a usable shell.
func terminalInDir(path string, args []string, dir string) *exec.Cmd {
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	return cmd
}

// launchDetached starts a launcher in its own session with no stdio, so it
// outlives the board and cannot corrupt the synchronized terminal output. The
// child is reaped in the background to keep the launcher itself a real process
// rather than a zombie for the life of the TUI.
func launchDetached(cmd *exec.Cmd) error {
	detach(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// launchTerminalCmd opens a terminal at dir without suspending the board.
func launchTerminalCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		cmd, label, err := terminalCommand(dir)
		if err != nil {
			return actionResultMsg{err: err}
		}
		if err = launchDetached(cmd); err != nil {
			return actionResultMsg{err: errors.New(label + " failed to start: " + err.Error())}
		}
		return actionResultMsg{notice: "Terminal opened at " + dir + " (" + label + ")."}
	}
}

// worktreeTerminal opens a terminal in the task's worktree, refusing with a
// notice when the worktree is gone or the task never had one.
func (m model) worktreeTerminal(task orchestrator.Task) (model, tea.Cmd) {
	dir, refusal := worktreeDir(task)
	if refusal != "" {
		m.notice.text = refusal
		return m, nil
	}
	return m, launchTerminalCmd(dir)
}
