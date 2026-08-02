package domain

import "context"

// JiraDevelopmentProject is one content-minimized GitLab project identity
// observed through Jira's experimental Development surface.
type JiraDevelopmentProject struct {
	Host        string
	ProjectPath string
}

// JiraDevelopmentCommit is one exact GitLab commit identity.
type JiraDevelopmentCommit struct {
	Host        string
	ProjectPath string
	SHA         string
}

// JiraDevelopmentBranch is one exact GitLab branch identity.
type JiraDevelopmentBranch struct {
	Host        string
	ProjectPath string
	Name        string
}

// JiraDevelopmentMergeRequest is one project-local GitLab merge-request
// identity with a closed normalized state.
type JiraDevelopmentMergeRequest struct {
	Host        string
	ProjectPath string
	IID         string
	State       string
}

// JiraDevelopmentInventory is returned only after the adapter has reconciled
// every requested selector. It deliberately contains no raw plugin envelope,
// narrative, people, timestamps, files, or downstream credentials.
type JiraDevelopmentInventory struct {
	Projects      []JiraDevelopmentProject
	Commits       []JiraDevelopmentCommit
	Branches      []JiraDevelopmentBranch
	MergeRequests []JiraDevelopmentMergeRequest
}

// JiraDevelopmentReader is the narrow experimental read capability used by
// opt-in Jira graph collection. numericIssueID is the already-qualified id from
// the stable issue snapshot; implementations must not re-read the issue.
type JiraDevelopmentReader interface {
	ReadIssueDevelopment(ctx context.Context, numericIssueID string) (JiraDevelopmentInventory, error)
}
