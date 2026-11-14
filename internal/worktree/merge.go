package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Preparation records the commit prepared for review. UpToDate means the
// worktree has nothing the source does not already contain.
type Preparation struct {
	WorktreeCommit string
	SourceCommit   string
	UpToDate       bool
}

// MergeResult describes a merge of a reviewed commit. WorktreeCommit is set
// once the worktree passed validation, also when the merge then fails.
type MergeResult struct {
	WorktreeCommit string
	MergeCommit    string
	UpToDate       bool // the source already contained the commit
	// Warning records a successful merge that still left the source checkout
	// needing attention, such as uncommitted source edits Git stashed but could
	// not reapply. It never turns a merge into a failure.
	Warning string
}

// MergeTimeout bounds one preparation, merge or abort.
const MergeTimeout = 30 * time.Second

// Commit commits all worktree changes and reports whether the
// resulting commit already exists in the source checkout. It validates that the
// worktree still belongs to the task and source recorded in owner.json before
// touching either repository. The source checkout is never modified.
func Commit(ctx context.Context, spec Spec) (Preparation, error) {
	owner, tree, err := validateMergeWorkspace(ctx, spec)
	if err != nil {
		return Preparation{}, err
	}
	outcome := Preparation{}

	dirty, err := gitOutput(ctx, tree, "status", "--porcelain")
	if err != nil {
		return outcome, err
	}
	if dirty != "" {
		if _, err = gitOutput(ctx, tree, "add", "-A"); err != nil {
			return outcome, err
		}
		commitArgs := append(gitIdentityArgs(ctx, tree), "commit", "--no-gpg-sign", "-m", mergeSubject(spec))
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

// Merge merges exactly the reviewed commit into the source
// repository's current checkout. It verifies that the commit is still part of
// the retained worktree history and descends from the isolation commit. A
// failed merge is aborted so the source does not remain in a conflicted state;
// uncommitted worktree content is left untouched by the merge.
func Merge(ctx context.Context, spec Spec, reviewed string) (MergeResult, error) {
	owner, tree, err := validateMergeWorkspace(ctx, spec)
	if err != nil {
		return MergeResult{}, err
	}
	outcome := MergeResult{WorktreeCommit: reviewed}

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
		outcome.UpToDate = true
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
	mergeArgs := append(gitIdentityArgs(ctx, owner.Source), "-c", "merge.autoStash=true", "merge", "--no-ff", "--no-gpg-sign", "-m", mergeSubject(spec), reviewed)
	if _, err = gitOutput(ctx, owner.Source, mergeArgs...); err != nil {
		// Leave the source checkout as it was rather than preserving a partial
		// merge for the operator to discover later. Only abort a merge this
		// attempt actually started. Aborting also reapplies the autostash.
		failure := describeMergeFailure(err)
		abortCtx, cancel := context.WithTimeout(context.Background(), MergeTimeout)
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
func validateMergeWorkspace(ctx context.Context, spec Spec) (workspaceOwner, string, error) {
	fail := func(err error) (workspaceOwner, string, error) { return workspaceOwner{}, "", err }
	reservation, err := reservationPath(spec.StatePath, spec.TaskID)
	if err != nil {
		return fail(err)
	}
	if err = realDir(reservation, "merge workspace reservation"); err != nil {
		return fail(err)
	}
	owner, err := readWorkspaceOwner(reservation, spec.TaskID)
	if err != nil {
		return fail(err)
	}
	tree := filepath.Join(reservation, "tree")
	if err = realDir(tree, "merge worktree"); err != nil {
		return fail(err)
	}
	if spec.Execution == "" {
		return fail(errors.New("task has no prepared execution workspace"))
	}
	treeAbs, err := filepath.EvalSymlinks(tree)
	if err != nil {
		return fail(err)
	}
	executionAbs, err := filepath.EvalSymlinks(spec.Execution)
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

func mergeSubject(spec Spec) string {
	title := strings.Join(strings.Fields(spec.Title), " ")
	if title == "" {
		title = "isolated work"
	}
	runes := []rune(title)
	if len(runes) > 72 {
		title = string(runes[:69]) + "..."
	}
	return "Merge isolated task " + spec.TaskID + ": " + title
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

// ReconcileDiscard reports whether a discarded worktree is gone. It never
// removes a surviving tree. If Git removed it, only an empty reservation or
// the task's validated ownership record may remain, and both are cleared.
func ReconcileDiscard(spec Spec) (bool, error) {
	reservation, err := reservationPath(spec.StatePath, spec.TaskID)
	if err != nil {
		return false, err
	}
	for _, dir := range []string{filepath.Dir(reservation), reservation} {
		if err = realDir(dir, "workspace reservation"); errors.Is(err, os.ErrNotExist) {
			return true, nil
		} else if err != nil {
			return false, err
		}
	}
	if _, err = os.Lstat(filepath.Join(reservation, "tree")); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, err := os.ReadDir(reservation)
	if err != nil {
		return false, err
	}
	if len(entries) > 0 {
		if len(entries) != 1 || entries[0].Name() != "owner.json" {
			return false, errors.New("unexpected files in discarded worktree reservation")
		}
		if _, err = readWorkspaceOwner(reservation, spec.TaskID); err != nil {
			return false, err
		}
		if err = os.Remove(filepath.Join(reservation, "owner.json")); err != nil {
			return false, err
		}
	}
	return true, os.Remove(reservation)
}

// Discard removes a worktree after repeating the ownership checks made
// when it was prepared. Removal deliberately omits --force so Git refuses any
// worktree that still holds uncommitted or untracked files.
func Discard(ctx context.Context, spec Spec) error {
	owner, tree, err := validateMergeWorkspace(ctx, spec)
	if err != nil {
		return err
	}
	dirty, err := gitOutput(ctx, tree, "status", "--porcelain")
	if err != nil {
		return err
	}
	if dirty != "" {
		return errors.New("worktree has uncommitted changes; commit or remove them before discarding")
	}
	head, err := gitOutput(ctx, tree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	sourceHead, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if _, err = gitOutput(ctx, owner.Source, "merge-base", "--is-ancestor", head, sourceHead); err != nil {
		return errors.New("worktree commit is not merged into the source checkout; merge it before discarding")
	}
	if _, err = gitOutput(ctx, owner.Source, "worktree", "remove", "--", tree); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	return nil
}
