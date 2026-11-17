package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
		commitArgs := append(gitIdentityArgs(ctx, tree), "commit", "--no-gpg-sign", "-m", prepareSubject(spec))
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

// DiffStat summarizes the line churn from the isolation base to commit. An
// empty commit compares the worktree's working tree, so a running task reports
// its uncommitted progress too.
func DiffStat(ctx context.Context, spec Spec, commit string) (added, removed int, err error) {
	owner, tree, err := validateMergeWorkspace(ctx, spec)
	if err != nil {
		return 0, 0, err
	}
	args := []string{"diff", "--numstat", "--no-ext-diff", "--no-color", owner.Commit}
	if commit != "" {
		resolved, resolveErr := gitOutput(ctx, tree, "rev-parse", "--verify", commit+"^{commit}")
		if resolveErr != nil || resolved != commit {
			return 0, 0, errors.New("diff commit is not available in the worktree")
		}
		if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", owner.Commit, commit); err != nil {
			return 0, 0, errors.New("diff commit does not descend from the isolation commit")
		}
		args = append(args, commit)
	}
	args = append(args, "--")
	output, err := gitOutput(ctx, tree, args...)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		// Binary files report "-" for both counts.
		if a, aErr := strconv.Atoi(fields[0]); aErr == nil {
			added += a
		}
		if r, rErr := strconv.Atoi(fields[1]); rErr == nil {
			removed += r
		}
	}
	if commit == "" {
		// git diff omits files Git has never seen. Agents create those often,
		// so count their lines directly; a NUL byte marks a binary file.
		untracked, listErr := gitOutput(ctx, tree, "ls-files", "--others", "--exclude-standard", "--")
		if listErr != nil {
			return 0, 0, listErr
		}
		for _, path := range strings.Split(untracked, "\n") {
			if path == "" {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(tree, path))
			if readErr != nil || bytes.IndexByte(data, 0) >= 0 {
				continue
			}
			added += newlines(data)
		}
	}
	return added, removed, nil
}

// newlines counts the lines in a text file, including a final unterminated one.
func newlines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	count := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		count++
	}
	return count
}

// ReviewDiff returns the patch from the isolation base to a validated reviewed commit.
func ReviewDiff(ctx context.Context, spec Spec, reviewed string) (string, error) {
	owner, tree, err := validateMergeWorkspace(ctx, spec)
	if err != nil {
		return "", err
	}
	resolved, err := gitOutput(ctx, tree, "rev-parse", "--verify", reviewed+"^{commit}")
	if err != nil || resolved != reviewed {
		return "", errors.New("reviewed commit is not available in the worktree")
	}
	if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", reviewed, "HEAD"); err != nil {
		return "", errors.New("reviewed commit is no longer part of the worktree history")
	}
	if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", owner.Commit, reviewed); err != nil {
		return "", errors.New("reviewed commit does not descend from the isolation commit")
	}
	return gitOutput(ctx, tree, "diff", "--no-ext-diff", "--no-color", "--find-renames", owner.Commit, reviewed, "--")
}

// Merge merges exactly the reviewed commit into the source
// repository's current checkout. It verifies that the commit is still part of
// the retained worktree history and descends from the isolation commit. If the
// merge conflicts, the merge commit is rolled back so the source does not keep
// conflict markers; uncommitted worktree content is left untouched.
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
	// edits, merges, and applies them again. When they cannot be reapplied, Git
	// leaves the stash entry and the file with conflict markers; roll the whole
	// merge back instead, because a merge the operator cannot see behind their
	// own edits is not a success.
	stashBefore := stashTip(ctx, owner.Source)
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
		if note := retainedAutostashNote(abortCtx, owner.Source, stashBefore); note != "" {
			failure = fmt.Errorf("%w (%s)", failure, note)
		}
		cancel()
		return outcome, fmt.Errorf("merge isolated worktree: %w", failure)
	}
	if retainedAutostash(ctx, owner.Source, stashBefore) != "" {
		files, _ := gitOutput(ctx, owner.Source, "diff", "--name-only", "--diff-filter=U")
		if files = strings.Join(strings.Fields(files), ", "); files == "" {
			files = "the merged files"
		}
		failure := fmt.Errorf("uncommitted source changes conflict with this merge (%s); commit or stash them, then retry the task", files)
		rollbackCtx, cancel := context.WithTimeout(context.Background(), MergeTimeout)
		if _, resetErr := gitOutput(rollbackCtx, owner.Source, "reset", "--hard", sourceHead); resetErr != nil {
			failure = fmt.Errorf("%w (rolling the source checkout back failed: %v; the merge is still in place)", failure, resetErr)
		} else if _, popErr := gitOutput(rollbackCtx, owner.Source, "stash", "pop", "--index", "stash@{0}"); popErr != nil {
			failure = fmt.Errorf("%w (the source checkout was rolled back but the changes remain in stash@{0}: %v)", failure, popErr)
		}
		cancel()
		return outcome, fmt.Errorf("merge isolated worktree: %w", failure)
	}
	mergeCommit, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return outcome, err
	}
	outcome.MergeCommit = mergeCommit
	return outcome, nil
}

// stashTip identifies the current top stash entry, so a merge can tell its own
// autostash apart from entries an operator already had. An absent stash is an
// empty tip.
func stashTip(ctx context.Context, dir string) string {
	tip, err := gitOutput(ctx, dir, "rev-parse", "--verify", "refs/stash")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(tip)
}

// retainedAutostash returns the stash entry a merge created and kept because
// the operator's uncommitted source edits could not be reapplied. A changed tip
// with Git's "autostash" subject proves the entry came from this merge, so a
// stale stash left by an earlier attempt is never mistaken for it.
func retainedAutostash(ctx context.Context, dir, before string) string {
	if tip := stashTip(ctx, dir); tip == "" || tip == before {
		return ""
	}
	list, err := gitOutput(ctx, dir, "stash", "list", "--format=%gd%x09%s")
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(list), "\n")
	ref, subject, found := strings.Cut(line, "\t")
	if !found {
		return ""
	}
	subject = strings.TrimSpace(subject)
	if subject == "autostash" || strings.HasSuffix(subject, ": autostash") {
		return strings.TrimSpace(ref)
	}
	return ""
}

// retainedAutostashNote explains a stash entry left behind by an aborted merge,
// where Git normally restores the operator's uncommitted edits itself.
func retainedAutostashNote(ctx context.Context, dir, before string) string {
	ref := retainedAutostash(ctx, dir, before)
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
	return commitSubject("Merge isolated task ", spec)
}

// prepareSubject names the commit made inside the worktree. It deliberately
// differs from the merge subject so history does not show two commits that both
// claim to merge the task, one of them with a single parent.
func prepareSubject(spec Spec) string {
	return commitSubject("Isolated task ", spec)
}

func commitSubject(prefix string, spec Spec) string {
	title := strings.Join(strings.Fields(spec.Title), " ")
	if title == "" {
		title = "isolated work"
	}
	runes := []rune(title)
	if len(runes) > 72 {
		title = string(runes[:69]) + "..."
	}
	return prefix + spec.TaskID + ": " + title
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
