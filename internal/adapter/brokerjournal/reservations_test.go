//go:build linux || darwin

package brokerjournal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/isukharev/atl/internal/domain"
)

func TestUnknownPairDeletionCannotEraseFenceOnRestart(t *testing.T) {
	f := newFixture(t, Limits{})
	r := admitted(t, f.journal)
	r, err := f.journal.ClaimDispatch(context.Background(), comparison(r))
	must(t, err)
	r, err = f.journal.Complete(context.Background(), comparison(r), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
	must(t, err)
	fenced, err := f.journal.Fenced(context.Background(), r.Intent.TargetSHA256)
	must(t, err)
	if !fenced {
		t.Fatal("regression setup did not reach a real unknown fence")
	}
	if _, err := f.journal.Lookup(context.Background(), r.Reservation.Owner, digest([]byte("unknown ID"))); !errors.Is(err, errDenied) {
		t.Fatalf("unknown ID was admitted: %v", err)
	}
	must(t, f.journal.Close())
	// Remove all three of this test's operation files; preserve only the
	// valid identity and committed index. The index alone must prevent an
	// unfenced empty journal, without relying on a leftover state file.
	must(t, os.Remove(filepath.Join(f.path, r.OperationID+".rec")))
	must(t, os.Remove(filepath.Join(f.path, r.OperationID+".art")))
	must(t, os.Remove(filepath.Join(f.path, r.OperationID+".state")))
	assertReopenUnavailable(t, f)
}

func TestReservationIndexRejectsMissingTornAlteredAndUnindexedState(t *testing.T) {
	cases := map[string]func(*testing.T, fixture, domain.BrokerJournalRecord){
		"missing index": func(t *testing.T, f fixture, _ domain.BrokerJournalRecord) {
			must(t, os.Remove(filepath.Join(f.path, "reservations")))
		},
		"truncated index": func(t *testing.T, f fixture, _ domain.BrokerJournalRecord) {
			must(t, os.Truncate(filepath.Join(f.path, "reservations"), indexSlotBytes-1))
		},
		"torn appended slot": func(t *testing.T, f fixture, _ domain.BrokerJournalRecord) {
			writeIndexBytes(t, f, indexSlotBytes, []byte{1})
		},
		"altered checksum": func(t *testing.T, f fixture, _ domain.BrokerJournalRecord) {
			data, err := os.ReadFile(filepath.Join(f.path, "reservations"))
			must(t, err)
			writeIndexBytes(t, f, 4, []byte{data[4] ^ 255})
		},
		"zeroed issued entry": func(t *testing.T, f fixture, _ domain.BrokerJournalRecord) {
			writeIndexBytes(t, f, 0, make([]byte, indexSlotBytes))
		},
		"valid changed capacity": func(t *testing.T, f fixture, r domain.BrokerJournalRecord) {
			data, err := encodeFrame(reservationEntry{1, 1, r.OperationID, r.Reservation.ArtifactCapacity + 1}, indexSlotBytes)
			must(t, err)
			writeIndexBytes(t, f, 0, data)
		},
		"duplicate ID": func(t *testing.T, f fixture, r domain.BrokerJournalRecord) {
			data, err := encodeFrame(reservationEntry{1, 2, r.OperationID, r.Reservation.ArtifactCapacity}, indexSlotBytes)
			must(t, err)
			writeIndexBytes(t, f, indexSlotBytes, data)
		},
		"future index version": func(t *testing.T, f fixture, r domain.BrokerJournalRecord) {
			data, err := encodeFrame(reservationEntry{2, 1, r.OperationID, r.Reservation.ArtifactCapacity}, indexSlotBytes)
			must(t, err)
			writeIndexBytes(t, f, 0, data)
		},
		"sequence gap": func(t *testing.T, f fixture, r domain.BrokerJournalRecord) {
			data, err := encodeFrame(reservationEntry{1, 2, r.OperationID, r.Reservation.ArtifactCapacity}, indexSlotBytes)
			must(t, err)
			writeIndexBytes(t, f, indexSlotBytes, data)
			writeIndexBytes(t, f, 0, make([]byte, indexSlotBytes))
		},
		"index mode": func(t *testing.T, f fixture, _ domain.BrokerJournalRecord) {
			must(t, os.Chmod(filepath.Join(f.path, "reservations"), 0o640))
		},
		"index hardlink": func(t *testing.T, f fixture, _ domain.BrokerJournalRecord) {
			must(t, os.Link(filepath.Join(f.path, "reservations"), filepath.Join(filepath.Dir(f.path), "index-link")))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := reserveBound(t, f.journal)
			must(t, f.journal.Close())
			mutate(t, f, r)
			assertReopenUnavailable(t, f)
		})
	}
	t.Run("unexpected unindexed pair", func(t *testing.T) {
		f := newFixture(t, Limits{})
		r, err := f.journal.Reserve(context.Background(), testReservation())
		must(t, err)
		id := digest([]byte("unindexed reservation"))
		must(t, f.journal.disk.allocate(id+".rec", recordBytes))
		must(t, f.journal.disk.allocate(id+".art", r.Reservation.ArtifactCapacity))
		must(t, f.journal.disk.allocate(id+".state", stateBytes))
		r.OperationID = id
		data, err := encodeSlot(snapshot{1, r})
		must(t, err)
		must(t, f.journal.disk.write(id+".rec", 0, data))
		commitment, err := encodeFrame(commitmentFor(r, data), indexSlotBytes)
		must(t, err)
		must(t, f.journal.disk.write(id+".state", 0, commitment))
		if _, err := f.journal.loadRecord(id); err != nil {
			t.Fatal("unindexed pair was not otherwise valid")
		}
		must(t, f.journal.Close())
		assertReopenUnavailable(t, f)
	})
}

func TestIndexSyncFailureDoesNotExposeReservedID(t *testing.T) {
	f := newFixture(t, Limits{})
	fired := false
	f.journal.hook = func(point string) error {
		if point == "reservation_allocated" {
			f.journal.disk.(*localDisk).hook = func(at string) error {
				if at == "file_sync" {
					fired = true
					return unix.ENOSPC
				}
				return nil
			}
		}
		return nil
	}
	r, err := f.journal.Reserve(context.Background(), testReservation())
	if !fired || !errors.Is(err, errUnavailable) || r.OperationID != "" {
		t.Fatalf("index fsync exposed reservation: %v", err)
	}
	must(t, f.journal.Close())
	assertReopenUnavailable(t, f)
}

func writeIndexBytes(t *testing.T, f fixture, offset int64, data []byte) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(f.path, "reservations"), os.O_RDWR, 0)
	must(t, err)
	_, err = file.WriteAt(data, offset)
	must(t, err)
	must(t, file.Close())
}

func assertReopenUnavailable(t *testing.T, f fixture) {
	t.Helper()
	j, err := openJournal(f.path, testIdentity(), Limits{}, false, func() time.Time { return time.UnixMilli(testMillis) })
	if j != nil {
		_ = j.Close()
	}
	if !errors.Is(err, errUnavailable) {
		t.Fatalf("inconsistent inventory became ready: %v", err)
	}
}

func FuzzReservationIndexFrame(f *testing.F) {
	valid, err := encodeFrame(reservationEntry{1, 1, digest([]byte("id")), 4096}, indexSlotBytes)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add(make([]byte, indexSlotBytes))
	f.Add([]byte(`{"Version":1,"Version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded reservationEntry
		if decodeFrame(data, indexSlotBytes, &decoded) != nil {
			return
		}
		encoded, err := encodeFrame(decoded, indexSlotBytes)
		if err != nil || !bytes.Equal(data, encoded) {
			t.Fatal("accepted index frame is not canonical")
		}
	})
}
