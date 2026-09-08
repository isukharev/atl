//go:build linux

package brokerjournal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/isukharev/atl/internal/domain"
)

func TestConsumedRecordTailLossCannotReleaseUnknownFence(t *testing.T) {
	for _, lost := range []string{"record tail", "commitment tail", "commitment file", "commitment digest"} {
		t.Run(lost, func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := admitted(t, f.journal)
			r, err := f.journal.ClaimDispatch(context.Background(), comparison(r))
			must(t, err)
			r, err = f.journal.Complete(context.Background(), comparison(r), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
			must(t, err)
			fenced, err := f.journal.Fenced(context.Background(), r.Intent.TargetSHA256)
			must(t, err)
			if !fenced || r.Sequence != 5 {
				t.Fatal("regression did not reach consumed unknown state")
			}
			must(t, f.journal.Close())
			switch lost {
			case "record tail":
				// Slots 1..3 still form a valid admitted record; only the
				// independent commitment proves slots 4+ must exist.
				writeOwnedBytes(t, filepath.Join(f.path, r.OperationID+".rec"), 3*slotBytes, make([]byte, recordBytes-3*slotBytes))
			case "commitment tail":
				writeOwnedBytes(t, filepath.Join(f.path, r.OperationID+".state"), 3*indexSlotBytes, make([]byte, stateBytes-3*indexSlotBytes))
			case "commitment file":
				must(t, os.Remove(filepath.Join(f.path, r.OperationID+".state")))
			case "commitment digest":
				commitment := stateCommitment{1, 5, digest([]byte("changed snapshot")), true}
				data, err := encodeFrame(commitment, indexSlotBytes)
				must(t, err)
				writeOwnedBytes(t, filepath.Join(f.path, r.OperationID+".state"), 4*indexSlotBytes, data)
			}
			before, err := os.ReadFile(filepath.Join(f.path, r.OperationID+".rec"))
			must(t, err)
			assertReopenUnavailable(t, f)
			after, err := os.ReadFile(filepath.Join(f.path, r.OperationID+".rec"))
			must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("failed recovery rewrote phase and could release a fence")
			}
		})
	}
}

func TestStateCommitmentSyncFailureNeverGrantsDispatch(t *testing.T) {
	for _, point := range []string{"before_write", "file_sync", "after_file_sync"} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := admitted(t, f.journal)
			fired := false
			f.journal.hook = func(at string) error {
				if at == "dispatching_record_durable" {
					f.journal.disk.(*localDisk).hook = func(stage string) error {
						if stage == point {
							fired = true
							return unix.ENOSPC
						}
						return nil
					}
				}
				return nil
			}
			result, err := f.journal.ClaimDispatch(context.Background(), comparison(r))
			if !fired || !errors.Is(err, errUnavailable) || result.OperationID != "" {
				t.Fatalf("failed commitment granted dispatch: %v", err)
			}
			if point == "before_write" {
				must(t, f.journal.Close())
				assertReopenUnavailable(t, f)
				return
			}
			// A complete commitment whose sync acknowledgement was lost may
			// be stabilized on reopen, but remains consumed and unknown.
			j := reopen(t, f, Limits{})
			got, err := j.Lookup(context.Background(), r.Reservation.Owner, r.OperationID)
			must(t, err)
			if got.Phase != domain.BrokerOperationOutcomeUnknown || !got.DispatchClaimed {
				t.Fatal("lost sync acknowledgement released dispatch evidence")
			}
			if _, err := j.ClaimDispatch(context.Background(), comparison(got)); !errors.Is(err, errConflict) {
				t.Fatalf("commitment replay=%v", err)
			}
		})
	}
}

func writeOwnedBytes(t *testing.T, path string, offset int64, data []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	must(t, err)
	_, err = file.WriteAt(data, offset)
	must(t, err)
	must(t, file.Close())
}

func FuzzStateCommitmentFrame(f *testing.F) {
	valid, err := encodeFrame(stateCommitment{1, 4, digest([]byte("snapshot")), true}, indexSlotBytes)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add(make([]byte, indexSlotBytes))
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded stateCommitment
		if decodeFrame(data, indexSlotBytes, &decoded) != nil {
			return
		}
		encoded, err := encodeFrame(decoded, indexSlotBytes)
		if err != nil || !bytes.Equal(data, encoded) {
			t.Fatal("accepted commitment is not canonical")
		}
	})
}
