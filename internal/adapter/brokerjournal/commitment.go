package brokerjournal

import "github.com/isukharev/atl/internal/domain"

// Each record snapshot has an independently durable commitment. Neither a
// missing record tail nor a missing commitment tail is an absent transition:
// mismatching chains fail closed before recovering phase or target fences.
// The issuance index has one immutable slot per ID. Separate fixed .state
// slots preserve append-only phase evidence and reserved terminal capacity
// without rewriting that global inventory on every transition.
type stateCommitment struct {
	Version         int
	Sequence        uint64
	RecordSHA256    string
	DispatchClaimed bool
}

func commitmentFor(r domain.BrokerJournalRecord, slot []byte) stateCommitment {
	return stateCommitment{1, r.Sequence, digest(slot), r.DispatchClaimed}
}

func (j *Journal) commitState(r domain.BrokerJournalRecord, slot []byte) error {
	phase := string(r.Phase)
	if phase == "" {
		phase = "reservation"
		if r.BindingSHA256 != "" {
			phase = "binding"
		}
	}
	if j.checkpoint(phase+"_record_durable") != nil {
		return j.poison()
	}
	data, err := encodeFrame(commitmentFor(r, slot), indexSlotBytes)
	offset := int64(r.Sequence-1) * indexSlotBytes // #nosec G115 -- persist validates Sequence to 1..8 before this private call.
	if err != nil || j.disk.write(r.OperationID+".state", offset, data) != nil {
		return j.poison()
	}
	return j.checkpoint(phase + "_state_durable")
}
