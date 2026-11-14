package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorizeAcceptsLocalRequests(t *testing.T) {
	s := newTestService(t)

	cases := []struct {
		purpose string
		reason  string
	}{
		{PurposeRouter, "trusted local request accepted by the deterministic router"},
		{PurposeRelated, "explicit local request in existing thread"},
		{PurposeManual, "explicit local request"},
	}
	for _, tc := range cases {
		req := AuthorizationRequest{
			Source:    LocalPolicySource,
			Actor:     LocalActor,
			Purpose:   tc.purpose,
			TriggerID: "trigger",
			ThreadID:  "thread",
			Text:      "do the work",
			Workspace: "/workspace",
		}
		decision, err := s.authorize(req)
		if err != nil {
			t.Fatalf("%s: local request rejected: %v", tc.purpose, err)
		}
		if decision.Outcome != OutcomeAccept || decision.Reason != tc.reason {
			t.Fatalf("%s: unexpected decision: %+v", tc.purpose, decision)
		}
		if decision.PolicyID != LocalPolicyID || decision.PolicyVersion != LocalPolicyVersion {
			t.Fatalf("%s: authorization record missing its identifiers: %+v", tc.purpose, decision)
		}
		if decision.Source != LocalPolicySource || decision.Actor != LocalActor {
			t.Fatalf("%s: source or actor not recorded: %+v", tc.purpose, decision)
		}
		if decision.TriggerID != req.TriggerID {
			t.Fatalf("%s: trigger not recorded: %+v", tc.purpose, decision)
		}
		if decision.InputDigest != authorizationInputDigest(req) || !strings.HasPrefix(decision.InputDigest, "sha256:") {
			t.Fatalf("%s: input digest not recorded: %+v", tc.purpose, decision)
		}
	}
}

func TestAuthorizeAcceptsDelegatedRequest(t *testing.T) {
	s := newTestService(t)
	req := AuthorizationRequest{
		Source:    SubagentPolicySource,
		Actor:     "task:parent",
		Purpose:   PurposeSubagent,
		TriggerID: "trigger",
		ThreadID:  "thread",
		ParentID:  "parent",
		Text:      "child mandate",
		Workspace: "/workspace",
		Depth:     1,
	}
	decision := mustGet(s.authorize(req))(t)
	if decision.Outcome != OutcomeAccept {
		t.Fatalf("delegated request rejected: %+v", decision)
	}
	if decision.PolicyID != SubagentPolicyID || decision.PolicyVersion != SubagentPolicyVersion {
		t.Fatalf("authorization record missing its identifiers: %+v", decision)
	}
	if want := "delegated by running task parent; child has a separate mandate"; decision.Reason != want {
		t.Fatalf("delegation reason drifted: %q", decision.Reason)
	}
	if decision.InputDigest != authorizationInputDigest(req) {
		t.Fatalf("input digest not recorded: %+v", decision)
	}
}

func TestAuthorizeRejectsBlankMandateAndUnknownSource(t *testing.T) {
	s := newTestService(t)
	cases := []struct {
		name string
		req  AuthorizationRequest
		want string
		err  string
	}{
		{
			"blank local",
			AuthorizationRequest{Source: LocalPolicySource, Actor: LocalActor, Purpose: PurposeManual, Text: "  "},
			"local request has no mandate",
			"request rejected by policy local-1: local request has no mandate",
		},
		{
			"blank delegated",
			AuthorizationRequest{Source: SubagentPolicySource, ParentID: "parent", Depth: 1},
			"subagent request has no mandate",
			"request rejected by policy subagent-1: subagent request has no mandate",
		},
		{
			"delegated without parent",
			AuthorizationRequest{Source: SubagentPolicySource, Depth: 1, Text: "work"},
			"subagent policy requires a delegated request with a parent task",
			"request rejected by policy subagent-1: subagent policy requires a delegated request with a parent task",
		},
		{
			"delegated without depth",
			AuthorizationRequest{Source: SubagentPolicySource, ParentID: "parent", Text: "work"},
			"subagent policy requires a delegated request with a parent task",
			"request rejected by policy subagent-1: subagent policy requires a delegated request with a parent task",
		},
	}
	for _, tc := range cases {
		decision, err := s.authorize(tc.req)
		if err == nil || err.Error() != tc.err {
			t.Fatalf("%s: rejection text drifted: %v", tc.name, err)
		}
		if decision.Outcome != OutcomeReject || decision.Reason != tc.want {
			t.Fatalf("%s: unexpected decision: %+v", tc.name, decision)
		}
	}

	decision, err := s.authorize(AuthorizationRequest{Source: "external", Text: "work"})
	if err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("unknown source not rejected: %v", err)
	}
	if decision.Outcome != OutcomeReject || decision.PolicyID != "" {
		t.Fatalf("unknown source claimed an authorization record: %+v", decision)
	}
	if decision.InputDigest == "" {
		t.Fatalf("rejected request lost its input digest: %+v", decision)
	}
}

func TestAuthorizationRejectionCreatesNoTask(t *testing.T) {
	s := newTestService(t)
	var err error
	s.mu.Lock()
	_, err = s.acceptLocked(acceptedTaskInput{title: "title", agent: DefaultRouteAgent,
		auth: AuthorizationRequest{Source: "external", Actor: "intruder", Purpose: PurposeManual, Text: "should not run", Workspace: t.TempDir()}})
	s.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("rejection not reported: %v", err)
	}
	if tasks := s.ListTasks(); len(tasks) != 0 {
		t.Fatalf("rejected requests created tasks: %+v", tasks)
	}
}

func TestAuthorizationDecisionCarriesAuditEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "done")
		return err
	}))
	task := mustGet(s.SubmitRequest("audit this request", t.TempDir()))(t)
	done := await(t, s, task.ID, Completed)
	decision := done.Decision
	if decision.Outcome != OutcomeAccept || !strings.Contains(decision.Reason, "router") {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if decision.PolicyID != LocalPolicyID || decision.PolicyVersion != LocalPolicyVersion {
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

	restored := mustGet(Open(path))(t)
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

// TestAuthorizationInputDigestFormatIsStable pins the digest schema and one
// recorded value. Digests are persisted in state.json, so a changed payload or
// schema would silently invalidate every audit record written by earlier builds
// even if the digest stayed deterministic.
func TestAuthorizationInputDigestFormatIsStable(t *testing.T) {
	if authorizationDigestSchema != "orchestrator.authorization/v1" {
		t.Fatalf("digest schema drifted: %q", authorizationDigestSchema)
	}
	want := "sha256:41f759f3d1edaacea0198ccd5dd934b7e87c7aa2cc5ade743bddb9b35bbe39fd"
	got := authorizationInputDigest(AuthorizationRequest{
		Source:    LocalPolicySource,
		Actor:     LocalActor,
		Purpose:   PurposeManual,
		TriggerID: "trigger",
		ThreadID:  "thread",
		ParentID:  "parent",
		Text:      "mandate",
		Workspace: "/workspace",
		Depth:     1,
	})
	if got != want {
		t.Fatalf("recorded digest changed: %q", got)
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

func TestAuthorizationIdentifiersStayStable(t *testing.T) {
	if LocalPolicyID != "local" || LocalPolicyVersion != "local-1" {
		t.Fatalf("local authorization identifiers drifted: %q %q", LocalPolicyID, LocalPolicyVersion)
	}
	if SubagentPolicyID != "subagent" || SubagentPolicyVersion != "subagent-1" {
		t.Fatalf("subagent authorization identifiers drifted: %q %q", SubagentPolicyID, SubagentPolicyVersion)
	}
	if LocalPolicySource != "manual" || SubagentPolicySource != "subagent" {
		t.Fatalf("source names drifted: %q %q", LocalPolicySource, SubagentPolicySource)
	}
	if LocalActor != "local-user" {
		t.Fatalf("local actor drifted: %q", LocalActor)
	}
	if PurposeRouter != "router" || PurposeManual != "manual" || PurposeRelated != "related" || PurposeSubagent != "subagent" {
		t.Fatal("request purpose names drifted")
	}
	if OutcomeAccept != "accept" || OutcomeReject != "reject" {
		t.Fatal("authorization outcome names drifted")
	}
}
