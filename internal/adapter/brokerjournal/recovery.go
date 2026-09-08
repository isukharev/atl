package brokerjournal

import "github.com/isukharev/atl/internal/domain"

func (j *Journal) loadRecord(id string) (domain.BrokerJournalRecord, error) {
	data, err := j.disk.read(id+".rec", recordBytes)
	if err != nil {
		return domain.BrokerJournalRecord{}, errUnavailable
	}
	commitments, err := j.disk.read(id+".state", stateBytes)
	if err != nil {
		return domain.BrokerJournalRecord{}, errUnavailable
	}
	var current domain.BrokerJournalRecord
	empty := false
	for index := range slotCount {
		slot := data[index*slotBytes : (index+1)*slotBytes]
		commitmentSlot := commitments[index*indexSlotBytes : (index+1)*indexSlotBytes]
		if zero(slot) {
			if !zero(commitmentSlot) {
				return domain.BrokerJournalRecord{}, errUnavailable
			}
			empty = true
			continue
		}
		var next snapshot
		if empty || decodeSlot(slot, &next) != nil || next.Version != 1 || next.Record.OperationID != id ||
			next.Record.Sequence != uint64(index+1) || validateRecord(next.Record) != nil {
			return domain.BrokerJournalRecord{}, errUnavailable
		}
		var commitment stateCommitment
		if decodeFrame(commitmentSlot, indexSlotBytes, &commitment) != nil || commitment != commitmentFor(next.Record, slot) {
			return domain.BrokerJournalRecord{}, errUnavailable
		}
		if index == 0 && (next.Record.BindingSHA256 != "" || next.Record.Phase != "") ||
			index > 0 && !validSuccessor(current, next.Record) {
			return domain.BrokerJournalRecord{}, errUnavailable
		}
		current = next.Record
	}
	if current.Sequence == 0 {
		return domain.BrokerJournalRecord{}, errUnavailable
	}
	return current, nil
}

func (j *Journal) recover() error {
	entries, err := j.readReservations()
	if err != nil {
		return errUnavailable
	}
	names, err := j.disk.names(2 + 3*j.header.Limits.Records)
	if err != nil {
		return errUnavailable
	}
	owned := make(map[string]bool, len(names))
	for _, name := range names {
		if !validFilename(name) {
			return errUnavailable
		}
		owned[name] = true
	}
	if !owned["identity"] || !owned["reservations"] || len(names) != 2+3*len(entries) {
		return errUnavailable
	}
	// Validate the entire inventory before any recovery write. A partial
	// reservation, artifact or slot is unavailable, never guessed healthy.
	for _, entry := range entries {
		id := entry.OperationID
		if !owned[id+".rec"] || !owned[id+".art"] || !owned[id+".state"] {
			return errUnavailable
		}
		r, err := j.loadRecord(id)
		if err != nil || r.Reservation.ArtifactCapacity != entry.ArtifactCapacity || r.Reservation.Owner.BrokerSHA256 != j.header.Identity.BrokerSHA256 || r.Reservation.Owner.BackendSHA256 != j.header.Identity.BackendSHA256 || r.IssuedAtMillis < j.header.CreatedAtMillis-domain.BrokerClockAllowanceMillis {
			return errUnavailable
		}
		cost := int64(recordBytes+stateBytes) + r.Reservation.ArtifactCapacity
		if cost > j.header.Limits.ReservedBytes-j.reserved {
			return errUnavailable
		}
		j.reserved += cost
		if r.ArtifactSHA256 == "" {
			data, readErr := j.disk.read(id+".art", r.Reservation.ArtifactCapacity)
			if readErr != nil || !zero(data) {
				return errUnavailable
			}
		} else if _, err := j.artifact(r); err != nil {
			return errUnavailable
		}
		j.records[id] = r
		j.highWater = max(j.highWater, r.RecordedAtMillis)
		if r.Phase == domain.BrokerOperationAdmitted || r.Phase == domain.BrokerOperationDispatching || r.Phase == domain.BrokerOperationOutcomeUnknown {
			if _, exists := j.fences[r.Intent.TargetSHA256]; exists {
				return errUnavailable
			}
			j.fences[r.Intent.TargetSHA256] = id
		}
	}
	j.reservations = entries
	for _, entry := range entries {
		r := j.records[entry.OperationID]
		switch r.Phase {
		case domain.BrokerOperationAdmitted:
			r.Phase = domain.BrokerOperationNotApplied
		case domain.BrokerOperationDispatching:
			r.Phase = domain.BrokerOperationOutcomeUnknown
		default:
			continue
		}
		r.Sequence++
		r.RecordedAtMillis = j.observeTime()
		if err := j.persist(r); err != nil {
			return err
		}
		if r.Phase == domain.BrokerOperationNotApplied {
			delete(j.fences, r.Intent.TargetSHA256)
		}
	}
	return nil
}
