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
	// Warning records a successful merge that still left the source checkout
	// needing attention, such as uncommitted source edits Git stashed but could
	// not reapply. It never turns a merge into a failure.
	Warning string
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
	var owner workspaceOwner
	if statePath == "" || !validTaskID(t.ID) {
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

// A subagent inherits an exact execution directory. Descendants which continue
// to share that directory also retain it after restart, including between
// attempts. An isolated child integrates back into its inherited directory.
func (s *Service) hasInheritedWorkspaceLocked(owner Task, activeOnly bool) bool {
	if owner.ExecutionWorkspace == "" {
		return false
	}
	for _, t := range s.data.Tasks {
		if t.ParentID == "" || t.Workspace != owner.ExecutionWorkspace {
			continue
		}
		if !activeOnly || t.Status == Queued || t.Status == Running || t.MergeStatus == MergeWaiting {
			return true
		}
	}
	return false
}

func (s *Service) checkRequeueIntegrationLocked(t Task) error {
	if t.integrating || t.MergeStatus == MergeWaiting {
		return errors.New("wait for worktree integration before requeueing this task")
	}
	if t.ParentID != "" {
		for _, owner := range s.data.Tasks {
			if owner.integrating && owner.ExecutionWorkspace == t.Workspace {
				return errors.New("inherited worktree is being integrated")
			}
		}
	}
	return nil
}

// The owner's successful attempt releases its execution slot even when a
// queued subagent needs that slot. Its durable integration intent is reconciled
// after configuration at startup and after tracked workers become idle. A
// waiting task prepares a reviewable commit; an approved task merges exactly the
// commit the operator reviewed.
func (s *Service) resumeMergesLocked() {
	if s.closing {
		return
	}
	for i := range s.data.Tasks {
		owner := &s.data.Tasks[i]
		if owner.integrating || s.hasInheritedWorkspaceLocked(*owner, true) {
			continue
		}
		if owner.ReviewStatus == ReviewApproved && owner.ReviewCommit != "" &&
			owner.MergeStatus != MergeFailed && owner.MergeStatus != MergeMerged && owner.MergeStatus != MergeUpToDate {
			owner.integrating = true
			s.wg.Add(1)
			go s.finishReviewedMerge(clone(*owner))
			continue
		}
		if owner.MergeStatus == MergeWaiting && owner.ReviewStatus == "" {
			owner.integrating = true
			s.wg.Add(1)
			go s.finishDeferredPreparation(clone(*owner))
			continue
		}
		// A deferred preparation that stopped for an unsuccessful descendant is
		// re-entered once that descendant is gone, so deleting the child lets the
		// operator prepare the retained worktree without re-running the agent. A
		// Git failure keeps MergeFailed and is deliberately left for the operator.
		if mergeBlockedByDescendant(*owner) {
			if _, blocked := s.integrationBlockerLocked(*owner); blocked {
				continue
			}
			owner.integrating = true
			s.wg.Add(1)
			go s.finishDeferredPreparation(clone(*owner))
		}
	}
}

// mergeBlockedByDescendant reports a deferred preparation that failed only
// because an unsuccessful descendant still held the inherited worktree.
func mergeBlockedByDescendant(t Task) bool {
	return t.MergeStatus == MergeFailed && t.ReviewStatus == "" && strings.HasPrefix(t.MergeError, blockerMergeError)
}

func (s *Service) finishDeferredPreparation(owner Task) {
	defer s.wg.Done()
	s.mu.Lock()
	blocker, blocked := s.integrationBlockerLocked(owner)
	s.mu.Unlock()
	if blocked {
		s.finalizeIntegration(owner.ID, &blocker, nil)
		return
	}
	preparation, err := s.prepareIsolatedCommit(owner)
	if err != nil {
		outcome := mergeOutcome{Status: MergeFailed, Error: err.Error(), WorktreeCommit: preparation.WorktreeCommit}
		s.finalizeIntegration(owner.ID, &outcome, nil)
		return
	}
	s.finalizeIntegration(owner.ID, nil, &preparation)
}

func (s *Service) finishReviewedMerge(owner Task) {
	defer s.wg.Done()
	s.mu.Lock()
	blocker, blocked := s.integrationBlockerLocked(owner)
	s.mu.Unlock()
	if blocked {
		s.finalizeIntegration(owner.ID, &blocker, nil)
		return
	}
	outcome := s.mergeApprovedCommit(owner)
	s.finalizeIntegration(owner.ID, &outcome, nil)
}

// finalizeIntegration records one asynchronous integration result and resumes
// any other integration intent that became eligible.
func (s *Service) finalizeIntegration(id string, merge *mergeOutcome, preparation *preparationOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		t.integrating = false
		t.UpdatedAt = time.Now().UTC()
		if merge != nil {
			t.recordMerge(*merge)
		}
		if preparation != nil {
			t.recordPreparation(*preparation)
		}
		if err := s.save(); err != nil {
			s.persistErr = err
		}
		// The in-memory task is already advanced, so resuming is safe even when
		// the write failed: it only schedules work for other tasks and never
		// re-enters this one while integrating is cleared above.
		s.resumeMergesLocked()
		return
	}
}

func (task *Task) recordMerge(merge mergeOutcome) {
	task.MergeStatus = merge.Status
	task.MergeError = merge.Error
	task.MergeWarning = merge.Warning
	task.MergeCommit = merge.MergeCommit
	if merge.WorktreeCommit != "" {
		task.WorktreeCommit = merge.WorktreeCommit
	}
	task.MergedAt = time.Time{}
	eventKind, message := "merge_"+merge.Status, ""
	switch merge.Status {
	case MergeMerged:
		eventKind = "merged"
		task.MergedAt = task.UpdatedAt
		message = "Merged worktree commit " + task.WorktreeCommit + " into source (merge commit " + merge.MergeCommit + ")"
	case MergeUpToDate:
		eventKind = "merge_up_to_date"
		task.MergedAt = task.UpdatedAt
		message = "No unmerged isolated worktree changes"
	case MergeWaiting:
		message = "Integration waits for subagents using this worktree"
	case MergeFailed:
		eventKind = "merge_failed"
		message = "Isolated workspace merge failed: " + merge.Error
	}
	task.Events = append(task.Events, Event{task.UpdatedAt, eventKind, message})
	if merge.Status == MergeMerged && merge.Warning != "" {
		task.Events = append(task.Events, Event{task.UpdatedAt, "merge_warning", "Merge succeeded with a warning: " + merge.Warning})
	}
}

// recordPreparation records a commit prepared by an isolated attempt. A new
// commit opens review instead of merging; an up-to-date completion has nothing
// to review.
func (task *Task) recordPreparation(preparation preparationOutcome) {
	task.WorktreeCommit = preparation.WorktreeCommit
	task.MergeError = ""
	task.MergeWarning = ""
	task.MergeCommit = ""
	task.MergedAt = time.Time{}
	if preparation.UpToDate {
		task.MergeStatus = MergeUpToDate
		task.MergeCommit = preparation.SourceCommit
		task.MergedAt = task.UpdatedAt
		task.Events = append(task.Events, Event{task.UpdatedAt, "merge_up_to_date", "No unmerged isolated worktree changes"})
		return
	}
	task.MergeStatus = ""
	if !task.Isolated || preparation.WorktreeCommit == "" {
		return
	}
	if err := task.beginReview(preparation.WorktreeCommit); err != nil {
		// Leaving MergeStatus empty would hide a completed task that never
		// reached review, so surface it as a merge failure for the operator.
		task.MergeStatus = MergeFailed
		task.MergeError = "could not open review: " + err.Error()
		task.Events = append(task.Events, Event{task.UpdatedAt, "review_error", "Could not open review: " + err.Error()})
		return
	}
	task.Events = append(task.Events, Event{task.UpdatedAt, "review_pending", "Prepared worktree commit " + preparation.WorktreeCommit + " awaits review"})
}
