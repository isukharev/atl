package domain

import "context"

// BrokerJiraProjectPageReader is the optional semantic client port for the
// fixed execution-v2 Jira project-page operation. Direct Jira composition does
// not implement or emulate this capability.
type BrokerJiraProjectPageReader interface {
	ReadJiraProjectIssuePage(context.Context, BrokerProjectPageArguments) (BrokerJiraProjectPageResultV2, error)
}
