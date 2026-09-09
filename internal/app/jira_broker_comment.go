package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

// JiraBrokerCommentResult is the content-minimized public projection of the
// closed Broker result. It is deliberately distinct from JiraCommentAddResult,
// whose fields describe direct-backend qualification and request accounting.
type JiraBrokerCommentResult struct {
	SchemaVersion         int    `json:"schema_version"`
	Operation             string `json:"operation"`
	QualificationProfile  string `json:"qualification_profile"`
	ArgumentsSHA256       string `json:"arguments_sha256"`
	OperationTicket       string `json:"operation_ticket"`
	Mode                  string `json:"mode"`
	Status                string `json:"status"`
	ProposalHash          string `json:"proposal_hash"`
	NativeCandidateSHA256 string `json:"native_candidate_sha256"`
	VersionEvidenceSHA256 string `json:"version_evidence_sha256"`
	CommentID             string `json:"comment_id,omitempty"`
	WriteAttempted        bool   `json:"write_attempted"`
	Complete              bool   `json:"complete"`
	Reconciled            bool   `json:"reconciled"`
}

func (s *JiraService) AddBrokerCommentGuarded(ctx context.Context, requestedKey string, opts JiraCommentAddOpts, operationTicket string) (*JiraBrokerCommentResult, error) {
	if s == nil || s.brokerComments == nil {
		return nil, fmt.Errorf("%w: Broker Jira comments are not configured", domain.ErrConfig)
	}
	key, err := ValidateJiraGuardedCommentKey(requestedKey)
	if err != nil {
		return nil, err
	}
	opts, err = normalizeJiraCommentAddOpts(opts)
	if err != nil {
		return nil, err
	}
	if opts.SatisfactionPolicy != jiraCommentSatisfactionAppendAlways {
		return nil, fmt.Errorf("%w: Broker Jira comments require append_always", domain.ErrUsage)
	}
	var result domain.BrokerJiraCommentResult
	if opts.Apply {
		if operationTicket == "" {
			return nil, fmt.Errorf("%w: --operation-ticket is required with --apply in Broker mode; run the Broker preview first", domain.ErrUsage)
		}
		result, err = s.brokerComments.ApplyJiraComment(ctx, key, opts.Body, opts.ExpectedProposalHash, operationTicket)
	} else {
		if operationTicket != "" {
			return nil, fmt.Errorf("%w: --operation-ticket requires --apply", domain.ErrUsage)
		}
		result, err = s.brokerComments.PreviewJiraComment(ctx, key, opts.Body)
	}
	if err != nil {
		return nil, err
	}
	projected := jiraBrokerCommentResult(result)
	switch result.Status {
	case "proposed", "applied", "recovered":
		return projected, nil
	case "not_applied":
		return projected, &brokerCommentTerminalError{status: result.Status}
	case "outcome_unknown":
		return projected, &brokerCommentTerminalError{status: result.Status}
	default:
		return nil, fmt.Errorf("%w: Broker returned an unsupported Jira comment status", domain.ErrCheckFailed)
	}
}

func jiraBrokerCommentResult(value domain.BrokerJiraCommentResult) *JiraBrokerCommentResult {
	return &JiraBrokerCommentResult{
		SchemaVersion: value.SchemaVersion, Operation: "jira.comment." + value.Mode,
		QualificationProfile: domain.BrokerJiraCommentQualificationProfileV1,
		ArgumentsSHA256:      value.ArgumentsSHA256, OperationTicket: value.OperationTicket,
		Mode: value.Mode, Status: value.Status, ProposalHash: value.ProposalHash,
		NativeCandidateSHA256: value.NativeCandidateSHA256, VersionEvidenceSHA256: value.VersionEvidenceSHA256,
		CommentID: value.CommentID, WriteAttempted: value.WriteAttempted, Complete: value.Complete, Reconciled: value.Reconciled,
	}
}

func JiraBrokerCommentText(result *JiraBrokerCommentResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("schema_version: %d\noperation: %s\nqualification_profile: %s\narguments_sha256: %s\noperation_ticket: %s\nmode: %s\nstatus: %s\nproposal_hash: %s\nnative_candidate_sha256: %s\nversion_evidence_sha256: %s\ncomment_id: %s\nwrite_attempted: %t\ncomplete: %t\nreconciled: %t",
		result.SchemaVersion, result.Operation, result.QualificationProfile, result.ArgumentsSHA256,
		result.OperationTicket, result.Mode, result.Status, result.ProposalHash,
		result.NativeCandidateSHA256, result.VersionEvidenceSHA256, result.CommentID,
		result.WriteAttempted, result.Complete, result.Reconciled)
}

type brokerCommentTerminalError struct{ status string }

func (e *brokerCommentTerminalError) Error() string {
	if e != nil && e.status == "outcome_unknown" {
		return "Broker Jira comment apply outcome is unknown; do not retry apply; run `atl jira issue comment outcome --operation-ticket <SAME-TICKET>` with the original operation ticket"
	}
	return "Broker Jira comment was not applied"
}
func (*brokerCommentTerminalError) Unwrap() error { return domain.ErrCheckFailed }
func (e *brokerCommentTerminalError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.status == "outcome_unknown"
}
func (e *brokerCommentTerminalError) DiagnosticTerminalCheckFailure() bool {
	return e != nil && (e.status == "not_applied" || e.status == "outcome_unknown")
}
