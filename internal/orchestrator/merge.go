package orchestrator

import (
	"context"
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
	MergeWaiting  = "waiting_for_subagents"
)

const mergeTimeout = 30 * time.Second

// blockerMergeError prefixes a merge failure caused by an unsuccessful
// descendant subagent rather than by Git. It marks a failure an operator can
// clear by resolving the child task instead of re-running the agent.
const blockerMergeError = "Inspect inherited worktree and resolve unsuccessful subagent "

type mergeOutcome struct {
	Status         string
	WorktreeCommit string
	MergeCommit    string
	Error          string
}

// preparationOutcome records the commit prepared for review. UpToDate means the
// worktree has nothing the source does not already contain.
type preparationOutcome struct {
	WorktreeCommit string
	SourceCommit   string
	UpToDate       bool
}

// integrationBlockerLocked reports worktree state that prevents preparing or
// merging an isolated task. Caller holds mu. An active descendant still uses the
// inherited worktree, so integration waits; an unsuccessful descendant may have
// left partial edits that need inspection first.
func (s *Service) integrationBlockerLocked(owner Task) (mergeOutcome, bool) {
	if s.hasInheritedWorkspaceLocked(owner, true) {
		return mergeOutcome{Status: MergeWaiting}, true
	}
	for _, child := range s.data.Tasks {
		if child.ParentID == "" || child.Workspace != owner.ExecutionWorkspace {
			continue
		}
		if child.Status == Failed || child.Status == Interrupted || child.Status == Canceled || child.MergeStatus == MergeFailed {
			return mergeOutcome{Status: MergeFailed, Error: blockerMergeError + child.ID + " before retrying integration"}, true
		}
	}
	return mergeOutcome{}, false
}

// prepareIsolatedCommit commits worktree changes and records the resulting
// commit as reviewable work. It never changes the source checkout. Git
// operations never run while holding the service mutex.
func (s *Service) prepareIsolatedCommit(t Task) (preparationOutcome, error) {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), mergeTimeout)
	defer cancel()
	return prepareWorkspaceCommit(ctx, s.path, t)
}

// mergeApprovedCommit integrates exactly the reviewed commit. It deliberately
// does not commit or stage worktree changes, so content that appeared after
// review cannot ride along with the operator's approval. Merges are serialized
// because several worktrees may point at the same source checkout.
func (s *Service) mergeApprovedCommit(t Task) mergeOutcome {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), mergeTimeout)
	defer cancel()
	outcome, err := mergeReviewedWorkspace(ctx, s.path, t, t.ReviewCommit)
	if err != nil {
		outcome.Status = MergeFailed
		outcome.Error = err.Error()
	}
	return outcome
}

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

	mergeArgs := append(gitIdentityArgs(ctx, owner.Source), "merge", "--no-ff", "--no-gpg-sign", "-m", mergeSubject(t), reviewed)
	if _, err = gitOutput(ctx, owner.Source, mergeArgs...); err != nil {
		// Leave the source checkout as it was rather than preserving a partial
		// merge for the operator to discover later. Only abort a merge this
		// attempt actually started.
		abortCtx, cancel := context.WithTimeout(context.Background(), mergeTimeout)
		if _, mergeErr := gitOutput(abortCtx, owner.Source, "rev-parse", "--verify", "MERGE_HEAD"); mergeErr == nil {
			_, _ = gitOutput(abortCtx, owner.Source, "merge", "--abort")
		}
		cancel()
		return outcome, fmt.Errorf("merge isolated worktree: %w", describeMergeFailure(err))
	}
	mergeCommit, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return outcome, err
	}
	outcome.Status = MergeMerged
	outcome.MergeCommit = mergeCommit
	return outcome, nil
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
	owner, err = readWorkspaceOwner(reservation, t.ID)
	if err != nil {
		return owner, "", err
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

	common, err := gitPath(ctx, tree, "--git-common-dir")
	if err != nil {
		return owner, "", err
	}
	if common != owner.CommonDir {
		return owner, "", errors.New("worktree belongs to a different repository")
	}
	sourceRoot, err := gitPath(ctx, owner.Source, "--show-toplevel")
	if err != nil {
		return owner, "", err
	}
	if sourceRoot != owner.Source {
		return owner, "", errors.New("source repository no longer matches the workspace reservation")
	}
	sourceCommon, err := gitPath(ctx, owner.Source, "--git-common-dir")
	if err != nil {
		return owner, "", err
	}
	if sourceCommon != owner.CommonDir {
		return owner, "", errors.New("source repository no longer matches the worktree")
	}
	root, err := gitPath(ctx, tree, "--show-toplevel")
	if err != nil {
		return owner, "", err
	}
	if root != treeAbs {
		return owner, "", errors.New("merge path is not the reserved worktree")
	}
	if _, err = gitOutput(ctx, tree, "merge-base", "--is-ancestor", owner.Commit, "HEAD"); err != nil {
		return owner, "", errors.New("worktree history no longer descends from the isolation commit")
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
