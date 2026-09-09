//go:build linux || darwin

package brokerjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func rawSlot(payload []byte) []byte {
	data := make([]byte, slotBytes)
	binary.BigEndian.PutUint32(data, uint32(len(payload)))
	sum := sha256.Sum256(append([]byte("atl.broker.journal.v1/slot\x00"), payload...))
	copy(data[4:36], sum[:])
	copy(data[36:], payload)
	return data
}

func TestIntentBindingMatchesSharedKnownAnswer(t *testing.T) {
	reservation := testReservation()
	record := domain.BrokerJournalRecord{
		OperationID: digest([]byte("known operation")), Reservation: reservation,
		IssuedAtMillis: testMillis, AcceptUntilMillis: testMillis + domain.BrokerMaxOperationMillis,
	}
	intent := testIntent(record)
	privateBinding, privateErr := intentBinding(record, intent)
	sharedBinding, sharedErr := brokercontract.BrokerJournalIntentBindingSHA256V1(record, intent)
	const want = "7ef99e38feb2397f1e10a51ff00251a1d99f86757b2fdd5ee4eb2b4258158b3f"
	if privateErr != nil || sharedErr != nil || privateBinding != sharedBinding || privateBinding != want {
		t.Fatalf("private=%q shared=%q errors=%v/%v", privateBinding, sharedBinding, privateErr, sharedErr)
	}
}

func TestStrictPrivateCodecAndCorruptRestart(t *testing.T) {
	f := newFixture(t, Limits{})
	r := reserveBound(t, f.journal)
	valid, err := encodeSlot(snapshot{1, r})
	must(t, err)
	n := int(binary.BigEndian.Uint32(valid[:4]))
	payload := valid[36 : 36+n]
	cases := map[string][]byte{
		"duplicate":  rawSlot(bytes.Replace(payload, []byte(`"Version":1`), []byte(`"Version":1,"Version":1`), 1)),
		"future":     rawSlot(bytes.Replace(payload, []byte(`"Version":1`), []byte(`"Version":2`), 1)),
		"unknown":    rawSlot(bytes.Replace(payload, []byte(`"Version":1`), []byte(`"Version":1,"Extra":0`), 1)),
		"missing":    rawSlot(bytes.Replace(payload, []byte(`"Version":1,`), nil, 1)),
		"wrong case": rawSlot(bytes.Replace(payload, []byte(`"Version":1`), []byte(`"version":1`), 1)),
		"coercion":   rawSlot(bytes.Replace(payload, []byte(`"Version":1`), []byte(`"Version":"1"`), 1)),
		"fraction":   rawSlot(bytes.Replace(payload, []byte(`"Version":1`), []byte(`"Version":1.0`), 1)),
		"trailing":   rawSlot(append(bytes.Clone(payload), []byte(`{}`)...)),
		"whitespace": rawSlot(append([]byte(" "), payload...)),
		"truncated":  valid[:len(valid)-1],
		"checksum":   append([]byte{255}, valid[1:]...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			var decoded snapshot
			err := decodeSlot(data, &decoded)
			if err == nil && (decoded.Version != 1 || validateRecord(decoded.Record) != nil) {
				err = errUnavailable
			}
			if !errors.Is(err, errUnavailable) {
				t.Fatalf("invalid codec accepted: %v", err)
			}
		})
	}
	// A torn future slot must block the entire journal, even with all earlier
	// slots intact. It cannot be ignored as evidence that dispatch did not occur.
	must(t, f.journal.Close())
	file, err := os.OpenFile(filepath.Join(f.path, r.OperationID+".rec"), os.O_RDWR, 0)
	must(t, err)
	_, err = file.WriteAt([]byte{1}, 2*slotBytes)
	must(t, err)
	must(t, file.Close())
	if _, err := openJournal(f.path, testIdentity(), Limits{}, false, func() time.Time { return time.UnixMilli(testMillis) }); !errors.Is(err, errUnavailable) {
		t.Fatalf("partial slot recovered: %v", err)
	}
}

func TestTransitionGraphHasAtMostSixDurableSnapshots(t *testing.T) {
	// Enumerate every legal phase path, including recovery and observation.
	edges := map[string][]string{
		"reserved": {"bound"}, "bound": {"admitted"},
		"admitted":        {"dispatching", "not_applied"},
		"dispatching":     {"applied", "not_applied", "outcome_unknown"},
		"outcome_unknown": {"applied", "not_applied"},
	}
	maximum := 0
	var visit func(string, int, map[string]bool)
	visit = func(state string, count int, seen map[string]bool) {
		if seen[state] {
			t.Fatalf("cycle at %s", state)
		}
		seen[state] = true
		defer delete(seen, state)
		maximum = max(maximum, count)
		for _, next := range edges[state] {
			visit(next, count+1, seen)
		}
	}
	visit("reserved", 1, map[string]bool{})
	if maximum != 6 || maximum > slotCount {
		t.Fatalf("slot proof=%d reserved=%d", maximum, slotCount)
	}
	f := newFixture(t, Limits{})
	r := admitted(t, f.journal)
	r, err := f.journal.ClaimDispatch(context.Background(), comparison(r))
	must(t, err)
	unknown, err := f.journal.Complete(context.Background(), comparison(r), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
	must(t, err)
	cleared := unknown
	cleared.Phase, cleared.Sequence, cleared.DispatchClaimed = domain.BrokerOperationNotApplied, unknown.Sequence+1, false
	if validSuccessor(unknown, cleared) {
		t.Fatal("transition cleared consumed dispatch evidence")
	}
	for _, phase := range []domain.BrokerOperationPhase{"", domain.BrokerOperationAdmitted, domain.BrokerOperationDispatching, domain.BrokerOperationRetiredNonReplayable} {
		if _, err := f.journal.Complete(context.Background(), comparison(unknown), domain.BrokerJournalCompletion{Phase: phase}); !errors.Is(err, errConflict) {
			t.Fatalf("illegal regression %s: %v", phase, err)
		}
	}
	if _, err := f.journal.Complete(context.Background(), comparison(unknown), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationNotApplied}); err == nil {
		t.Fatal("absence of proof released unknown fence")
	}
}

func TestArtifactMismatchCannotClaimDispatch(t *testing.T) {
	f := newFixture(t, Limits{})
	r := reserveBound(t, f.journal)
	if _, err := f.journal.Admit(context.Background(), comparison(r), []byte("changed"), digest([]byte("original"))); !errors.Is(err, errInvalid) {
		t.Fatalf("artifact digest=%v", err)
	}
	artifact := []byte("candidate\n")
	r, err := f.journal.Admit(context.Background(), comparison(r), artifact, digest(artifact))
	must(t, err)
	file, err := os.OpenFile(filepath.Join(f.path, r.OperationID+".art"), os.O_RDWR, 0)
	must(t, err)
	_, err = file.WriteAt([]byte("C"), 0)
	must(t, err)
	must(t, file.Close())
	if _, err := f.journal.ClaimDispatch(context.Background(), comparison(r)); !errors.Is(err, errUnavailable) {
		t.Fatalf("artifact altered=%v", err)
	}
}

func FuzzPrivateSlot(f *testing.F) {
	r := domain.BrokerJournalRecord{OperationID: digest([]byte("id")), Reservation: testReservation(), IssuedAtMillis: testMillis, AcceptUntilMillis: testMillis + 60000, Sequence: 1, RecordedAtMillis: testMillis}
	valid, err := encodeSlot(snapshot{1, r})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add(rawSlot([]byte(`{"Version":1,"Version":1}`)))
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded snapshot
		if decodeSlot(data, &decoded) != nil {
			return
		}
		encoded, err := encodeSlot(decoded)
		if err != nil || !bytes.Equal(data, encoded) {
			t.Fatal("accepted private slot is not canonical")
		}
	})
}

func FuzzJournalSelector(f *testing.F) {
	for _, seed := range []string{"", ".", "..", "../escape", "a/b", "a\\b", digest([]byte("valid")), string([]byte{0, 255})} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, selector string) {
		if !validDigest(selector) {
			return
		}
		if len(selector) != 64 || filepath.Base(selector) != selector || filepath.IsAbs(selector) {
			t.Fatal("accepted selector can escape fixed name")
		}
	})
}
