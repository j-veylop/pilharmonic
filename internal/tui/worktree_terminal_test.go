package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
)

// stubTerminals puts fake executables on PATH, so launcher detection never
// reaches the real kitty, tmux or terminal emulators installed on the machine.
func stubTerminals(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("KITTY_LISTEN_ON", "")
	t.Setenv("TMUX", "")
	t.Setenv("TERMINAL", "")
}

// argv reports a command's arguments with the launcher path reduced to its base
// name, because detection resolves executables through PATH.
func argv(cmdArgs []string) string {
	args := append([]string{filepath.Base(cmdArgs[0])}, cmdArgs[1:]...)
	return strings.Join(args, " ")
}

func TestTerminalCommandPrefersKittyTab(t *testing.T) {
	stubTerminals(t, "kitty", "tmux")
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-test")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	dir := t.TempDir()

	cmd, label, err := terminalCommand(dir)
	if err != nil {
		t.Fatal(err)
	}
	if label != "kitty tab" {
		t.Fatalf("tmux won over a reachable kitty socket: label=%q", label)
	}
	want := "kitty @ --to unix:/tmp/kitty-test launch --type=tab --cwd=" + dir
	if got := argv(cmd.Args); got != want {
		t.Fatalf("kitty tab command = %q, want %q", got, want)
	}
}

func TestTerminalCommandUsesTmuxWindow(t *testing.T) {
	stubTerminals(t, "kitty", "tmux")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	dir := t.TempDir()

	cmd, label, err := terminalCommand(dir)
	if err != nil {
		t.Fatal(err)
	}
	if label != "tmux window" {
		t.Fatalf("label = %q, want a tmux window", label)
	}
	want := "tmux new-window -c " + dir
	if got := argv(cmd.Args); got != want {
		t.Fatalf("tmux command = %q, want %q", got, want)
	}
}

func TestTerminalCommandFallsBackToKittyWindow(t *testing.T) {
	stubTerminals(t, "kitty")
	dir := t.TempDir()

	cmd, label, err := terminalCommand(dir)
	if err != nil {
		t.Fatal(err)
	}
	if label != "kitty window" {
		t.Fatalf("label = %q, want a kitty window", label)
	}
	want := "kitty --detach --directory=" + dir
	if got := argv(cmd.Args); got != want {
		t.Fatalf("kitty window command = %q, want %q", got, want)
	}
}

func TestTerminalCommandHonorsTerminalEnv(t *testing.T) {
	stubTerminals(t, "mytterm")
	t.Setenv("TERMINAL", "mytterm --hold")
	dir := t.TempDir()

	cmd, label, err := terminalCommand(dir)
	if err != nil {
		t.Fatal(err)
	}
	if label != "mytterm" {
		t.Fatalf("label = %q, want the configured terminal", label)
	}
	if got, want := argv(cmd.Args), "mytterm --hold"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if cmd.Dir != dir {
		t.Fatalf("working directory = %q, want %q", cmd.Dir, dir)
	}
}

func TestTerminalCommandUsesSystemTerminal(t *testing.T) {
	stubTerminals(t, "x-terminal-emulator")
	dir := t.TempDir()

	cmd, label, err := terminalCommand(dir)
	if err != nil {
		t.Fatal(err)
	}
	if label != "x-terminal-emulator" {
		t.Fatalf("label = %q, want the system terminal", label)
	}
	if cmd.Dir != dir {
		t.Fatalf("working directory = %q, want %q", cmd.Dir, dir)
	}
}

func TestTerminalCommandReportsNoLauncher(t *testing.T) {
	stubTerminals(t)

	if _, _, err := terminalCommand(t.TempDir()); err == nil || !strings.Contains(err.Error(), "TERMINAL") {
		t.Fatalf("missing launchers did not name $TERMINAL: %v", err)
	}
}

func TestWorktreeDirRefusesMissingWorktree(t *testing.T) {
	if _, refusal := worktreeDir(orchestrator.Task{}); !strings.Contains(refusal, "no worktree") {
		t.Fatalf("task without a worktree did not explain itself: %q", refusal)
	}
	gone := filepath.Join(t.TempDir(), "removed")
	if dir, refusal := worktreeDir(orchestrator.Task{ExecutionWorkspace: gone}); dir != "" || !strings.Contains(refusal, "unavailable") {
		t.Fatalf("removed worktree resolved to %q with refusal %q", dir, refusal)
	}
	live := t.TempDir()
	if dir, refusal := worktreeDir(orchestrator.Task{ExecutionWorkspace: live}); dir != live || refusal != "" {
		t.Fatalf("live worktree resolved to %q with refusal %q", dir, refusal)
	}
}

// The review shortcut opens a detached terminal instead of suspending the board
// in an editor, so it returns a command and leaves the notice alone.
func TestReviewTerminalShortcutUsesCapturedReviewTask(t *testing.T) {
	stubTerminals(t, "kitty")
	m := model{reviewDiff: reviewDiffState{target: "review"}, tasks: []orchestrator.Task{
		{ID: "selected", Workspace: "/unrelated"},
		{ID: "review", Workspace: "/review", ExecutionWorkspace: t.TempDir()},
	}}
	updated, cmd := m.reviewDiffKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	if cmd == nil {
		t.Fatal("ctrl+t did not open a terminal")
	}
	if updated.notice.text != "" {
		t.Fatalf("ctrl+t refused a live worktree: %q", updated.notice.text)
	}
}

func TestBoardTerminalKeyRequiresAWorktree(t *testing.T) {
	stubTerminals(t, "kitty")
	m := model{tasks: []orchestrator.Task{{ID: "plain", Title: "plain", Status: orchestrator.Running}}}
	m.selected = 0
	updated, cmd := m.boardKey("ctrl+t")
	if cmd != nil {
		t.Fatal("ctrl+t launched a terminal for a task without a worktree")
	}
	if !strings.Contains(updated.notice.text, "no worktree") {
		t.Fatalf("refusal was not explained: %q", updated.notice.text)
	}

	m.tasks = []orchestrator.Task{{ID: "isolated", Title: "isolated", ExecutionWorkspace: t.TempDir()}}
	m.selected = 0
	updated, cmd = m.boardKey("ctrl+t")
	if cmd == nil {
		t.Fatal("ctrl+t did not open a terminal in an existing worktree")
	}
	if updated.notice.text != "" {
		t.Fatalf("ctrl+t set a notice before the launcher ran: %q", updated.notice.text)
	}
}
