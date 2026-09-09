package domain

import "context"

// BrokerJiraCommentClient exposes only the fixed guarded-comment preview and
// ticket-bound apply operations. Direct Jira adapters do not implement or
// emulate this capability.
type BrokerJiraCommentClient interface {
	PreviewJiraComment(context.Context, string, []byte) (BrokerJiraCommentResult, error)
	ApplyJiraComment(context.Context, string, []byte, string, string) (BrokerJiraCommentResult, error)
}

// BrokerOperationOutcomeClient observes durable Broker metadata for one
// opaque ticket. It cannot replay an operation or read the backend.
type BrokerOperationOutcomeClient interface {
	ObserveBrokerOperation(context.Context, string) (BrokerOperationOutcome, error)
}
