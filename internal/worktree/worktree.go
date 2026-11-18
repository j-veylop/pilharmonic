// Package worktree prepares, integrates and removes the detached Git
// worktrees isolated tasks run in. Each task owns one reservation beside the
// state file, with an ownership record every destructive operation checks
// again. Git runs with explicit arguments, never through a shell.
package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/j-veylop/pilharmonic/internal/instance"
)

// Spec identifies one task's worktree.
type Spec struct {
	StatePath string // reservations live in a workspaces directory beside it
	TaskID    string
	Workspace string // the source workspace the worktree is prepared from
	Execution string // the recorded execution directory inside the worktree
	Title     string // used in commit subjects
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// A fixed locale keeps diagnostics matchable by describeMergeFailure; no
	// terminal prompt keeps a missing credential from blocking a worker.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		command := "git"
		if len(args) > 0 {
			command += " " + args[0]
		}
		return "", fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// gitPath resolves a path reported by git rev-parse, which may go through a
// symlink, so it can be compared with a reservation.
func gitPath(ctx context.Context, dir string, option string) (string, error) {
	path, err := gitOutput(ctx, dir, "rev-parse", "--path-format=absolute", option)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// Isolatable reports whether the workspace is in a Git repository
// with a committed HEAD, the only source a worktree can be prepared from.
func Isolatable(workspace string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := gitOutput(ctx, workspace, "rev-parse", "--show-toplevel"); err != nil {
		return false
	}
	_, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "HEAD^{commit}")
	return err == nil
}

func requireCleanSource(ctx context.Context, source string) error {
	status, err := gitOutput(ctx, source, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("source workspace has uncommitted changes; commit or stash them before creating an isolated worktree")
	}
	return nil
}

type workspaceOwner struct{ TaskID, Source, CommonDir, Commit string }

// reservationPath names a task's worktree reservation beside the state file.
// The task ID must be a single local path component.
func reservationPath(statePath, taskID string) (string, error) {
	if statePath == "" || taskID == "." || !filepath.IsLocal(taskID) || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid task identity for workspace")
	}
	base, err := filepath.Abs(filepath.Join(filepath.Dir(statePath), "workspaces"))
	return filepath.Join(base, taskID), err
}

// realDir returns Lstat's error, or an error when path is not a directory;
// a symlink is not one.
func realDir(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if !info.IsDir() {
		return errors.New(label + " is not a regular directory")
	}
	return nil
}

func readWorkspaceOwner(reservation, taskID string) (workspaceOwner, error) {
	var owner workspaceOwner
	path := filepath.Join(reservation, "owner.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return owner, errors.New("workspace reservation has no valid ownership record")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return owner, err
	}
	if err = json.Unmarshal(b, &owner); err != nil {
		return owner, fmt.Errorf("workspace ownership: %w", err)
	}
	if owner.TaskID != taskID || owner.Source == "" || owner.CommonDir == "" || owner.Commit == "" {
		return owner, errors.New("workspace ownership does not match task")
	}
	return owner, nil
}

// emptyReservation reports a reservation an interrupted attempt left without an
// ownership record and without a worktree. Recreating it loses nothing.
func emptyReservation(reservation string) bool {
	if _, err := os.Lstat(filepath.Join(reservation, "tree")); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	_, err := os.Lstat(filepath.Join(reservation, "owner.json"))
	return errors.Is(err, os.ErrNotExist)
}

// removeReservation deletes an ownership record and its directory without
// touching a worktree.
func removeReservation(reservation string) {
	_ = os.Remove(filepath.Join(reservation, "owner.json"))
	_ = os.Remove(reservation)
}

// discardBrokenWorktree removes a worktree directory and prunes the
// registration an interrupted worktree add can leave behind.
func discardBrokenWorktree(ctx context.Context, source, tree string) error {
	if err := os.RemoveAll(tree); err != nil {
		return err
	}
	_, _ = gitOutput(ctx, source, "worktree", "prune")
	return nil
}

func addReservedWorktree(ctx context.Context, source, tree, commit string) error {
	_, err := gitOutput(ctx, source, "worktree", "add", "--detach", "--", tree, commit)
	return err
}

// checkReservedWorktree verifies that tree is still the reserved worktree of
// the repository whose common directory is common.
func checkReservedWorktree(ctx context.Context, tree, common string) error {
	actual, err := gitPath(ctx, tree, "--git-common-dir")
	if err != nil {
		return err
	}
	if actual != common {
		return errors.New("worktree belongs to a different repository")
	}
	top, err := gitPath(ctx, tree, "--show-toplevel")
	if err != nil {
		return err
	}
	// Git reports a resolved path, so compare against the resolved tree.
	want, err := filepath.EvalSymlinks(tree)
	if err != nil {
		return err
	}
	if top != want {
		return errors.New("execution workspace is not the reserved worktree")
	}
	return nil
}

// Remove deletes the task's worktree and reservation, validating the
// ownership record before removing anything.
// A missing reservation is a successful no-op.
func Remove(ctx context.Context, spec Spec) error {
	reservation, err := reservationPath(spec.StatePath, spec.TaskID)
	if err != nil {
		return err
	}
	if err = realDir(reservation, "workspace reservation"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	owner, err := readWorkspaceOwner(reservation, spec.TaskID)
	if err != nil {
		return err
	}
	tree := filepath.Join(reservation, "tree")
	if err = realDir(tree, "workspace worktree"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.RemoveAll(reservation)
		}
		return err
	}
	common, err := gitPath(ctx, tree, "--git-common-dir")
	if err != nil {
		return fmt.Errorf("inspect worktree before cleanup: %w", err)
	}
	if common != owner.CommonDir {
		return errors.New("worktree belongs to a different repository")
	}
	if _, err = gitOutput(ctx, owner.Source, "worktree", "remove", "--force", "--", tree); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	return os.RemoveAll(reservation)
}

// Prepare checks out the source repository's committed HEAD in the
// task's reservation, or reuses the worktree an earlier attempt prepared. It
// returns the execution directory and the isolation commit.
func Prepare(ctx context.Context, spec Spec) (string, string, error) {
	root, err := gitPath(ctx, spec.Workspace, "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	source, err := filepath.EvalSymlinks(spec.Workspace)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("workspace is outside source repository")
	}
	common, err := gitPath(ctx, root, "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	reservation, err := reservationPath(spec.StatePath, spec.TaskID)
	if err != nil {
		return "", "", err
	}
	if err = os.MkdirAll(filepath.Dir(reservation), 0700); err != nil {
		return "", "", err
	}
	owner, created, err := reserveWorkspace(ctx, reservation, workspaceOwner{TaskID: spec.TaskID, Source: root, CommonDir: common})
	if err != nil {
		return "", "", err
	}
	tree := filepath.Join(reservation, "tree")
	// A reservation this attempt created must not survive a failure, or it
	// blocks every later attempt.
	defer func() {
		if created {
			if _, statErr := os.Lstat(tree); statErr == nil {
				_ = discardBrokenWorktree(ctx, root, tree)
			}
			removeReservation(reservation)
		}
	}()
	if err = ensureWorktree(ctx, tree, owner); err != nil {
		return "", "", err
	}
	cwd := filepath.Join(tree, rel)
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return "", "", errors.New("source subdirectory does not exist in committed snapshot")
	}
	created = false
	return cwd, owner.Commit, nil
}

// reserveWorkspace claims the reservation directory and records the source
// HEAD it will check out, or adopts the reservation an earlier attempt of the
// same task left. It reports whether this call created the reservation.
func reserveWorkspace(ctx context.Context, reservation string, owner workspaceOwner) (workspaceOwner, bool, error) {
	if err := os.Mkdir(reservation, 0700); errors.Is(err, os.ErrExist) {
		if err = realDir(reservation, "workspace reservation"); err != nil {
			return owner, false, err
		}
		saved, readErr := readWorkspaceOwner(reservation, owner.TaskID)
		if readErr == nil {
			if saved.Source != owner.Source || saved.CommonDir != owner.CommonDir {
				return owner, false, errors.New("workspace ownership does not match source")
			}
			return saved, false, nil
		}
		// A crash between Mkdir and the ownership write leaves an empty
		// reservation; recreate it instead of failing every later attempt.
		if !emptyReservation(reservation) {
			return owner, false, readErr
		}
	} else if err != nil {
		return owner, false, err
	}
	if err := requireCleanSource(ctx, owner.Source); err != nil {
		removeReservation(reservation)
		return owner, false, err
	}
	var err error
	if owner.Commit, err = gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		// Written atomically: a truncated record would be rejected forever.
		var b []byte
		if b, err = json.Marshal(owner); err == nil {
			err = instance.WriteFileAtomic(filepath.Join(reservation, "owner.json"), ".owner-*.json", b)
		}
	}
	if err != nil {
		removeReservation(reservation)
		return owner, false, err
	}
	return owner, true, nil
}

// ensureWorktree checks out the reserved commit, or verifies the worktree an
// earlier attempt left. An interrupted add can leave a tree Git does not
// recognize whose stale registration blocks every retry, so it is removed,
// pruned and added once more.
func ensureWorktree(ctx context.Context, tree string, owner workspaceOwner) error {
	err := realDir(tree, "execution workspace")
	if errors.Is(err, os.ErrNotExist) {
		if err = addReservedWorktree(ctx, owner.Source, tree, owner.Commit); err != nil {
			_ = discardBrokenWorktree(ctx, owner.Source, tree)
		}
		return err
	}
	if err != nil {
		return err
	}
	if err = checkReservedWorktree(ctx, tree, owner.CommonDir); err == nil {
		return nil
	}
	if removeErr := discardBrokenWorktree(ctx, owner.Source, tree); removeErr != nil {
		return fmt.Errorf("%w: %w; delete %s and retry", err, removeErr, tree)
	}
	if err = addReservedWorktree(ctx, owner.Source, tree, owner.Commit); err != nil {
		return fmt.Errorf("execution workspace %s is unusable: %w; delete the directory and retry", tree, err)
	}
	return nil
}
