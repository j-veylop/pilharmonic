package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// LocalPolicyID identifies the authorization record written for the trusted
	// local source. Its version remains local-1 because acceptance behavior is
	// unchanged.
	LocalPolicyID = "local"
	// SubagentPolicyID identifies the separate authorization record written for
	// delegated child work.
	SubagentPolicyID = "subagent"

	// LocalPolicySource and SubagentPolicySource are the stable source names used
	// by authorization requests.
	LocalPolicySource    = "manual"
	SubagentPolicySource = "subagent"

	// LocalActor identifies the trusted local operator. It is audit metadata, not
	// an authentication mechanism.
	LocalActor = "local-user"

	// Request purposes distinguish the UI entry points without changing
	// authorization.
	PurposeRouter   = "router"
	PurposeManual   = "manual"
	PurposeRelated  = "related"
	PurposeSubagent = "subagent"

	// Authorization outcomes. Only accept currently creates a task.
	OutcomeAccept = "accept"
	OutcomeReject = "reject"

	authorizationDigestSchema = "orchestrator.authorization/v1"
)

// AuthorizationRequest is the complete input available to authorization.
// Text is the proposed mandate, never later context. Workspace is the
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

// authorize is the single compiled-in authorization step. It is deterministic:
// it derives its answer only from the request and never from model or agent
// output, so the same input always produces the same decision. An accepted
// request records its mandate identity, source, actor and input digest; a
// rejected request creates no task and is reported to the caller.
func (s *Service) authorize(req AuthorizationRequest) (Decision, error) {
	decision := Decision{
		TriggerID:   req.TriggerID,
		Source:      req.Source,
		Actor:       req.Actor,
		InputDigest: authorizationInputDigest(req),
		CreatedAt:   time.Now().UTC(),
	}
	var reason string
	switch req.Source {
	case LocalPolicySource:
		decision.PolicyID, decision.PolicyVersion = LocalPolicyID, LocalPolicyVersion
		if strings.TrimSpace(req.Text) == "" {
			reason = "local request has no mandate"
			break
		}
		decision.Outcome = OutcomeAccept
		switch req.Purpose {
		case PurposeRouter:
			decision.Reason = "trusted local request accepted by the deterministic router"
		case PurposeRelated:
			decision.Reason = "explicit local request in existing thread"
		default:
			decision.Reason = "explicit local request"
		}
		return decision, nil
	case SubagentPolicySource:
		decision.PolicyID, decision.PolicyVersion = SubagentPolicyID, SubagentPolicyVersion
		if req.ParentID == "" || req.Depth <= 0 {
			reason = "subagent policy requires a delegated request with a parent task"
			break
		}
		if strings.TrimSpace(req.Text) == "" {
			reason = "subagent request has no mandate"
			break
		}
		decision.Outcome = OutcomeAccept
		decision.Reason = "delegated by running task " + req.ParentID + "; child has a separate mandate"
		return decision, nil
	default:
		// An unknown source is never authorized. The two supported sources are
		// handled above, so there is nothing further to consult.
		decision.Outcome = OutcomeReject
		decision.Reason = fmt.Sprintf("unknown authorization source %q", req.Source)
		return decision, fmt.Errorf("unknown authorization source %q", req.Source)
	}
	decision.Outcome = OutcomeReject
	decision.Reason = reason
	return decision, fmt.Errorf("request rejected by policy %s: %s", decision.PolicyVersion, reason)
}

// authorizationInputDigest is a deterministic digest of the exact authorization
// input. The canonical JSON schema is versioned so a future change cannot
// silently reinterpret an older decision.
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
