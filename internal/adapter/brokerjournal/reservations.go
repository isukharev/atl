package brokerjournal

import (
	"slices"

	"github.com/isukharev/atl/internal/domain"
)

// The durable inventory is independent of pair filenames: removing an entire
// consumed pair must not erase evidence that its fence needs restoration.
// Its fixed slots are preallocated at creation and never deleted or reused.
type reservationEntry struct {
	Version          int
	Sequence         int
	OperationID      string
	ArtifactCapacity int64
}

func initialBytes(limits Limits) int64 {
	return slotBytes + int64(limits.Records)*indexSlotBytes
}

func (j *Journal) readReservations() ([]reservationEntry, error) {
	data, err := j.disk.read("reservations", int64(j.header.Limits.Records)*indexSlotBytes)
	if err != nil {
		return nil, errUnavailable
	}
	entries := make([]reservationEntry, 0, j.header.Limits.Records)
	seen := make(map[string]bool)
	empty := false
	for index := range j.header.Limits.Records {
		frame := data[index*indexSlotBytes : (index+1)*indexSlotBytes]
		if zero(frame) {
			empty = true
			continue
		}
		var entry reservationEntry
		if empty || decodeFrame(frame, indexSlotBytes, &entry) != nil || entry.Version != 1 || entry.Sequence != index+1 || !validDigest(entry.OperationID) || entry.ArtifactCapacity < 1 || entry.ArtifactCapacity > MaxArtifactBytes || seen[entry.OperationID] {
			return nil, errUnavailable
		}
		seen[entry.OperationID] = true
		entries = append(entries, entry)
	}
	return entries, nil
}

func (j *Journal) checkReservations() error {
	entries, err := j.readReservations()
	if err != nil || !slices.Equal(entries, j.reservations) {
		return j.poison()
	}
	return nil
}

func (j *Journal) indexReservation(record domain.BrokerJournalRecord) error {
	if j.checkReservations() != nil || len(j.reservations) >= j.header.Limits.Records {
		return j.poison()
	}
	entry := reservationEntry{1, len(j.reservations) + 1, record.OperationID, record.Reservation.ArtifactCapacity}
	data, err := encodeFrame(entry, indexSlotBytes)
	if err != nil || j.disk.write("reservations", int64(len(j.reservations))*indexSlotBytes, data) != nil {
		return j.poison()
	}
	j.reservations = append(j.reservations, entry)
	return j.checkpoint("reservation_indexed")
}
