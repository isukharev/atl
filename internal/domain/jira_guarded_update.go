package domain

import (
	"context"
	"strings"
)

const (
	JiraGuardedUpdateMaxFields               = 1024
	JiraGuardedUpdateMaxCatalogEntries       = 4096
	JiraGuardedUpdateMaxFieldIDBytes         = 1024
	JiraGuardedUpdateMaxRequestedKeyBytes    = 64
	JiraGuardedUpdateMaxImmutableIDBytes     = 64
	JiraGuardedUpdateMaxInputBytes           = int64(64 << 20)
	JiraGuardedUpdateMaxDesiredBytes         = int64(64 << 20)
	JiraGuardedUpdateMaxCurrentBytes         = int64(64 << 20)
	JiraGuardedUpdateMaxPreparedBytes        = int64(64 << 20)
	JiraGuardedUpdateMaxCatalogResponseBytes = int64(16 << 20)
	JiraGuardedUpdateMaxIssueResponseBytes   = int64(64 << 20)
	JiraGuardedUpdateMaxWriteResponseBytes   = int64(1 << 20)
	JiraGuardedUpdateMaxQueryAndPathBytes    = 64 << 10
	JiraGuardedUpdateDeadlineMillis          = int64(60_000)
	JiraGuardedUpdatePreviewRequests         = 2
	JiraGuardedUpdateApplyRequests           = 6
	JiraGuardedUpdatePreviewResponseBytes    = int64(80 << 20)
	JiraGuardedUpdateApplyResponseBytes      = int64(225 << 20)
)

type JiraGuardedUpdatePreparedField struct {
	FieldID   string
	InputKind string
	JSONKind  string
	Bytes     int
	SHA256    string
}

type JiraGuardedUpdatePreparationRequest struct {
	Summary            string
	SummaryPresent     bool
	Description        []byte
	DescriptionPresent bool
	DescriptionSource  string
	Fields             map[string]JiraFieldInput
	Qualified          []JiraGuardedFieldCatalogEntry
}

// JiraGuardedUpdatePreparation retains the exact canonical payload and a
// private typed value copy for application-level exact readback comparison.
// Public results project only kinds, lengths, and digests.
type JiraGuardedUpdatePreparation struct {
	Payload []byte
	Fields  []JiraGuardedUpdatePreparedField
	Values  map[string]any
}

type JiraGuardedUpdateWrite struct {
	ID        string
	Key       string
	Project   string
	Qualified []JiraGuardedFieldCatalogEntry
	Prepared  JiraGuardedUpdatePreparation
}

// JiraGuardedUpdatePort owns the strict whole-issue update boundary. Reads are
// bounded and the sole writer accepts only an immutable numeric id plus exact
// adapter-prepared bytes.
type JiraGuardedUpdatePort interface {
	ReadGuardedUpdateFieldCatalog(context.Context, []string) (JiraGuardedFieldCatalog, error)
	ReadGuardedUpdateIssue(context.Context, string, []string) (JiraGuardedFieldIssue, error)
	PrepareGuardedUpdate(JiraGuardedUpdatePreparationRequest) (JiraGuardedUpdatePreparation, error)
	WriteGuardedUpdate(context.Context, JiraGuardedUpdateWrite) error
}

// ValidJiraGuardedUpdateCandidateField accepts an exact candidate identifier
// that can still be proved custom by the bounded catalog. Known system fields
// and fields with dedicated mutation workflows fail before backend access.
func ValidJiraGuardedUpdateCandidateField(identifier string) bool {
	if !ValidJiraGuardedFieldID(identifier) || JiraGuardedFieldReserved(identifier) {
		return false
	}
	return !ValidJiraTechnicalFieldID(identifier) || strings.HasPrefix(identifier, "customfield_")
}
