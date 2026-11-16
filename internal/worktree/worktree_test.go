package worktree

import (
	"errors"
	"strings"
	"testing"
)

func TestRequestsChanges(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"inspection", "review the parser", false},
		{"noun after inspection", "review the recent fix", false},
		{"question about a command", "explain how to run gofmt", false},
		{"run tests", "run the tests", false},
		{"read-only git", "git status", false},
		{"read-only git worktree", "git worktree list", false},
		{"ambiguous", "do work", false},
		{"subject inspection", "we should review the parser", false},
		{"explicit change", "fix the parser", true},
		{"connector", "review the parser and fix the bug", true},
		{"punctuation", "what is wrong? fix the parser", true},
		{"newline", "review the code\nupdate the docs", true},
		{"run gofmt", "run gofmt", true},
		{"git mutation", "git commit the changes", true},
		{"git worktree mutation", "git worktree add ../review-tree", true},
		{"check out", "check out a new branch", true},
		{"politeness", "Can you add regression tests", true},
		{"subject and modal", "we could add regression tests", true},
		{"contraction", "I'd like to update the docs", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequestsChanges(tc.prompt); got != tc.want {
				t.Fatalf("RequestsChanges(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestUntrackedMergeFailureGuidance(t *testing.T) {
	cause := errors.New("The following untracked working tree files would be overwritten by merge:\n\tlocal.txt")
	err := describeMergeFailure(cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "move or commit") {
		t.Fatalf("missing untracked file guidance or Git diagnostic: %v", err)
	}
}

// The prepared worktree commit must not claim to be a merge; only the real
// merge into the source checkout may carry the merge subject.
func TestPreparedCommitDoesNotUseMergeSubject(t *testing.T) {
	spec := Spec{TaskID: "abc123", Title: "add numbered rows"}
	prepared, merged := prepareSubject(spec), mergeSubject(spec)
	if !strings.HasPrefix(prepared, "Isolated task abc123: add numbered rows") {
		t.Fatalf("prepare subject = %q", prepared)
	}
	if strings.HasPrefix(prepared, "Merge ") || prepared == merged {
		t.Fatalf("prepared commit %q claims to merge the task", prepared)
	}
	if merged != "Merge isolated task abc123: add numbered rows" {
		t.Fatalf("merge subject = %q", merged)
	}
}
