package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Merge outcomes are recorded separately from the execution attempt. A merge
// failure does not rewrite a successful process exit into a failed attempt.
const (
	MergeMerged   = "merged"
	MergeUpToDate = "up_to_date"
	MergeFailed   = "failed"
)

const mergeTimeout = 30 * time.Second

type mergeOutcome struct {
	Status         string
	WorktreeCommit string
	MergeCommit    string
	Error          string
}

// autoMergeIsolated integrates a successful isolated attempt back into its
// source repository. Merges are serialized because several worktrees may point
// at the same source checkout. Git operations never run while holding the
// service mutex.
func (s *Service) autoMergeIsolated(t Task) mergeOutcome {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), mergeTimeout)
	defer cancel()
	outcome, err := mergeIsolatedWorkspace(ctx, s.path, t)
	if err != nil {
		outcome.Status = MergeFailed
		outcome.Error = err.Error()
	}
	return outcome
}

// mergeIsolatedWorkspace commits all worktree changes, then merges that commit
// into the source repository's current checkout. It validates that the worktree
// still belongs to the task and source recorded in owner.json before touching
// either repository. A failed merge is aborted so the source does not remain in
// a conflicted state; the worktree is always retained.
func mergeIsolatedWorkspace(ctx context.Context, statePath string, t Task) (mergeOutcome, error) {
	owner, tree, err := validateMergeWorkspace(ctx, statePath, t)
	if err != nil {
		return mergeOutcome{}, err
	}
	outcome := mergeOutcome{Status: MergeFailed}

	dirty, err := gitOutput(ctx, tree, "status", "--porcelain")
	if err != nil {
		return outcome, err
	}
	if dirty != "" {
		if _, err = gitOutput(ctx, tree, "add", "-A"); err != nil {
			return outcome, err
		}
		commitArgs := append(gitIdentityArgs(ctx, tree), "commit", "--no-gpg-sign", "-m", mergeSubject(t))
		if _, err = gitOutput(ctx, tree, commitArgs...); err != nil {
			return outcome, err
		}
	}
	worktreeHead, err := gitOutput(ctx, tree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return outcome, err
	}
	outcome.WorktreeCommit = worktreeHead

	sourceHead, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return outcome, err
	}
	// A retry can complete without new changes, or another task may already
	// have merged this worktree commit. Both are successful no-ops.
	if _, err = gitOutput(ctx, owner.Source, "merge-base", "--is-ancestor", worktreeHead, sourceHead); err == nil {
		outcome.Status = MergeUpToDate
		outcome.MergeCommit = sourceHead
		return outcome, nil
	}
	if _, err = gitOutput(ctx, owner.Source, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
		return outcome, errors.New("source repository already has an in-progress merge")
	}

	mergeArgs := append(gitIdentityArgs(ctx, owner.Source), "merge", "--no-ff", "--no-gpg-sign", "-m", mergeSubject(t), worktreeHead)
	if _, err = gitOutput(ctx, owner.Source, mergeArgs...); err != nil {
		// Leave the source checkout as it was rather than preserving a partial
		// merge for the operator to discover later. Only abort a merge this
		// attempt actually started.
		abortCtx, cancel := context.WithTimeout(context.Background(), mergeTimeout)
		if _, mergeErr := gitOutput(abortCtx, owner.Source, "rev-parse", "--verify", "MERGE_HEAD"); mergeErr == nil {
			_, _ = gitOutput(abortCtx, owner.Source, "merge", "--abort")
		}
		cancel()
		return outcome, fmt.Errorf("merge isolated worktree: %w", err)
	}
	mergeCommit, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return outcome, err
	}
	outcome.Status = MergeMerged
	outcome.MergeCommit = mergeCommit
	return outcome, nil
}

// validateMergeWorkspace repeats the ownership and repository checks made when
// the worktree was prepared. Merging is destructive to the source checkout, so
// it must not trust the persisted execution path alone.
func validateMergeWorkspace(ctx context.Context, statePath string, t Task) (workspaceOwner, string, error) {
	var owner workspaceOwner
	if statePath == "" || t.ID == "" || filepath.Base(t.ID) != t.ID || t.ID == "." || t.ID == ".." {
		return owner, "", errors.New("invalid task identity for merge")
	}
	base, err := filepath.Abs(filepath.Join(filepath.Dir(statePath), "workspaces"))
	if err != nil {
		return owner, "", err
	}
	reservation := filepath.Join(base, t.ID)
	info, err := os.Lstat(reservation)
	if err != nil {
		return owner, "", fmt.Errorf("merge workspace reservation: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return owner, "", errors.New("merge workspace reservation is not a regular directory")
	}
	ownerInfo, err := os.Lstat(filepath.Join(reservation, "owner.json"))
	if err != nil || !ownerInfo.Mode().IsRegular() {
		return owner, "", errors.New("merge workspace reservation has no valid ownership record")
	}
	b, err := os.ReadFile(filepath.Join(reservation, "owner.json"))
	if err != nil {
		return owner, "", err
	}
	if err = json.Unmarshal(b, &owner); err != nil {
		return owner, "", fmt.Errorf("merge workspace ownership: %w", err)
	}
	if owner.TaskID != t.ID || owner.Source == "" || owner.CommonDir == "" || owner.Commit == "" {
		return owner, "", errors.New("merge workspace ownership does not match task")
	}

	tree := filepath.Join(reservation, "tree")
	treeInfo, err := os.Lstat(tree)
	if err != nil {
		return owner, "", fmt.Errorf("merge worktree: %w", err)
	}
	if !treeInfo.IsDir() || treeInfo.Mode()&os.ModeSymlink != 0 {
		return owner, "", errors.New("merge worktree is not a regular directory")
	}
	treeAbs, err := filepath.EvalSymlinks(tree)
	if err != nil {
		return owner, "", err
	}
	execution := t.ExecutionWorkspace
	if execution == "" {
		return owner, "", errors.New("task has no prepared execution workspace")
	}
	executionAbs, err := filepath.EvalSymlinks(execution)
	if err != nil {
		return owner, "", err
	}
	if executionAbs != treeAbs && !strings.HasPrefix(executionAbs, treeAbs+string(os.PathSeparator)) {
		return owner, "", errors.New("execution workspace is not inside the reserved worktree")
	}

	common, err := gitOutput(ctx, tree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return owner, "", err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return owner, "", err
	}
	if common != owner.CommonDir {
		return owner, "", errors.New("worktree belongs to a different repository")
	}
	sourceRoot, err := gitOutput(ctx, owner.Source, "rev-parse", "--show-toplevel")
	if err != nil {
		return owner, "", err
	}
	sourceRoot, err = filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return owner, "", err
	}
	if sourceRoot != owner.Source {
		return owner, "", errors.New("source repository no longer matches the workspace reservation")
	}
	sourceCommon, err := gitOutput(ctx, owner.Source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return owner, "", err
	}
	sourceCommon, err = filepath.EvalSymlinks(sourceCommon)
	if err != nil {
		return owner, "", err
	}
	if sourceCommon != owner.CommonDir {
		return owner, "", errors.New("source repository no longer matches the worktree")
	}
	if root, err := gitOutput(ctx, tree, "rev-parse", "--show-toplevel"); err != nil {
		return owner, "", err
	} else if root, err = filepath.EvalSymlinks(root); err != nil {
		return owner, "", err
	} else if root != treeAbs {
		return owner, "", errors.New("merge path is not the reserved worktree")
	}
	if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", owner.Commit, "HEAD"); err != nil {
		return owner, "", errors.New("worktree history no longer descends from the isolation commit")
	}
	return owner, tree, nil
}

func mergeSubject(t Task) string {
	title := strings.Join(strings.Fields(t.Title), " ")
	if title == "" {
		title = "isolated work"
	}
	runes := []rune(title)
	if len(runes) > 72 {
		title = string(runes[:69]) + "..."
	}
	return "Merge isolated task " + t.ID + ": " + title
}

// gitIdentityArgs supply a neutral fallback identity only when the repository
// has no user.name or user.email configured. Explicit configuration wins.
func gitIdentityArgs(ctx context.Context, dir string) []string {
	var args []string
	if value, err := gitOutput(ctx, dir, "config", "--get", "user.name"); err != nil || strings.TrimSpace(value) == "" {
		args = append(args, "-c", "user.name=Orchestrator")
	}
	if value, err := gitOutput(ctx, dir, "config", "--get", "user.email"); err != nil || strings.TrimSpace(value) == "" {
		args = append(args, "-c", "user.email=orchestrator@localhost")
	}
	return args
}
