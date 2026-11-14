package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// prepareWorkspaceCommit commits all worktree changes and reports whether the
// resulting commit already exists in the source checkout. It validates that the
// worktree still belongs to the task and source recorded in owner.json before
// touching either repository. The source checkout is never modified.
func prepareWorkspaceCommit(ctx context.Context, statePath string, t Task) (preparationOutcome, error) {
	owner, tree, err := validateMergeWorkspace(ctx, statePath, t)
	if err != nil {
		return preparationOutcome{}, err
	}
	outcome := preparationOutcome{}

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
	outcome.SourceCommit = sourceHead
	if _, err = gitOutput(ctx, owner.Source, "merge-base", "--is-ancestor", worktreeHead, sourceHead); err == nil {
		outcome.UpToDate = true
	}
	return outcome, nil
}

// mergeReviewedWorkspace merges exactly the reviewed commit into the source
// repository's current checkout. It verifies that the commit is still part of
// the retained worktree history and descends from the isolation commit. A
// failed merge is aborted so the source does not remain in a conflicted state;
// uncommitted worktree content is left untouched by the merge.
func mergeReviewedWorkspace(ctx context.Context, statePath string, t Task, reviewed string) (mergeOutcome, error) {
	reviewed = strings.TrimSpace(reviewed)
	if reviewed == "" || reviewed != t.ReviewCommit {
		return mergeOutcome{}, errors.New("reviewed commit does not match the task review")
	}
	owner, tree, err := validateMergeWorkspace(ctx, statePath, t)
	if err != nil {
		return mergeOutcome{}, err
	}
	outcome := mergeOutcome{Status: MergeFailed, WorktreeCommit: reviewed}

	resolved, err := gitOutput(ctx, tree, "rev-parse", "--verify", reviewed+"^{commit}")
	if err != nil {
		return outcome, errors.New("reviewed commit is not available in the worktree")
	}
	if resolved != reviewed {
		return outcome, errors.New("reviewed commit is not the exact worktree commit")
	}
	if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", reviewed, "HEAD"); err != nil {
		return outcome, errors.New("reviewed commit is no longer part of the worktree history")
	}
	if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", owner.Commit, reviewed); err != nil {
		return outcome, errors.New("reviewed commit does not descend from the isolation commit")
	}

	sourceHead, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return outcome, err
	}
	// A restart can repeat an approval whose merge already completed. An
	// ancestor check is also used when another task merged this commit.
	if _, err = gitOutput(ctx, owner.Source, "merge-base", "--is-ancestor", reviewed, sourceHead); err == nil {
		outcome.Status = MergeUpToDate
		outcome.MergeCommit = sourceHead
		return outcome, nil
	}
	if _, err = gitOutput(ctx, owner.Source, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
		return outcome, errors.New("source repository already has an in-progress merge")
	}

	// merge.autoStash handles the common case of an operator who keeps editing
	// the source checkout while tasks run. Git refuses to merge into a checkout
	// whose uncommitted changes the merge would overwrite, so it stashes those
	// edits, merges, and applies them again. When they cannot be reapplied it
	// keeps the stash entry instead of losing them, which the caller reports as
	// a warning rather than a failed merge.
	mergeArgs := append(gitIdentityArgs(ctx, owner.Source), "-c", "merge.autoStash=true", "merge", "--no-ff", "--no-gpg-sign", "-m", mergeSubject(t), reviewed)
	if _, err = gitOutput(ctx, owner.Source, mergeArgs...); err != nil {
		// Leave the source checkout as it was rather than preserving a partial
		// merge for the operator to discover later. Only abort a merge this
		// attempt actually started. Aborting also reapplies the autostash.
		failure := describeMergeFailure(err)
		abortCtx, cancel := context.WithTimeout(context.Background(), mergeTimeout)
		if _, mergeErr := gitOutput(abortCtx, owner.Source, "rev-parse", "--verify", "MERGE_HEAD"); mergeErr == nil {
			if _, abortErr := gitOutput(abortCtx, owner.Source, "merge", "--abort"); abortErr != nil {
				failure = fmt.Errorf("%w (aborting the source merge also failed: %v; the source checkout may still have a merge in progress)", failure, abortErr)
			}
		}
		if note := retainedAutostashNote(abortCtx, owner.Source); note != "" {
			failure = fmt.Errorf("%w (%s)", failure, note)
		}
		cancel()
		return outcome, fmt.Errorf("merge isolated worktree: %w", failure)
	}
	mergeCommit, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return outcome, err
	}
	outcome.Status = MergeMerged
	outcome.MergeCommit = mergeCommit
	outcome.Warning = sourceStashWarning(ctx, owner.Source)
	return outcome, nil
}

// retainedAutostash returns the stash entry Git created for a merge and kept
// because the operator's uncommitted source edits could not be reapplied. Git
// labels the entry "autostash", prefixed with the branch it was taken from.
// Matching that label keeps stashes the operator created by hand out of the
// report.
func retainedAutostash(ctx context.Context, dir string) string {
	list, err := gitOutput(ctx, dir, "stash", "list", "--format=%gd%x09%s")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(list, "\n") {
		ref, subject, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		subject = strings.TrimSpace(subject)
		if subject == "autostash" || strings.HasSuffix(subject, ": autostash") {
			return strings.TrimSpace(ref)
		}
	}
	return ""
}

// sourceStashWarning reports a merge that succeeded while the operator's
// uncommitted source edits stayed in a stash entry. Git leaves the affected
// files with conflict markers, so the operator needs to know where the content
// is before the checkout keeps surprising them.
func sourceStashWarning(ctx context.Context, dir string) string {
	ref := retainedAutostash(ctx, dir)
	if ref == "" {
		return ""
	}
	warning := "uncommitted source changes are kept in the autostash entry " + ref + " because they conflict with the merged files"
	if unmerged, err := gitOutput(ctx, dir, "diff", "--name-only", "--diff-filter=U"); err == nil {
		if files := strings.Join(strings.Fields(unmerged), ", "); files != "" {
			warning += " (conflict markers in " + files + ")"
		}
	}
	return warning + "; resolve them and then run git stash drop"
}

// retainedAutostashNote explains a stash entry left behind by an aborted merge,
// where Git normally restores the operator's uncommitted edits itself.
func retainedAutostashNote(ctx context.Context, dir string) string {
	ref := retainedAutostash(ctx, dir)
	if ref == "" {
		return ""
	}
	return "uncommitted source changes were not restored automatically and are kept in the autostash entry " + ref
}

// describeMergeFailure turns Git's local-checkout refusals into actionable
// guidance. Git aborts instead of overwriting uncommitted or untracked source
// files, so an overlap with the worktree's changes blocks the merge until the
// operator commits or stashes those files and retries the task. Other merge
// failures are returned unchanged.
func describeMergeFailure(err error) error {
	message := strings.ToLower(err.Error())
	guidance := ""
	switch {
	case strings.Contains(message, "untracked working tree files"):
		guidance = "source checkout has untracked files this worktree would overwrite; move or commit them, then retry the task"
	case strings.Contains(message, "your local changes") || strings.Contains(message, "would be overwritten"):
		guidance = "source checkout has uncommitted changes to files this worktree also changes; commit or stash them, then retry the task"
	}
	if guidance == "" {
		return err
	}
	return fmt.Errorf("%s: %w", guidance, err)
}

// validateMergeWorkspace repeats the ownership and repository checks made when
// the worktree was prepared. Merging is destructive to the source checkout, so
// it must not trust the persisted execution path alone.
func validateMergeWorkspace(ctx context.Context, statePath string, t Task) (workspaceOwner, string, error) {
	fail := func(err error) (workspaceOwner, string, error) { return workspaceOwner{}, "", err }
	reservation, err := reservationPath(statePath, t.ID)
	if err != nil {
		return fail(err)
	}
	if err = realDir(reservation, "merge workspace reservation"); err != nil {
		return fail(err)
	}
	owner, err := readWorkspaceOwner(reservation, t.ID)
	if err != nil {
		return fail(err)
	}
	tree := filepath.Join(reservation, "tree")
	if err = realDir(tree, "merge worktree"); err != nil {
		return fail(err)
	}
	if t.ExecutionWorkspace == "" {
		return fail(errors.New("task has no prepared execution workspace"))
	}
	treeAbs, err := filepath.EvalSymlinks(tree)
	if err != nil {
		return fail(err)
	}
	executionAbs, err := filepath.EvalSymlinks(t.ExecutionWorkspace)
	if err != nil {
		return fail(err)
	}
	if executionAbs != treeAbs && !strings.HasPrefix(executionAbs, treeAbs+string(os.PathSeparator)) {
		return fail(errors.New("execution workspace is not inside the reserved worktree"))
	}
	if err = checkReservedWorktree(ctx, tree, owner.CommonDir); err != nil {
		return fail(err)
	}
	sourceRoot, err := gitPath(ctx, owner.Source, "--show-toplevel")
	if err == nil && sourceRoot != owner.Source {
		err = errors.New("source repository no longer matches the workspace reservation")
	}
	if err != nil {
		return fail(err)
	}
	sourceCommon, err := gitPath(ctx, owner.Source, "--git-common-dir")
	if err == nil && sourceCommon != owner.CommonDir {
		err = errors.New("source repository no longer matches the worktree")
	}
	if err != nil {
		return fail(err)
	}
	if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", owner.Commit, "HEAD"); err != nil {
		return fail(errors.New("worktree history no longer descends from the isolation commit"))
	}
	return owner, tree, nil
}

// MergeConflict reports whether a failed approved merge was caused by a
// conflict. The merge is always aborted, so this is an operator-facing
// classification of the retained error, not a separate merge outcome.
func (t Task) MergeConflict() bool {
	if t.MergeStatus != MergeFailed {
		return false
	}
	return strings.Contains(strings.ToLower(t.MergeError), "conflict")
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
