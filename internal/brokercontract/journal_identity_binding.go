package brokercontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/isukharev/atl/internal/domain"
)

// BrokerJournalIntentBindingSHA256V1 reproduces the journal's immutable
// binding bytes. It deliberately uses the original json.Marshal field names
// and order rather than the general canonical JSON digest mechanism.
func BrokerJournalIntentBindingSHA256V1(record domain.BrokerJournalRecord, intent domain.BrokerJournalIntent) (string, error) {
	ticket := intent.Ticket
	owner := record.Reservation.Owner
	if ticket.Operation != domain.BrokerOperationJiraCommentApply || ticket.OperationID != record.OperationID ||
		ticket.PrincipalSHA256 != owner.PrincipalSHA256 || ticket.ExecutionSHA256 != record.Reservation.ExecutionSHA256 ||
		ticket.AudienceSHA256 != owner.AudienceSHA256 || ticket.BackendSHA256 != owner.BackendSHA256 ||
		ticket.IssuedAtMillis != record.IssuedAtMillis || ticket.AcceptUntilMillis != record.AcceptUntilMillis ||
		!validDigest(intent.NativeSHA256) || !validDigest(intent.TargetSHA256) || !validDigest(intent.EffectSHA256) || !validDigest(intent.EvidenceSHA256) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	ticketDigest, err := OperationTicketSHA256(ticket)
	if err != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	data, err := json.Marshal(struct {
		Reservation    domain.BrokerJournalReservation
		TicketSHA256   string
		NativeSHA256   string
		TargetSHA256   string
		EffectSHA256   string
		EvidenceSHA256 string
	}{record.Reservation, ticketDigest, intent.NativeSHA256, intent.TargetSHA256, intent.EffectSHA256, intent.EvidenceSHA256})
	if err != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	digest := sha256.Sum256(append([]byte("atl.broker.journal.v1/binding\x00"), data...))
	return hex.EncodeToString(digest[:]), nil
}
