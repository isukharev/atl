package domain

import "context"

// BrokerJournalOwner is stable across observation executions. These hashes
// identify ownership, not authority; every consumer must authorize its action.
type BrokerJournalOwner struct {
	BrokerSHA256    string
	BackendSHA256   string
	PrincipalSHA256 string
	WorkloadSHA256  string
	AudienceSHA256  string
}

// BrokerJournalReservation fixes the original writer and hard deadlines.
// ArtifactCapacity reserves separate content-sensitive storage before issuance.
type BrokerJournalReservation struct {
	Owner                    BrokerJournalOwner
	ExecutionSHA256          string
	AuthorityRevisionSHA256  string
	ExecutionNotBeforeMillis int64
	ExecutionExpiresMillis   int64
	GrantExpiresMillis       int64
	CredentialExpiresMillis  int64
	OperationDeadlineMillis  int64
	ObservationUntilMillis   int64
	ArtifactCapacity         int64
}

// BrokerJournalIntent contains only immutable semantic bindings. ArgumentsSHA256
// in Ticket is the existing v1 arguments digest, including the opaque ticket ID;
// no transport request/correlation ID participates. TargetSHA256 names the
// backend-bound immutable issue and comment-dependent fence; callers use the
// same key across principals. EvidenceSHA256 binds qualified version evidence.
type BrokerJournalIntent struct {
	Ticket         BrokerOperationTicket
	NativeSHA256   string
	TargetSHA256   string
	EffectSHA256   string
	EvidenceSHA256 string
}

// BrokerJournalRecord is metadata only. An empty Phase is an internal issued
// reservation, never the published admitted phase. Sequence is compare-only;
// exact repeats and reads never consume a sequence or extend any deadline.
type BrokerJournalRecord struct {
	OperationID       string
	Reservation       BrokerJournalReservation
	IssuedAtMillis    int64
	AcceptUntilMillis int64
	Intent            BrokerJournalIntent
	BindingSHA256     string
	Sequence          uint64
	Phase             BrokerOperationPhase
	DispatchClaimed   bool
	ArtifactSHA256    string
	ArtifactBytes     int64
	ResultSHA256      string
	RecordedAtMillis  int64
}

// BrokerJournalCompare binds a mutation to the exact durable intent and state.
type BrokerJournalCompare struct {
	Owner         BrokerJournalOwner
	OperationID   string
	BindingSHA256 string
	Sequence      uint64
}

// BrokerJournalCompletion accepts only operation-owner evidence: applied
// requires a qualified result digest; not_applied requires positive proof of
// no dispatch or definitive rejection; insufficient proof means outcome_unknown.
// This storage contract does not itself qualify backend evidence.
type BrokerJournalCompletion struct {
	Phase        BrokerOperationPhase
	ResultSHA256 string
}

// BrokerJournalLookup is the only journal capability exposed to metadata-only
// outcome observers. Callers must independently authorize the exact ticket
// before invoking it; knowledge of an ID is never a grant.
type BrokerJournalLookup interface {
	Lookup(context.Context, BrokerJournalOwner, string) (BrokerJournalRecord, error)
}

// BrokerJournal is a storage-only port. Reserve durably mints an ID before Bind
// fixes the prospective apply arguments containing it. Admit durably stores the
// separate recovery artifact. A successful ClaimDispatch is the sole dispatch
// right; every repeat fails. Consumers must recheck current authority and hard
// deadlines after durable I/O and before their single operation-specific send.
// Lookup and ReadArtifact require independent current observation authorization
// upstream, including exact ticket scope; knowledge of an ID is never a grant.
// There is no listing, retry, deletion, automatic reconciliation or GC API.
type BrokerJournal interface {
	BrokerJournalLookup
	Reserve(context.Context, BrokerJournalReservation) (BrokerJournalRecord, error)
	Bind(context.Context, BrokerJournalOwner, string, BrokerJournalIntent) (BrokerJournalRecord, error)
	Admit(context.Context, BrokerJournalCompare, []byte, string) (BrokerJournalRecord, error)
	ClaimDispatch(context.Context, BrokerJournalCompare) (BrokerJournalRecord, error)
	Complete(context.Context, BrokerJournalCompare, BrokerJournalCompletion) (BrokerJournalRecord, error)
	ReadArtifact(context.Context, BrokerJournalOwner, string) ([]byte, error)
	Fenced(context.Context, string) (bool, error)
}
