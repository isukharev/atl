package brokerjournal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

// Journal owns one local filesystem writer until Close. An ambiguous durable
// operation poisons the instance, including reads; reopen validates everything
// and restores fences before returning a usable instance.
type Journal struct {
	mu             sync.Mutex
	disk           disk
	header         header
	records        map[string]domain.BrokerJournalRecord
	reservations   []reservationEntry
	fences         map[string]string
	reserved       int64
	highWater      int64
	failed, closed bool
	now            func() time.Time
	hook           func(string) error // package-private crash checkpoints
}

var _ domain.BrokerJournal = (*Journal)(nil)

// Create requires an absent root beneath an existing current-owner 0700 parent.
// It never adopts or repairs existing storage. A missing root on Open is an
// error; callers must not automatically fall back from Open to Create.
func Create(path string, identity Identity, limits Limits) (*Journal, error) {
	return openJournal(path, identity, limits, true, time.Now)
}

// Open reopens only the exact existing deployment identity and limits. It does
// not reconstruct authority or contact any backend during recovery.
func Open(path string, identity Identity, limits Limits) (*Journal, error) {
	return openJournal(path, identity, limits, false, time.Now)
}

func openJournal(path string, identity Identity, limits Limits, create bool, now func() time.Time) (*Journal, error) {
	limits, err := normalizeLimits(limits)
	if err != nil || !validDigest(identity.BrokerSHA256) || !validDigest(identity.BackendSHA256) {
		return nil, errInvalid
	}
	d, err := openDisk(path, create)
	if err != nil {
		return nil, err
	}
	j := &Journal{disk: d, records: make(map[string]domain.BrokerJournalRecord), fences: make(map[string]string), reserved: initialBytes(limits), now: now}
	ok := false
	defer func() {
		if !ok {
			_ = d.close()
		}
	}()
	if create {
		j.header = header{1, identity, limits, now().UnixMilli()}
		if j.header.CreatedAtMillis <= 0 {
			return nil, errInvalid
		}
		data, encodeErr := encodeSlot(j.header)
		if encodeErr != nil || d.allocate("reservations", int64(limits.Records)*indexSlotBytes) != nil || d.write("identity", 0, data) != nil {
			return nil, errUnavailable
		}
	} else {
		data, readErr := d.read("identity", slotBytes)
		if readErr != nil || decodeSlot(data, &j.header) != nil || j.header.Version != 1 || j.header.Identity != identity || j.header.Limits != limits || j.header.CreatedAtMillis <= 0 {
			return nil, errUnavailable
		}
	}
	j.highWater = j.header.CreatedAtMillis
	if j.recover() != nil {
		return nil, errUnavailable
	}
	ok = true
	return j, nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return j.disk.close()
}

func (j *Journal) usable(ctx context.Context) error {
	if j.closed || j.failed {
		return errUnavailable
	}
	if ctx == nil || ctx.Err() != nil {
		return errDenied
	}
	if j.disk.check() != nil {
		return j.poison()
	}
	return nil
}

func (j *Journal) poison() error { j.failed = true; return errUnavailable }

func (j *Journal) checkpoint(point string) error {
	if j.hook != nil && j.hook(point) != nil {
		return j.poison()
	}
	return nil
}

func (j *Journal) writeTime() (int64, error) {
	now := j.now().UnixMilli()
	previous := j.highWater
	j.highWater = max(previous, now)
	if now <= 0 || now < previous-domain.BrokerClockAllowanceMillis {
		return 0, errExpired
	}
	return j.highWater, nil
}

// Every observation sticks for this open instance, including a refused
// expiry check. A backward clock cannot make an already observed deadline
// admissible again. Successful durable transitions retain the high-water.
func (j *Journal) observeTime() int64 {
	j.highWater = max(j.highWater, j.now().UnixMilli())
	return j.highWater
}

func (j *Journal) writerTime(r domain.BrokerJournalRecord, acceptance bool) (int64, error) {
	now, err := j.writeTime()
	deadline := r.Reservation.OperationDeadlineMillis
	if acceptance {
		deadline = r.AcceptUntilMillis
	}
	if err != nil || now < r.Reservation.ExecutionNotBeforeMillis || now >= deadline {
		return 0, errExpired
	}
	return now, nil
}

// Reserve returns an ID only after both its preallocated files and first
// metadata slot are durable. Failed issuance leaves residue, never deletion.
func (j *Journal) Reserve(ctx context.Context, reservation domain.BrokerJournalReservation) (domain.BrokerJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.usable(ctx); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	now, err := j.writeTime()
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	accept := min(now+domain.BrokerMaxOperationMillis, reservation.ExecutionExpiresMillis, reservation.GrantExpiresMillis, reservation.CredentialExpiresMillis, reservation.OperationDeadlineMillis)
	if !validReservation(reservation, now, accept) || reservation.Owner.BrokerSHA256 != j.header.Identity.BrokerSHA256 || reservation.Owner.BackendSHA256 != j.header.Identity.BackendSHA256 {
		return domain.BrokerJournalRecord{}, errInvalid
	}
	cost := int64(recordBytes+stateBytes) + reservation.ArtifactCapacity
	if len(j.records) >= j.header.Limits.Records || cost > j.header.Limits.ReservedBytes-j.reserved {
		return domain.BrokerJournalRecord{}, errCapacity
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return domain.BrokerJournalRecord{}, errUnavailable
	}
	id := hex.EncodeToString(random[:])
	if _, exists := j.records[id]; exists {
		return domain.BrokerJournalRecord{}, j.poison()
	}
	r := domain.BrokerJournalRecord{OperationID: id, Reservation: reservation, IssuedAtMillis: now, AcceptUntilMillis: accept, Sequence: 1, RecordedAtMillis: max(now, j.highWater)}
	if j.disk.allocate(id+".rec", recordBytes) != nil || j.disk.allocate(id+".art", reservation.ArtifactCapacity) != nil || j.disk.allocate(id+".state", stateBytes) != nil || j.checkpoint("reservation_allocated") != nil {
		return domain.BrokerJournalRecord{}, j.poison()
	}
	if err := j.indexReservation(r); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if err := j.persist(r); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	j.reserved += cost
	if j.checkpoint("reservation_durable") != nil {
		return domain.BrokerJournalRecord{}, errUnavailable
	}
	return r, nil
}

// Bind closes the ID/arguments ordering without a callback: the trusted caller
// hashes prospective apply arguments only after Reserve has minted its ID.
func (j *Journal) Bind(ctx context.Context, owner domain.BrokerJournalOwner, id string, intent domain.BrokerJournalIntent) (domain.BrokerJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, err := j.lookup(ctx, owner, id)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if r.BindingSHA256 != "" {
		if r.Intent == intent {
			return r, nil
		}
		return domain.BrokerJournalRecord{}, errConflict
	}
	now, err := j.writerTime(r, true)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	binding, err := intentBinding(r, intent)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	r.Intent, r.BindingSHA256 = intent, binding
	r.Sequence++
	r.RecordedAtMillis = max(now, j.highWater)
	return j.save(r, "binding_durable")
}

func (j *Journal) lookup(ctx context.Context, owner domain.BrokerJournalOwner, id string) (domain.BrokerJournalRecord, error) {
	if err := j.usable(ctx); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	r, exists := j.records[id]
	if !validOwner(owner) || !validDigest(id) || !exists || r.Reservation.Owner != owner {
		return domain.BrokerJournalRecord{}, errDenied
	}
	if err := j.checkReservations(); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	// Compare the durable slot chain too; a cached state is not proof against
	// in-place corruption or replacement by another local actor.
	onDisk, err := j.loadRecord(id)
	if err != nil || onDisk != r {
		return domain.BrokerJournalRecord{}, j.poison()
	}
	return r, nil
}

func (j *Journal) compare(ctx context.Context, expected domain.BrokerJournalCompare) (domain.BrokerJournalRecord, error) {
	r, err := j.lookup(ctx, expected.Owner, expected.OperationID)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if expected.Sequence != r.Sequence || expected.BindingSHA256 == "" || expected.BindingSHA256 != r.BindingSHA256 {
		return domain.BrokerJournalRecord{}, errConflict
	}
	return r, nil
}

func (j *Journal) Lookup(ctx context.Context, owner domain.BrokerJournalOwner, id string) (domain.BrokerJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, err := j.lookup(ctx, owner, id)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if j.observeTime() >= r.Reservation.ObservationUntilMillis {
		return domain.BrokerJournalRecord{}, errDenied
	}
	return r, nil
}

func (j *Journal) ReadArtifact(ctx context.Context, owner domain.BrokerJournalOwner, id string) ([]byte, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, err := j.lookup(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if j.observeTime() >= r.Reservation.ObservationUntilMillis || r.ArtifactSHA256 == "" {
		return nil, errDenied
	}
	return j.artifact(r)
}

func (j *Journal) Fenced(ctx context.Context, target string) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.usable(ctx); err != nil {
		return false, err
	}
	if !validDigest(target) {
		return false, errInvalid
	}
	_, exists := j.fences[target]
	return exists, nil
}

func (j *Journal) save(r domain.BrokerJournalRecord, checkpoint string) (domain.BrokerJournalRecord, error) {
	if err := j.persist(r); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if err := j.checkpoint(checkpoint); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	return r, nil
}

func (j *Journal) persist(r domain.BrokerJournalRecord) error {
	if validateRecord(r) != nil {
		return j.poison()
	}
	if before, exists := j.records[r.OperationID]; exists && !validSuccessor(before, r) {
		return j.poison()
	}
	data, err := encodeSlot(snapshot{1, r})
	offset := int64(r.Sequence-1) * slotBytes // #nosec G115 -- validateRecord above bounds Sequence to 1..8.
	if err != nil || j.disk.write(r.OperationID+".rec", offset, data) != nil {
		return j.poison()
	}
	if err := j.commitState(r, data); err != nil {
		return err
	}
	j.records[r.OperationID] = r
	j.highWater = max(j.highWater, r.RecordedAtMillis)
	return nil
}
