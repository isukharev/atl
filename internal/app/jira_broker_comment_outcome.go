package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

type JiraBrokerOperationOutcome struct {
	SchemaVersion        int                         `json:"schema_version"`
	Operation            string                      `json:"operation"`
	QualificationProfile string                      `json:"qualification_profile"`
	OperationTicket      string                      `json:"operation_ticket"`
	TicketSHA256         string                      `json:"ticket_sha256"`
	Phase                domain.BrokerOperationPhase `json:"phase"`
	ObservedAtMillis     int64                       `json:"observed_at_millis"`
	ResultSHA256         string                      `json:"result_sha256,omitempty"`
	Complete             bool                        `json:"complete"`
	Reconciled           bool                        `json:"reconciled"`
}

func (s *JiraService) ObserveBrokerCommentOperation(ctx context.Context, operationTicket string) (*JiraBrokerOperationOutcome, error) {
	if s == nil || s.brokerOutcomes == nil {
		return nil, fmt.Errorf("%w: jira_observation_session_file is required for Broker outcome observation", domain.ErrConfig)
	}
	if operationTicket == "" {
		return nil, fmt.Errorf("%w: --operation-ticket is required", domain.ErrUsage)
	}
	value, err := s.brokerOutcomes.ObserveBrokerOperation(ctx, operationTicket)
	if err != nil {
		return nil, err
	}
	return &JiraBrokerOperationOutcome{
		SchemaVersion: value.SchemaVersion, Operation: string(domain.BrokerOperationOutcomeLookup),
		QualificationProfile: "operation_ticket_v1", OperationTicket: operationTicket,
		TicketSHA256: value.TicketSHA256, Phase: value.Phase, ObservedAtMillis: value.ObservedAtMillis,
		ResultSHA256: value.ResultSHA256, Complete: value.Complete, Reconciled: value.Reconciled,
	}, nil
}

func JiraBrokerOperationOutcomeText(result *JiraBrokerOperationOutcome) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("schema_version: %d\noperation: %s\nqualification_profile: %s\noperation_ticket: %s\nticket_sha256: %s\nphase: %s\nobserved_at_millis: %d\nresult_sha256: %s\ncomplete: %t\nreconciled: %t",
		result.SchemaVersion, result.Operation, result.QualificationProfile, result.OperationTicket,
		result.TicketSHA256, result.Phase, result.ObservedAtMillis, result.ResultSHA256,
		result.Complete, result.Reconciled)
}
