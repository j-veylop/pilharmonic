package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// LocalPolicyVersion identifies the deterministic routing policy used for the
// trusted local source. Model-assisted evaluation is deliberately not
// implemented: a local submission is an explicit request, so acceptance is
// deterministic and auditable.
const LocalPolicyVersion = "local-1"

// DefaultRouteAgent is the executor assigned by the local routing policy.
const DefaultRouteAgent = "general"

// SplitAgentMention recognizes an explicit executor choice at the start of a
// request. The leading @name token is routing metadata, not part of the
// mandate: the returned request excludes it. The name is matched exactly
// against registered executors by the caller.
func SplitAgentMention(text string) (agent, request string, explicit bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "@") {
		return "", trimmed, false
	}
	rest := trimmed[1:]
	end := strings.IndexFunc(rest, unicode.IsSpace)
	if end < 0 {
		return rest, "", true
	}
	return rest[:end], strings.TrimSpace(rest[end:]), true
}

// routeTopLevelRequest resolves an explicit @executor mention or assigns the
// general fallback. An unavailable or incomplete mention is rejected rather
// than silently routed elsewhere.
func (s *Service) routeTopLevelRequest(text string) (string, string, error) {
	text = strings.TrimSpace(text)
	agent, request, explicit := SplitAgentMention(text)
	if !explicit {
		return DefaultRouteAgent, text, nil
	}
	if agent == "" {
		return "", "", errors.New("executor name is required after @")
	}
	if request == "" {
		return "", "", errors.New("request text is required after the executor mention")
	}
	s.mu.Lock()
	_, available := s.runners[agent]
	s.mu.Unlock()
	if !available {
		return "", "", fmt.Errorf("agent %q unavailable", agent)
	}
	return agent, request, nil
}

// SubmitRequest is the unified entry point for local work. It records one
// explicit request, accepts it under the deterministic local policy, records
// the assignment, and requests execution. Acceptance, assignment and start
// remain separate durable records.
func (s *Service) SubmitRequest(text, workspace string) (Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Task{}, errors.New("request text is required")
	}
	task, err := s.acceptLocal(PurposeRouter, "", "", text, workspace, "")
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

// DerivedTitle returns the deterministic display title for a request, with any
// leading executor mention removed. The service uses the same value as the
// immediate title and as the fallback when a generated title is unavailable.
func DerivedTitle(text string) string {
	if _, request, explicit := SplitAgentMention(text); explicit {
		text = request
	}
	return requestTitle(text)
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
