// Package brokerjournal persists bounded, single-writer Broker ownership.
// It performs no authorization or backend I/O and is not runtime enablement.
package brokerjournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	slotBytes        = 8 << 10
	slotCount        = 8
	recordBytes      = slotBytes * slotCount
	indexSlotBytes   = 256
	stateBytes       = indexSlotBytes * slotCount
	MaxArtifactBytes = 16 << 20
	MaxRecords       = 1024
	MaxReservedBytes = 256 << 20
)

// Identity must come from trusted deployment configuration, including an
// invalidated deployment epoch after storage rollback; a local file cannot
// independently prove that its own historical identity is still current.
type Identity struct {
	BrokerSHA256  string
	BackendSHA256 string
}

// Limits are fixed at creation and must match on reopen. Zero means the
// conservative defaults; values can only reduce the hard ceilings.
type Limits struct {
	Records       int
	ReservedBytes int64
}

type header struct {
	Version         int
	Identity        Identity
	Limits          Limits
	CreatedAtMillis int64
}

type snapshot struct {
	Version int
	Record  domain.BrokerJournalRecord
}

var (
	errUnavailable = fmt.Errorf("%w: broker journal unavailable", domain.ErrCheckFailed)
	errConflict    = fmt.Errorf("%w: broker journal state conflict", domain.ErrCheckFailed)
	errDenied      = fmt.Errorf("%w: broker journal access denied", domain.ErrForbidden)
	errInvalid     = fmt.Errorf("%w: broker journal input invalid", domain.ErrUsage)
	errExpired     = fmt.Errorf("%w: broker journal deadline expired", domain.ErrForbidden)
	errCapacity    = fmt.Errorf("%w: broker journal capacity exhausted", domain.ErrCheckFailed)
)

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func encodeSlot(value any) ([]byte, error) {
	return encodeFrame(value, slotBytes)
}

func encodeFrame(value any, size int) ([]byte, error) {
	data, err := json.Marshal(value)
	if size != slotBytes && size != indexSlotBytes || err != nil || len(data) > size-36 {
		return nil, errUnavailable
	}
	result := make([]byte, size)
	binary.BigEndian.PutUint32(result, uint32(len(data))) // #nosec G115 -- length is bounded by the 8156-byte slot payload above.
	sum := sha256.Sum256(append([]byte("atl.broker.journal.v1/slot\x00"), data...))
	copy(result[4:36], sum[:])
	copy(result[36:], data)
	return result, nil
}

func decodeSlot(data []byte, out any) error {
	return decodeFrame(data, slotBytes, out)
}

func decodeFrame(data []byte, size int, out any) error {
	if size != slotBytes && size != indexSlotBytes || len(data) != size {
		return errUnavailable
	}
	n := int(binary.BigEndian.Uint32(data[:4]))
	if n < 1 || n > size-36 || !zero(data[36+n:]) {
		return errUnavailable
	}
	payload := data[36 : 36+n]
	sum := sha256.Sum256(append([]byte("atl.broker.journal.v1/slot\x00"), payload...))
	if !bytes.Equal(data[4:36], sum[:]) || strictjson.DecodeExact(payload, 8, out) != nil {
		return errUnavailable
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(canonical, payload) {
		return errUnavailable
	}
	return nil
}

func zero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func validFilename(name string) bool {
	if name == "identity" || name == "reservations" {
		return true
	}
	if len(name) < 64 || !validDigest(name[:64]) {
		return false
	}
	return name[64:] == ".rec" || name[64:] == ".art" || name[64:] == ".state"
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.Records == 0 {
		limits.Records = MaxRecords
	}
	if limits.ReservedBytes == 0 {
		limits.ReservedBytes = MaxReservedBytes
	}
	if limits.Records < 1 || limits.Records > MaxRecords || limits.ReservedBytes < initialBytes(limits)+recordBytes+stateBytes+1 || limits.ReservedBytes > MaxReservedBytes {
		return Limits{}, errInvalid
	}
	return limits, nil
}

func validOwner(owner domain.BrokerJournalOwner) bool {
	return validDigest(owner.BrokerSHA256) && validDigest(owner.BackendSHA256) && validDigest(owner.PrincipalSHA256) && validDigest(owner.WorkloadSHA256) && validDigest(owner.AudienceSHA256)
}

func validReservation(r domain.BrokerJournalReservation, issued, accept int64) bool {
	return validOwner(r.Owner) && validDigest(r.ExecutionSHA256) && validDigest(r.AuthorityRevisionSHA256) &&
		r.ExecutionNotBeforeMillis > 0 && issued >= r.ExecutionNotBeforeMillis && issued > 0 &&
		accept > issued && accept-issued <= domain.BrokerMaxOperationMillis &&
		accept <= r.ExecutionExpiresMillis && accept <= r.GrantExpiresMillis && accept <= r.CredentialExpiresMillis &&
		accept <= r.OperationDeadlineMillis && r.OperationDeadlineMillis <= r.ExecutionExpiresMillis &&
		r.OperationDeadlineMillis <= r.GrantExpiresMillis && r.OperationDeadlineMillis <= r.CredentialExpiresMillis &&
		r.OperationDeadlineMillis-issued <= domain.BrokerMaxOperationMillis &&
		r.ObservationUntilMillis >= r.OperationDeadlineMillis && r.ArtifactCapacity > 0 && r.ArtifactCapacity <= MaxArtifactBytes
}

func intentBinding(record domain.BrokerJournalRecord, intent domain.BrokerJournalIntent) (string, error) {
	ticket := intent.Ticket
	owner := record.Reservation.Owner
	if ticket.Operation != domain.BrokerOperationJiraCommentApply || ticket.OperationID != record.OperationID ||
		ticket.PrincipalSHA256 != owner.PrincipalSHA256 || ticket.ExecutionSHA256 != record.Reservation.ExecutionSHA256 ||
		ticket.AudienceSHA256 != owner.AudienceSHA256 || ticket.BackendSHA256 != owner.BackendSHA256 ||
		ticket.IssuedAtMillis != record.IssuedAtMillis || ticket.AcceptUntilMillis != record.AcceptUntilMillis ||
		!validDigest(intent.NativeSHA256) || !validDigest(intent.TargetSHA256) || !validDigest(intent.EffectSHA256) || !validDigest(intent.EvidenceSHA256) {
		return "", errInvalid
	}
	ticketDigest, err := brokercontract.OperationTicketSHA256(ticket)
	if err != nil {
		return "", errInvalid
	}
	// Reuse the public ticket digest without redefining its fields. The private
	// binding adds the stable owner, original authority and operation evidence.
	data, err := json.Marshal(struct {
		Reservation    domain.BrokerJournalReservation
		TicketSHA256   string
		NativeSHA256   string
		TargetSHA256   string
		EffectSHA256   string
		EvidenceSHA256 string
	}{record.Reservation, ticketDigest, intent.NativeSHA256, intent.TargetSHA256, intent.EffectSHA256, intent.EvidenceSHA256})
	if err != nil {
		return "", errInvalid
	}
	return digest(append([]byte("atl.broker.journal.v1/binding\x00"), data...)), nil
}

func validateRecord(r domain.BrokerJournalRecord) error {
	if !validDigest(r.OperationID) || !validReservation(r.Reservation, r.IssuedAtMillis, r.AcceptUntilMillis) ||
		r.Sequence < 1 || r.Sequence > slotCount || r.RecordedAtMillis < r.IssuedAtMillis {
		return errUnavailable
	}
	if r.BindingSHA256 == "" {
		if r.Intent != (domain.BrokerJournalIntent{}) || r.Sequence != 1 || r.Phase != "" {
			return errUnavailable
		}
	} else {
		binding, err := intentBinding(r, r.Intent)
		if err != nil || binding != r.BindingSHA256 || r.Sequence < 2 {
			return errUnavailable
		}
	}
	if r.Phase == "" {
		if r.DispatchClaimed || r.ArtifactSHA256 != "" || r.ArtifactBytes != 0 || r.ResultSHA256 != "" || r.Sequence > 2 {
			return errUnavailable
		}
		return nil
	}
	if !validDigest(r.ArtifactSHA256) || r.ArtifactBytes < 1 || r.ArtifactBytes > r.Reservation.ArtifactCapacity || r.BindingSHA256 == "" {
		return errUnavailable
	}
	switch r.Phase {
	case domain.BrokerOperationAdmitted:
		if r.DispatchClaimed || r.ResultSHA256 != "" {
			return errUnavailable
		}
	case domain.BrokerOperationDispatching, domain.BrokerOperationOutcomeUnknown:
		if !r.DispatchClaimed || r.ResultSHA256 != "" {
			return errUnavailable
		}
	case domain.BrokerOperationApplied:
		if !r.DispatchClaimed || !validDigest(r.ResultSHA256) {
			return errUnavailable
		}
	case domain.BrokerOperationNotApplied:
		if r.DispatchClaimed && !validDigest(r.ResultSHA256) || r.ResultSHA256 != "" && !validDigest(r.ResultSHA256) {
			return errUnavailable
		}
	default:
		return errUnavailable
	}
	return nil
}

func validSuccessor(before, after domain.BrokerJournalRecord) bool {
	if after.Sequence != before.Sequence+1 || after.RecordedAtMillis < before.RecordedAtMillis ||
		after.DispatchClaimed != (before.DispatchClaimed || after.Phase == domain.BrokerOperationDispatching) {
		return false
	}
	immutable := after
	immutable.Sequence, immutable.RecordedAtMillis = before.Sequence, before.RecordedAtMillis
	immutable.Phase, immutable.DispatchClaimed = before.Phase, before.DispatchClaimed
	immutable.ResultSHA256 = before.ResultSHA256
	if before.BindingSHA256 == "" {
		immutable.Intent, immutable.BindingSHA256 = before.Intent, before.BindingSHA256
		return immutable == before && after.BindingSHA256 != "" && after.Phase == ""
	}
	if before.Phase == "" {
		immutable.ArtifactBytes, immutable.ArtifactSHA256 = before.ArtifactBytes, before.ArtifactSHA256
		return immutable == before && after.Phase == domain.BrokerOperationAdmitted
	}
	if immutable != before {
		return false
	}
	switch before.Phase {
	case domain.BrokerOperationAdmitted:
		return after.Phase == domain.BrokerOperationDispatching || after.Phase == domain.BrokerOperationNotApplied
	case domain.BrokerOperationDispatching:
		return after.Phase == domain.BrokerOperationApplied || after.Phase == domain.BrokerOperationNotApplied || after.Phase == domain.BrokerOperationOutcomeUnknown
	case domain.BrokerOperationOutcomeUnknown:
		return after.Phase == domain.BrokerOperationApplied || after.Phase == domain.BrokerOperationNotApplied
	default:
		return false
	}
}
