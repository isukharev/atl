package contentpolicy

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

type DenialReason string

const (
	ReasonNoMatchingAllow        DenialReason = "no_matching_allow"
	ReasonExplicitDeny           DenialReason = "explicit_deny"
	ReasonScopeUnresolved        DenialReason = "scope_unresolved"
	ReasonScopeUnavailable       DenialReason = "scope_unavailable"
	ReasonScopeContradiction     DenialReason = "scope_contradiction"
	ReasonProtectedSubtree       DenialReason = "protected_subtree_detached"
	ReasonContainedContentDenied DenialReason = "contained_content_denied"
	ReasonPolicyRequiredAbsent   DenialReason = "policy_required_but_absent"
	ReasonPolicyDigestMismatch   DenialReason = "policy_digest_mismatch"
	ReasonBackendMismatch        DenialReason = "backend_mismatch"
	reasonInvalidRequest         DenialReason = "invalid_request"
)

type Advice string

const (
	AdviceNoRetry       Advice = "no_retry"
	AdviceOutOfScope    Advice = "out_of_scope"
	AdviceNarrowScope   Advice = "narrow_scope"
	AdviceWaitThenRetry Advice = "wait_then_retry"
)

// DenialError is intentionally data-oriented so CLI diagnostics can expose a
// stable public envelope without parsing its human message.
type DenialError struct {
	Reason    DenialReason  `json:"-"`
	RuleID    string        `json:"-"`
	Layer     string        `json:"-"`
	Attribute string        `json:"-"`
	Advice    Advice        `json:"-"`
	RetrySafe bool          `json:"-"`
	Message   string        `json:"-"`
	Details   DenialDetails `json:"-"`
}

type DenialDetails struct {
	SchemaVersion    int                 `json:"schema_version"`
	Phase            string              `json:"phase"`
	Verbs            domain.WriteVerbSet `json:"verbs"`
	Target           DenialTarget        `json:"target"`
	DecidedBy        DenialDecision      `json:"decided_by"`
	Reason           DenialReason        `json:"reason"`
	AllowedVerbsHere domain.WriteVerbSet `json:"allowed_verbs_here"`
	Advice           Advice              `json:"advice"`
	PolicyDigest     DenialPolicyDigest  `json:"policy_digest"`
	PolicySource     string              `json:"policy_source"`
	Attribute        string              `json:"attribute,omitempty"`
	RetrySafe        bool                `json:"retry_safe"`
}

type DenialTarget struct {
	Service     string   `json:"service"`
	Kind        string   `json:"kind"`
	ID          string   `json:"id,omitempty"`
	Project     string   `json:"project,omitempty"`
	Key         string   `json:"key,omitempty"`
	Space       string   `json:"space,omitempty"`
	AncestorIDs []string `json:"ancestor_ids,omitempty"`
}

type DenialDecision struct {
	Layer  string  `json:"layer"`
	RuleID *string `json:"rule_id"`
	Effect string  `json:"effect"`
}

type DenialPolicyDigest struct {
	Managed *string `json:"managed"`
	User    *string `json:"user"`
}

// NewSourceDenial builds a stable denial for process-policy failures that do
// not describe one backend resource (required policy, digest, or binding).
func NewSourceDenial(reason DenialReason, message, source string, resolved *Resolved) *DenialError {
	details := DenialDetails{
		SchemaVersion:    1,
		Phase:            "resolved",
		Verbs:            make(domain.WriteVerbSet, 0),
		Target:           DenialTarget{},
		DecidedBy:        DenialDecision{Layer: policyLayerName(source), Effect: "source_error"},
		Reason:           reason,
		AllowedVerbsHere: make(domain.WriteVerbSet, 0),
		Advice:           AdviceNoRetry,
		PolicySource:     source,
		RetrySafe:        false,
	}
	if resolved != nil {
		for _, layer := range resolved.Layers {
			digest := layer.Digest
			switch layer.Source {
			case "env_inline", "env_file":
				details.PolicyDigest.Managed = &digest
			case "config_dir":
				details.PolicyDigest.User = &digest
			}
		}
	}
	return &DenialError{Reason: reason, Advice: AdviceNoRetry, Message: message, Details: details}
}

func (e *DenialError) Error() string {
	if e == nil {
		return "write denied by local policy"
	}
	if e.Message != "" {
		return e.Message
	}
	verb := "write"
	if len(e.Details.Verbs) > 0 {
		verb = string(e.Details.Verbs[0])
	}
	target := denialTargetLabel(e.Details.Target)
	if e.Reason == ReasonNoMatchingAllow {
		return fmt.Sprintf("content policy denies %q on %s: no rule grants it here", verb, target)
	}
	if e.RuleID != "" {
		return fmt.Sprintf("content policy denies %q on %s: rule %q decided %s", verb, target, e.RuleID, e.Reason)
	}
	return fmt.Sprintf("content policy denies %q on %s: %s", verb, target, e.Reason)
}

func (e *DenialError) Unwrap() error { return domain.ErrCheckFailed }

func (e *DenialError) DiagnosticPolicyDenial() bool { return e != nil }

func (e *DenialError) DiagnosticWriteAttempted() bool { return false }

func (e *DenialError) DiagnosticPolicyDenialDetails() any {
	if e == nil {
		return nil
	}
	return e.Details
}

func denialFromDecision(decision Decision, request domain.WriteAuthorizationRequest, layers []Layer) *DenialError {
	advice := AdviceOutOfScope
	retrySafe := false
	if decision.Reason == ReasonScopeUnresolved || decision.Reason == ReasonScopeContradiction {
		advice = AdviceNoRetry
	}
	if decision.Reason == ReasonProtectedSubtree || decision.Reason == ReasonContainedContentDenied {
		advice = AdviceNarrowScope
	}
	if decision.Reason == ReasonScopeUnavailable {
		advice = AdviceWaitThenRetry
		retrySafe = true
	}
	details := DenialDetails{
		SchemaVersion: 1, Phase: "resolved", Verbs: append(domain.WriteVerbSet(nil), request.Verbs...),
		Reason: decision.Reason, Advice: advice, Attribute: decision.Attribute, RetrySafe: retrySafe,
		DecidedBy:        DenialDecision{Layer: policyLayerName(decision.Layer), Effect: "default_deny"},
		PolicySource:     decision.Layer,
		AllowedVerbsHere: make(domain.WriteVerbSet, 0),
	}
	if details.DecidedBy.Layer == "" && len(layers) > 0 {
		details.DecidedBy.Layer = policyLayerName(layers[0].Source)
		details.PolicySource = layers[0].Source
		details.DecidedBy.Effect = "scope_error"
	}
	if decision.Target >= 0 && decision.Target < len(request.Targets) {
		target := request.Targets[decision.Target]
		details.Target = DenialTarget{
			Service: target.Service, Kind: target.Kind, ID: target.ID,
			Project: target.Project, Key: target.Key, Space: target.Space,
			AncestorIDs: append([]string(nil), target.AncestorIDs...),
		}
	}
	if decision.RuleID != "" {
		ruleID := decision.RuleID
		details.DecidedBy.RuleID = &ruleID
		details.DecidedBy.Effect = "deny"
	}
	for _, layer := range layers {
		digest := layer.Digest
		switch layer.Source {
		case "config_dir":
			details.PolicyDigest.User = &digest
		case "env_inline", "env_file":
			details.PolicyDigest.Managed = &digest
		}
	}
	if request.ScopeProblem == domain.WriteScopeResolved && decision.Target >= 0 && decision.Target < len(request.Targets) {
		details.AllowedVerbsHere = allowedVerbsForTarget(layers, request.Targets[decision.Target])
	}
	return &DenialError{
		Reason: decision.Reason, RuleID: decision.RuleID, Layer: decision.Layer,
		Attribute: decision.Attribute, Advice: advice, RetrySafe: retrySafe, Details: details,
	}
}

func policyLayerName(source string) string {
	switch source {
	case "env_inline", "env_file":
		return "managed"
	case "config_dir":
		return "user"
	default:
		return source
	}
}

func allowedVerbsForTarget(layers []Layer, target domain.WriteTarget) domain.WriteVerbSet {
	all := domain.WriteVerbSet{
		domain.WriteVerbCreate, domain.WriteVerbUpdate, domain.WriteVerbComment,
		domain.WriteVerbTransition, domain.WriteVerbMove, domain.WriteVerbDelete,
	}
	allowed := make(domain.WriteVerbSet, 0, len(all))
	for _, verb := range all {
		if Decide(layers, domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{verb}, Targets: []domain.WriteTarget{target}}).Allowed {
			allowed = append(allowed, verb)
		}
	}
	return allowed
}

func denialTargetLabel(target DenialTarget) string {
	parts := []string{target.Service, target.Kind}
	for _, value := range []string{target.Key, target.ID, target.Project, target.Space} {
		if value != "" {
			parts = append(parts, value)
			break
		}
	}
	label := strings.TrimSpace(strings.Join(parts, " "))
	if label == "" {
		return "write target"
	}
	return label
}
