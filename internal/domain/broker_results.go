package domain

type BrokerJiraIssueReadField struct {
	Field   BrokerJiraIssueField
	Present bool
	Null    bool
	Value   string
}

type BrokerJiraIssueReadResult struct {
	SchemaVersion   int
	ArgumentsSHA256 string
	IssueID         string
	Key             string
	Project         string
	Updated         string
	Fields          []BrokerJiraIssueReadField
	Complete        bool
}

type BrokerConfluencePageReadResult struct {
	SchemaVersion   int
	ArgumentsSHA256 string
	PageID          string
	Type            string
	Space           string
	Version         int
	Title           string
	Updated         string
	Projection      BrokerConfluenceProjection
	Storage         []byte
	StoragePresent  bool
	Complete        bool
}

type BrokerJiraCommentResult struct {
	SchemaVersion         int
	ArgumentsSHA256       string
	OperationTicket       string
	Mode                  string
	Status                string
	ProposalHash          string
	NativeCandidateSHA256 string
	VersionEvidenceSHA256 string
	CommentID             string
	WriteAttempted        bool
	Complete              bool
	Reconciled            bool
}
