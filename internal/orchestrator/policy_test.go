package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

type fakePolicy struct {
	id      string
	version string
	decide  func(AuthorizationRequest) AuthorizationResult
}

func (p fakePolicy) ID() string      { return p.id }
func (p fakePolicy) Version() string { return p.version }
func (p fakePolicy) Authorize(req AuthorizationRequest) AuthorizationResult {
	return p.decide(req)
}

func TestAuthorizationDecisionCarriesAuditEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	if err = s.RegisterPolicy(LocalPolicySource, fakePolicy{
		id:      "test-local",
		version: "test-1",
		decide: func(req AuthorizationRequest) AuthorizationResult {
			if req.Purpose != PurposeRouter || req.Actor != LocalActor {
				t.Fatalf("unexpected policy input: %+v", req)
			}
			return AuthorizationResult{Outcome: OutcomeAccept, Reason: "approved by test policy"}
		},
	}); err != nil {
		t.Fatal(err)
	}
	task, err := s.SubmitRequest("audit this request", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	decision := done.Decision
	if decision.Outcome != OutcomeAccept || decision.Reason != "approved by test policy" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if decision.PolicyID != "test-local" || decision.PolicyVersion != "test-1" {
		t.Fatalf("policy identity not recorded: %+v", decision)
	}
	if decision.Source != LocalPolicySource || decision.Actor != LocalActor {
		t.Fatalf("source or actor not recorded: %+v", decision)
	}
	if !strings.HasPrefix(decision.InputDigest, "sha256:") {
		t.Fatalf("input digest not recorded: %+v", decision)
	}
	wantDigest := authorizationInputDigest(AuthorizationRequest{
		Source:    LocalPolicySource,
		Actor:     LocalActor,
		Purpose:   PurposeRouter,
		TriggerID: done.Trigger.ID,
		ThreadID:  done.ThreadID,
		Text:      done.Prompt,
		Workspace: done.Workspace,
	})
	if decision.InputDigest != wantDigest {
		t.Fatalf("digest %q does not match canonical input %q", decision.InputDigest, wantDigest)
	}

	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Shutdown() }()
	reloaded, ok := restored.GetTask(task.ID)
	if !ok || reloaded.Decision.PolicyID != decision.PolicyID ||
		reloaded.Decision.PolicyVersion != decision.PolicyVersion ||
		reloaded.Decision.Source != decision.Source ||
		reloaded.Decision.Actor != decision.Actor ||
		reloaded.Decision.InputDigest != decision.InputDigest {
		t.Fatalf("audit evidence was not durable: %+v", reloaded.Decision)
	}
}

func TestAuthorizationRejectionCreatesNoTask(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if err = s.RegisterPolicy(LocalPolicySource, fakePolicy{
		id:      "reject-local",
		version: "reject-1",
		decide: func(AuthorizationRequest) AuthorizationResult {
			return AuthorizationResult{Outcome: OutcomeReject, Reason: "not allowed in this test"}
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitRequest("should not run", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not allowed in this test") {
		t.Fatalf("rejection not reported: %v", err)
	}
	if _, err = s.CreateTask("manual", "also rejected", t.TempDir()); err == nil || !strings.Contains(err.Error(), "not allowed in this test") {
		t.Fatalf("manual rejection not reported: %v", err)
	}
	if tasks := s.ListTasks(); len(tasks) != 0 {
		t.Fatalf("rejected requests created tasks: %+v", tasks)
	}
}

func TestAuthorizationInputDigestIsCanonicalAndSensitive(t *testing.T) {
	base := AuthorizationRequest{
		Source:    LocalPolicySource,
		Actor:     LocalActor,
		Purpose:   PurposeManual,
		TriggerID: "trigger",
		ThreadID:  "thread",
		ParentID:  "parent",
		Text:      "mandate",
		Workspace: "/workspace",
		Depth:     1,
	}
	digest := authorizationInputDigest(base)
	if again := authorizationInputDigest(base); again != digest {
		t.Fatalf("digest is not deterministic: %q != %q", digest, again)
	}
	changes := []AuthorizationRequest{base, base, base, base, base, base, base, base, base}
	changes[0].Source = SubagentPolicySource
	changes[1].Actor = "other"
	changes[2].Purpose = PurposeRouter
	changes[3].TriggerID = "other-trigger"
	changes[4].ThreadID = "other-thread"
	changes[5].ParentID = "other-parent"
	changes[6].Text = "different mandate"
	changes[7].Workspace = "/other"
	changes[8].Depth = 2
	seen := map[string]bool{digest: true}
	for _, changed := range changes {
		next := authorizationInputDigest(changed)
		if seen[next] {
			t.Fatalf("digest collision for change %+v", changed)
		}
		seen[next] = true
	}
}

func TestBuiltInLocalPolicyKeepsLocalOneSemantics(t *testing.T) {
	policy := localPolicy{}
	result := policy.Authorize(AuthorizationRequest{Source: LocalPolicySource, Actor: LocalActor, Purpose: PurposeRouter, Text: "work"})
	if result.Outcome != OutcomeAccept || result.PolicyID != LocalPolicyID || result.PolicyVersion != LocalPolicyVersion {
		t.Fatalf("unexpected local result: %+v", result)
	}
	if result.Reason != "trusted local request accepted by the deterministic router" {
		t.Fatalf("router reason changed: %+v", result)
	}
	if rejected := policy.Authorize(AuthorizationRequest{Source: "external", Text: "work"}); rejected.Outcome != OutcomeReject {
		t.Fatalf("local policy accepted an untrusted source: %+v", rejected)
	}
	if rejected := policy.Authorize(AuthorizationRequest{Source: LocalPolicySource, Text: "  "}); rejected.Outcome != OutcomeReject {
		t.Fatalf("local policy accepted an empty mandate: %+v", rejected)
	}
}
