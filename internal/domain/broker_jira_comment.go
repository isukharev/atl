package domain

import "context"

// BrokerJiraCommentQualificationProfileV1 is the sole canonical consistency
// profile selected by the v1 guarded-comment operation/version pair. It proves
// an immutable issue identity snapshot, not atomic current-project membership.
const BrokerJiraCommentQualificationProfileV1 = "exact_jira_comment_v1"

// BrokerJiraGuardedCommentPort exposes only the reads and single guarded write
// needed by the internal Broker comment coordinator. Implementations own one
// server-selected destination and credentials; request bytes cannot select it.
type BrokerJiraGuardedCommentPort interface {
	BrokerOriginSHA256() (string, error)
	QualifyBrokerIssue(context.Context, string) (BrokerJiraIssueIdentity, error)
	JiraGuardedCommentPort
}
