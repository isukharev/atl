package domain

import "context"

const BrokerReadConsistencyIdentitySnapshotV1 = "identity_snapshot_v1"

type BrokerClientReadPurpose string

const (
	BrokerClientReadJiraIssue      BrokerClientReadPurpose = "jira_issue"
	BrokerClientReadConfluencePage BrokerClientReadPurpose = "confluence_page"
	BrokerClientReadConfluenceMeta BrokerClientReadPurpose = "confluence_metadata"
)

type brokerClientReadPurposeKey struct{}

// WithBrokerClientReadPurpose marks an application-owned use case admitted by
// the remote Broker client. Direct adapters ignore this marker.
func WithBrokerClientReadPurpose(ctx context.Context, purpose BrokerClientReadPurpose) context.Context {
	return context.WithValue(ctx, brokerClientReadPurposeKey{}, purpose)
}

func BrokerClientReadPurposeFromContext(ctx context.Context) BrokerClientReadPurpose {
	purpose, _ := ctx.Value(brokerClientReadPurposeKey{}).(BrokerClientReadPurpose)
	return purpose
}

// BrokerJiraIssueIdentity is the content-free qualification projection for one
// exact issue. Complete is required before it can enter final authorization.
type BrokerJiraIssueIdentity struct {
	ID       string
	Key      string
	Project  string
	Updated  string
	Complete bool
}

type BrokerJiraIssueSnapshot struct {
	Identity BrokerJiraIssueIdentity
	Fields   []BrokerJiraIssueReadField
	Complete bool
}

type BrokerConfluencePageIdentity struct {
	ID               string
	Type             string
	Status           string
	Space            string
	Version          int
	Updated          string
	AncestorIDs      []string
	AncestorsPresent bool
	Complete         bool
}

type BrokerConfluencePageSnapshot struct {
	Identity       BrokerConfluencePageIdentity
	Title          string
	Projection     BrokerConfluenceProjection
	Storage        []byte
	StoragePresent bool
	Complete       bool
}

// BrokerJiraIssueReadPort exposes only the two reads admitted by the exact-read
// operation. Implementations resolve their destination from server-owned
// composition and must honor the single-attempt budget carried by ctx.
type BrokerJiraIssueReadPort interface {
	BrokerOriginSHA256() (string, error)
	QualifyBrokerIssue(context.Context, string) (BrokerJiraIssueIdentity, error)
	ReadBrokerIssue(context.Context, string, []BrokerJiraIssueField) (BrokerJiraIssueSnapshot, error)
}

type BrokerConfluencePageReadPort interface {
	BrokerOriginSHA256() (string, error)
	QualifyBrokerPage(context.Context, string) (BrokerConfluencePageIdentity, error)
	ReadBrokerPage(context.Context, string, BrokerConfluenceProjection) (BrokerConfluencePageSnapshot, error)
}
