package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// LocalPolicyVersion identifies the deterministic routing policy used for the
// trusted local source. Model-assisted evaluation is deliberately not
// implemented: a local submission is an explicit request, so acceptance is
// deterministic and auditable.
const LocalPolicyVersion = "local-1"

// DefaultRouteAgent is the executor assigned by the local routing policy.
const DefaultRouteAgent = "pi"

// SubmitRequest is the unified entry point for local work. It records one
// explicit request, accepts it under the deterministic local policy, records
// the assignment, and requests execution. Acceptance, assignment and start
// remain separate durable records.
func (s *Service) SubmitRequest(text, workspace string) (Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Task{}, errors.New("request text is required")
	}
	isolated := s.defaultTaskIsolation(workspace)
	workspaceChoice := "shared workspace"
	if isolated {
		workspaceChoice = "isolated committed-HEAD worktree"
	}
	assignment := fmt.Sprintf("Router assignment: agent %s, %s, execution requested (%s)", DefaultRouteAgent, workspaceChoice, LocalPolicyVersion)
	task, err := s.createTaskWithIsolation(requestTitle(text), text, workspace, isolated,
		AuthorizationRequest{Source: LocalPolicySource, Actor: LocalActor, Purpose: PurposeRouter}, assignment)
	if err != nil {
		return Task{}, err
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		// The request is already durable and accepted; report the start failure
		// against the accepted task rather than pretending nothing happened.
		accepted, _ := s.GetTask(task.ID)
		return accepted, fmt.Errorf("request accepted but execution was not requested: %w", err)
	}
	started, _ := s.GetTask(task.ID)
	return started, nil
}

// requestTitle derives a display title from the first line of a request. The
// mandate remains the complete request text.
func requestTitle(text string) string {
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return "Untitled request"
	}
	const limit = 60
	runes := []rune(line)
	if len(runes) > limit {
		line = string(runes[:limit-3]) + "..."
	}
	return line
}
