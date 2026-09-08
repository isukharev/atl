//go:build linux

package brokerjournal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const testMillis int64 = 1_800_000_000_000

func testIdentity() Identity {
	return Identity{digest([]byte("broker epoch")), digest([]byte("backend"))}
}

func testReservation() domain.BrokerJournalReservation {
	i := testIdentity()
	return domain.BrokerJournalReservation{
		Owner:           domain.BrokerJournalOwner{BrokerSHA256: i.BrokerSHA256, BackendSHA256: i.BackendSHA256, PrincipalSHA256: digest([]byte("principal")), WorkloadSHA256: digest([]byte("workload")), AudienceSHA256: digest([]byte("audience"))},
		ExecutionSHA256: digest([]byte("execution")), AuthorityRevisionSHA256: digest([]byte("revision")),
		ExecutionNotBeforeMillis: testMillis - 1000, ExecutionExpiresMillis: testMillis + 120000,
		GrantExpiresMillis: testMillis + 90000, CredentialExpiresMillis: testMillis + 80000,
		OperationDeadlineMillis: testMillis + 60000, ObservationUntilMillis: testMillis + 3600000,
		ArtifactCapacity: 4096,
	}
}

func testIntent(r domain.BrokerJournalRecord) domain.BrokerJournalIntent {
	owner := r.Reservation.Owner
	return domain.BrokerJournalIntent{
		Ticket:       domain.BrokerOperationTicket{SchemaVersion: 1, OperationID: r.OperationID, PrincipalSHA256: owner.PrincipalSHA256, ExecutionSHA256: r.Reservation.ExecutionSHA256, AudienceSHA256: owner.AudienceSHA256, BackendSHA256: owner.BackendSHA256, Operation: domain.BrokerOperationJiraCommentApply, OperationVersion: 1, ArgumentsSHA256: digest([]byte("semantic arguments including " + r.OperationID)), ProposalSHA256: digest([]byte("proposal")), IssuedAtMillis: r.IssuedAtMillis, AcceptUntilMillis: r.AcceptUntilMillis},
		NativeSHA256: digest([]byte("native\n")), TargetSHA256: digest([]byte("backend/immutable issue/comments")), EffectSHA256: digest([]byte("append comment")), EvidenceSHA256: digest([]byte("qualified version")),
	}
}

func comparison(r domain.BrokerJournalRecord) domain.BrokerJournalCompare {
	return domain.BrokerJournalCompare{Owner: r.Reservation.Owner, OperationID: r.OperationID, BindingSHA256: r.BindingSHA256, Sequence: r.Sequence}
}

func appliedCompletion() domain.BrokerJournalCompletion {
	return domain.BrokerJournalCompletion{Phase: domain.BrokerOperationApplied, ResultSHA256: digest([]byte("qualified result"))}
}

type fixture struct {
	journal *Journal
	path    string
	clock   *atomic.Int64
}

func newFixture(t *testing.T, limits Limits) fixture {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(testMillis)
	path := filepath.Join(t.TempDir(), "journal")
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(path, testIdentity(), limits, true, func() time.Time { return time.UnixMilli(clock.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return fixture{j, path, clock}
}

func reserveBound(t *testing.T, j *Journal) domain.BrokerJournalRecord {
	t.Helper()
	r, err := j.Reserve(context.Background(), testReservation())
	if err != nil {
		t.Fatal(err)
	}
	r, err = j.Bind(context.Background(), r.Reservation.Owner, r.OperationID, testIntent(r))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func admitted(t *testing.T, j *Journal) domain.BrokerJournalRecord {
	t.Helper()
	r := reserveBound(t, j)
	artifact := []byte("native\n; qualified baseline")
	r, err := j.Admit(context.Background(), comparison(r), artifact, digest(artifact))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func reopen(t *testing.T, f fixture, limits Limits) *Journal {
	t.Helper()
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(f.path, testIdentity(), limits, false, func() time.Time { return time.UnixMilli(f.clock.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func TestLifecycleSingleClaimAndTerminalReservation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Limits{Records: 1, ReservedBytes: slotBytes + indexSlotBytes + recordBytes + stateBytes + 4096})
	j := f.journal
	r := admitted(t, j)
	if _, err := j.Reserve(ctx, testReservation()); !errors.Is(err, errCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	var successes atomic.Int64
	var claimed domain.BrokerJournalRecord
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			next, err := j.ClaimDispatch(ctx, comparison(r))
			if err == nil {
				successes.Add(1)
				mu.Lock()
				claimed = next
				mu.Unlock()
			} else if !errors.Is(err, errConflict) {
				t.Errorf("claim: %v", err)
			}
		})
	}
	workers.Wait()
	if successes.Load() != 1 {
		t.Fatalf("dispatch successes=%d", successes.Load())
	}
	unknown, err := j.Complete(ctx, comparison(claimed), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := j.Complete(ctx, comparison(unknown), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
		if err != nil || again != unknown {
			t.Fatalf("repeat consumed state: %v", err)
		}
	}
	terminal, err := j.Complete(ctx, comparison(unknown), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationApplied, ResultSHA256: digest([]byte("qualified result"))})
	if err != nil || terminal.Sequence != 6 {
		t.Fatalf("terminal: %v sequence=%d", err, terminal.Sequence)
	}
	if _, err := j.ClaimDispatch(ctx, comparison(terminal)); !errors.Is(err, errConflict) {
		t.Fatalf("replay: %v", err)
	}
	if fenced, err := j.Fenced(ctx, r.Intent.TargetSHA256); err != nil || fenced {
		t.Fatalf("terminal fence: %v", err)
	}
	restored := reopen(t, f, j.header.Limits)
	got, err := restored.Lookup(ctx, r.Reservation.Owner, r.OperationID)
	if err != nil || got != terminal {
		t.Fatalf("restore: %v", err)
	}
}

func TestRestartRestoresFencesBeforeReadiness(t *testing.T) {
	for _, dispatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "never dispatched", true: "possibly dispatched"}[dispatch], func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := admitted(t, f.journal)
			if dispatch {
				var err error
				r, err = f.journal.ClaimDispatch(context.Background(), comparison(r))
				if err != nil {
					t.Fatal(err)
				}
			}
			j := reopen(t, f, Limits{})
			got, err := j.Lookup(context.Background(), r.Reservation.Owner, r.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			want := domain.BrokerOperationNotApplied
			if dispatch {
				want = domain.BrokerOperationOutcomeUnknown
			}
			if got.Phase != want || got.DispatchClaimed != dispatch {
				t.Fatalf("recovery=%+v", got)
			}
			fenced, err := j.Fenced(context.Background(), r.Intent.TargetSHA256)
			if err != nil || fenced != dispatch {
				t.Fatalf("fence=%t err=%v", fenced, err)
			}
			if _, err := j.ClaimDispatch(context.Background(), comparison(got)); !errors.Is(err, errConflict) {
				t.Fatalf("restart replay=%v", err)
			}
		})
	}
}

func TestOwnershipBindingsAndUnknownIDsAreClosed(t *testing.T) {
	f := newFixture(t, Limits{})
	r := reserveBound(t, f.journal)
	ctx := context.Background()
	counted := &countedReads{disk: f.journal.disk}
	f.journal.disk = counted
	wrong := r.Reservation.Owner
	wrong.WorkloadSHA256 = digest([]byte("other workload"))
	_, wrongErr := f.journal.Lookup(ctx, wrong, r.OperationID)
	_, missingErr := f.journal.Lookup(ctx, r.Reservation.Owner, digest([]byte("unknown")))
	if wrongErr != missingErr || !errors.Is(wrongErr, domain.ErrForbidden) {
		t.Fatalf("owner enumeration: %v / %v", wrongErr, missingErr)
	}
	if counted.reads != 0 {
		t.Fatal("unauthorized ownership caused journal payload I/O")
	}
	changes := []func(*domain.BrokerJournalIntent){
		func(i *domain.BrokerJournalIntent) { i.Ticket.ArgumentsSHA256 = digest([]byte("changed body/newline")) },
		func(i *domain.BrokerJournalIntent) { i.Ticket.ProposalSHA256 = digest([]byte("changed proposal")) },
		func(i *domain.BrokerJournalIntent) { i.Ticket.BackendSHA256 = digest([]byte("changed backend")) },
		func(i *domain.BrokerJournalIntent) { i.Ticket.ExecutionSHA256 = digest([]byte("changed execution")) },
		func(i *domain.BrokerJournalIntent) { i.TargetSHA256 = digest([]byte("changed target")) },
		func(i *domain.BrokerJournalIntent) { i.NativeSHA256 = digest([]byte("changed native")) },
		func(i *domain.BrokerJournalIntent) { i.EffectSHA256 = digest([]byte("changed effect")) },
		func(i *domain.BrokerJournalIntent) { i.EvidenceSHA256 = digest([]byte("changed version")) },
	}
	for _, change := range changes {
		intent := r.Intent
		change(&intent)
		if _, err := f.journal.Bind(ctx, r.Reservation.Owner, r.OperationID, intent); !errors.Is(err, errConflict) {
			t.Fatalf("binding conflict=%v", err)
		}
	}
	if got, err := f.journal.Bind(ctx, r.Reservation.Owner, r.OperationID, r.Intent); err != nil || got != r {
		t.Fatalf("exact retry=%v", err)
	}
}

type countedReads struct {
	disk
	reads int
}

func (d *countedReads) read(name string, size int64) ([]byte, error) {
	d.reads++
	return d.disk.read(name, size)
}

func TestFixedClockDeadlinesAndIndependentObservation(t *testing.T) {
	f := newFixture(t, Limits{})
	r := admitted(t, f.journal)
	f.clock.Store(r.AcceptUntilMillis)
	if _, err := f.journal.ClaimDispatch(context.Background(), comparison(r)); !errors.Is(err, errExpired) {
		t.Fatalf("hard expiry=%v", err)
	}
	f.clock.Store(r.AcceptUntilMillis - 1)
	if _, err := f.journal.ClaimDispatch(context.Background(), comparison(r)); !errors.Is(err, errExpired) {
		t.Fatalf("within-allowance rollback revived expiry=%v", err)
	}
	// Lookup has stable ownership only: no old writer execution context is
	// revived or required. A future app must authorize its current observer.
	got, err := f.journal.Lookup(context.Background(), r.Reservation.Owner, r.OperationID)
	if err != nil || got != r {
		t.Fatalf("observe expired writer=%v", err)
	}
	f.clock.Store(testMillis - domain.BrokerClockAllowanceMillis - 1)
	if _, err := f.journal.Reserve(context.Background(), testReservation()); !errors.Is(err, errExpired) {
		t.Fatalf("rollback=%v", err)
	}
	f.clock.Store(testMillis)
	if _, err := f.journal.ClaimDispatch(context.Background(), comparison(r)); !errors.Is(err, errExpired) {
		t.Fatalf("observed expiry was revived after rollback=%v", err)
	}
	f.clock.Store(r.Reservation.ObservationUntilMillis)
	if _, err := f.journal.Lookup(context.Background(), r.Reservation.Owner, r.OperationID); !errors.Is(err, errDenied) {
		t.Fatalf("observation deadline=%v", err)
	}
	f.clock.Store(r.Reservation.ObservationUntilMillis - 1)
	if _, err := f.journal.Lookup(context.Background(), r.Reservation.Owner, r.OperationID); !errors.Is(err, errDenied) {
		t.Fatalf("within-allowance observation rollback=%v", err)
	}
	f.clock.Store(testMillis)
	if _, err := f.journal.Lookup(context.Background(), r.Reservation.Owner, r.OperationID); !errors.Is(err, errDenied) {
		t.Fatalf("observation expiry revived=%v", err)
	}
	if _, err := f.journal.ReadArtifact(context.Background(), r.Reservation.Owner, r.OperationID); !errors.Is(err, errDenied) {
		t.Fatalf("artifact observation expiry revived=%v", err)
	}
}

func TestDurableClockHighWaterSurvivesRestart(t *testing.T) {
	f := newFixture(t, Limits{})
	r := reserveBound(t, f.journal)
	f.clock.Store(testMillis + 10000)
	artifact := []byte("native and baseline")
	r, err := f.journal.Admit(context.Background(), comparison(r), artifact, digest(artifact))
	must(t, err)
	f.clock.Store(testMillis)
	j := reopen(t, f, Limits{})
	if _, err := j.Reserve(context.Background(), testReservation()); !errors.Is(err, errExpired) {
		t.Fatalf("persisted high-water lost=%v", err)
	}
	got, err := j.Lookup(context.Background(), r.Reservation.Owner, r.OperationID)
	must(t, err)
	if got.RecordedAtMillis < testMillis+10000 {
		t.Fatal("recovery moved durable clock backward")
	}
}

func TestPrivateMetadataAndSeparateArtifact(t *testing.T) {
	f := newFixture(t, Limits{})
	r := admitted(t, f.journal)
	metadata, err := os.ReadFile(filepath.Join(f.path, r.OperationID+".rec"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"native\n; qualified baseline", "principal", "backend/immutable issue/comments"} {
		if bytes.Contains(metadata, []byte(sensitive)) {
			t.Fatal("content leaked into metadata")
		}
	}
	artifact, err := f.journal.ReadArtifact(context.Background(), r.Reservation.Owner, r.OperationID)
	if err != nil || string(artifact) != "native\n; qualified baseline" {
		t.Fatalf("artifact=%v", err)
	}
	for _, err := range []error{errUnavailable, errConflict, errDenied, errInvalid, errExpired, errCapacity} {
		if strings.Contains(err.Error(), f.path) {
			t.Fatal("private path leaked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.journal.ClaimDispatch(ctx, comparison(r)); !errors.Is(err, errDenied) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestTargetFenceCrossesPrincipalsButNotIndependentTargets(t *testing.T) {
	f := newFixture(t, Limits{})
	first := admitted(t, f.journal)
	reservation := testReservation()
	reservation.Owner.PrincipalSHA256 = digest([]byte("another principal"))
	second, err := f.journal.Reserve(context.Background(), reservation)
	must(t, err)
	second, err = f.journal.Bind(context.Background(), second.Reservation.Owner, second.OperationID, testIntent(second))
	must(t, err)
	artifact := []byte("candidate and baseline")
	if _, err := f.journal.Admit(context.Background(), comparison(second), artifact, digest(artifact)); !errors.Is(err, errConflict) {
		t.Fatalf("cross-principal active fence=%v", err)
	}
	first, err = f.journal.ClaimDispatch(context.Background(), comparison(first))
	must(t, err)
	_, err = f.journal.Complete(context.Background(), comparison(first), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
	must(t, err)
	if _, err := f.journal.Admit(context.Background(), comparison(second), artifact, digest(artifact)); !errors.Is(err, errConflict) {
		t.Fatalf("cross-principal unknown fence=%v", err)
	}
	third, err := f.journal.Reserve(context.Background(), reservation)
	must(t, err)
	intent := testIntent(third)
	intent.TargetSHA256 = digest([]byte("independent backend-bound issue"))
	third, err = f.journal.Bind(context.Background(), third.Reservation.Owner, third.OperationID, intent)
	must(t, err)
	if _, err := f.journal.Admit(context.Background(), comparison(third), artifact, digest(artifact)); err != nil {
		t.Fatalf("independent target blocked=%v", err)
	}
}
