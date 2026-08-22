package domain

import "context"

// JiraGuardedCreatePreparationRequest is the typed, already-qualified input to
// Jira's single create-payload owner. DescriptionPresent preserves the
// distinction between an omitted description and an explicitly supplied empty
// native-wiki document.
type JiraGuardedCreatePreparationRequest struct {
	ProjectKey         string
	IssueTypeID        string
	Summary            string
	Description        []byte
	DescriptionPresent bool
	Fields             map[string]JiraFieldInput
}

// JiraGuardedCreatePreparedField is a content-free projection of one value in
// the exact serialized request. Value bytes stay only in Payload.
type JiraGuardedCreatePreparedField struct {
	FieldID   string
	InputKind string
	JSONKind  string
	SHA256    string
	Bytes     int
}

// JiraGuardedCreatePreparation is immutable adapter-owned wire evidence. The
// app hashes Payload and the comparison projection, then passes Payload back
// unchanged to the strict writer. Callers must clone it before retention.
type JiraGuardedCreatePreparation struct {
	Payload []byte
	Fields  []JiraGuardedCreatePreparedField
}

// JiraGuardedCreatePreparer is separate from Tracker so compatibility callers
// retain the legacy Create method while guarded workflows share its exact wire
// normalization.
type JiraGuardedCreatePreparer interface {
	PrepareGuardedCreate(JiraGuardedCreatePreparationRequest) (JiraGuardedCreatePreparation, error)
}

type JiraGuardedCreateWrite struct {
	Payload    []byte
	ProjectID  string
	ProjectKey string
}

type JiraGuardedCreateAcknowledgement struct {
	ID  string `json:"id"`
	Key string `json:"key,omitempty"`
}

// JiraGuardedCreateReadRequest asks for exactly one immutable-id readback and
// a bounded, deduplicated field projection.
type JiraGuardedCreateReadRequest struct {
	ID     string
	Fields []string
}

type JiraGuardedCreateFieldEvidence struct {
	Present bool
	Value   any
}

// JiraGuardedCreateReadback contains only the presence-qualified evidence the
// guarded app needs. Dynamic values use json.Number where the backend returned
// a JSON number.
type JiraGuardedCreateReadback struct {
	ID          string
	Key         string
	ProjectID   string
	ProjectKey  string
	IssueTypeID string
	Summary     string
	Description JiraGuardedCreateFieldEvidence
	Created     JiraGuardedCreateFieldEvidence
	Updated     JiraGuardedCreateFieldEvidence
	Fields      map[string]JiraGuardedCreateFieldEvidence
}

// JiraGuardedCreatePort deliberately excludes broad Tracker.Create and
// Tracker.GetIssue: a guarded create writes prepared bytes once and proves the
// result by immutable id once.
type JiraGuardedCreatePort interface {
	JiraGuardedCreatePreparer
	WriteGuardedCreate(context.Context, JiraGuardedCreateWrite) (JiraGuardedCreateAcknowledgement, error)
	ReadGuardedCreate(context.Context, JiraGuardedCreateReadRequest) (JiraGuardedCreateReadback, error)
}
