package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// LocalPolicyID identifies the authorization policy for the trusted local
	// source. Its version remains local-1 because acceptance behavior is unchanged.
	LocalPolicyID = "local"
	// SubagentPolicyID identifies the separate policy for delegated child work.
	SubagentPolicyID = "subagent"

	// LocalPolicySource and SubagentPolicySource are the stable source names used
	// by authorization requests.
	LocalPolicySource    = "manual"
	SubagentPolicySource = "subagent"

	// LocalActor identifies the trusted local operator. It is audit metadata, not
	// an authentication mechanism.
	LocalActor = "local-user"

	// Request purposes distinguish the UI entry points without changing policy.
	PurposeRouter   = "router"
	PurposeManual   = "manual"
	PurposeRelated  = "related"
	PurposeSubagent = "subagent"

	// Authorization outcomes. Only accept currently creates a task.
	OutcomeAccept      = "accept"
	OutcomeReject      = "reject"
	OutcomeNeedsReview = "needs_review"

	authorizationDigestSchema = "orchestrator.authorization/v1"
)

// AuthorizationRequest is the complete input available to an authorization
// policy. Text is the proposed mandate, never later context. Workspace is the
// normalized execution source, and ParentID/Depth identify delegated work.
type AuthorizationRequest struct {
	Source    string
	Actor     string
	Purpose   string
	TriggerID string
	ThreadID  string
	ParentID  string
	Text      string
	Workspace string
	Depth     int
}

// AuthorizationResult is a policy's answer. The orchestrator records the
// result together with the input digest; policies do not write state directly.
type AuthorizationResult struct {
	Outcome       string
	Reason        string
	PolicyID      string
	PolicyVersion string
}

// Policy is the authorization gate for one trusted source. Implementations must
// be deterministic and must not derive authority from model or agent output.
type Policy interface {
	ID() string
	Version() string
	Authorize(AuthorizationRequest) AuthorizationResult
}

type localPolicy struct{}

func (localPolicy) ID() string      { return LocalPolicyID }
func (localPolicy) Version() string { return LocalPolicyVersion }

func (localPolicy) Authorize(req AuthorizationRequest) AuthorizationResult {
	result := AuthorizationResult{Outcome: OutcomeReject, PolicyID: LocalPolicyID, PolicyVersion: LocalPolicyVersion}
	if req.Source != LocalPolicySource {
		result.Reason = "local policy only accepts the trusted local source"
		return result
	}
	if strings.TrimSpace(req.Text) == "" {
		result.Reason = "local request has no mandate"
		return result
	}
	result.Outcome = OutcomeAccept
	switch req.Purpose {
	case PurposeRouter:
		result.Reason = "trusted local request accepted by the deterministic router"
	case PurposeRelated:
		result.Reason = "explicit local request in existing thread"
	default:
		result.Reason = "explicit local request"
	}
	return result
}

type subagentPolicy struct{}

func (subagentPolicy) ID() string      { return SubagentPolicyID }
func (subagentPolicy) Version() string { return SubagentPolicyVersion }

func (subagentPolicy) Authorize(req AuthorizationRequest) AuthorizationResult {
	result := AuthorizationResult{Outcome: OutcomeReject, PolicyID: SubagentPolicyID, PolicyVersion: SubagentPolicyVersion}
	if req.Source != SubagentPolicySource || req.ParentID == "" || req.Depth <= 0 {
		result.Reason = "subagent policy requires a delegated request with a parent task"
		return result
	}
	if strings.TrimSpace(req.Text) == "" {
		result.Reason = "subagent request has no mandate"
		return result
	}
	result.Outcome = OutcomeAccept
	result.Reason = "delegated by running task " + req.ParentID + "; child has a separate mandate"
	return result
}

// RegisterPolicy replaces the authorization policy for a source. It is an
// extension point for future sources; the built-in local and subagent policies
// are registered when the service opens.
func (s *Service) RegisterPolicy(source string, policy Policy) error {
	if source == "" || policy == nil {
		return errors.New("policy source and implementation are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	if s.policies == nil {
		s.policies = map[string]Policy{}
	}
	s.policies[source] = policy
	return nil
}

func (s *Service) authorize(req AuthorizationRequest) (Decision, error) {
	s.mu.Lock()
	policy := s.policies[req.Source]
	s.mu.Unlock()
	return authorizeWithPolicy(policy, req)
}

// authorizeLocked is for callers that already hold s.mu.
func (s *Service) authorizeLocked(req AuthorizationRequest) (Decision, error) {
	return authorizeWithPolicy(s.policies[req.Source], req)
}

func authorizeWithPolicy(policy Policy, req AuthorizationRequest) (Decision, error) {
	if policy == nil {
		return Decision{}, fmt.Errorf("no authorization policy for source %q", req.Source)
	}
	result := policy.Authorize(req)
	if result.Outcome == "" {
		result.Outcome = OutcomeReject
		result.Reason = "authorization policy returned no outcome"
	}
	if result.PolicyID == "" {
		result.PolicyID = policy.ID()
	}
	if result.PolicyVersion == "" {
		result.PolicyVersion = policy.Version()
	}
	decision := Decision{
		TriggerID:     req.TriggerID,
		Outcome:       result.Outcome,
		Reason:        result.Reason,
		PolicyID:      result.PolicyID,
		PolicyVersion: result.PolicyVersion,
		Source:        req.Source,
		Actor:         req.Actor,
		InputDigest:   authorizationInputDigest(req),
		CreatedAt:     time.Now().UTC(),
	}
	if result.Outcome != OutcomeAccept {
		if result.Outcome == OutcomeNeedsReview {
			return decision, fmt.Errorf("request requires review by policy %s: %s", result.PolicyVersion, result.Reason)
		}
		return decision, fmt.Errorf("request rejected by policy %s: %s", result.PolicyVersion, result.Reason)
	}
	return decision, nil
}

// authorizationInputDigest is a deterministic digest of the exact policy input.
// The canonical JSON schema is versioned so a future change cannot silently
// reinterpret an older decision.
func authorizationInputDigest(req AuthorizationRequest) string {
	payload := struct {
		Schema    string `json:"schema"`
		Source    string `json:"source"`
		Actor     string `json:"actor"`
		Purpose   string `json:"purpose"`
		TriggerID string `json:"trigger_id"`
		ThreadID  string `json:"thread_id"`
		ParentID  string `json:"parent_id"`
		Text      string `json:"text"`
		Workspace string `json:"workspace"`
		Depth     int    `json:"depth"`
	}{
		Schema:    authorizationDigestSchema,
		Source:    req.Source,
		Actor:     req.Actor,
		Purpose:   req.Purpose,
		TriggerID: req.TriggerID,
		ThreadID:  req.ThreadID,
		ParentID:  req.ParentID,
		Text:      req.Text,
		Workspace: req.Workspace,
		Depth:     req.Depth,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
