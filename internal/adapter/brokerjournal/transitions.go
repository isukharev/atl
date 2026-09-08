package brokerjournal

import (
	"context"

	"github.com/isukharev/atl/internal/domain"
)

func (j *Journal) Admit(ctx context.Context, expected domain.BrokerJournalCompare, artifact []byte, artifactSHA256 string) (domain.BrokerJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, err := j.compare(ctx, expected)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if r.Phase != "" {
		return domain.BrokerJournalRecord{}, errConflict
	}
	now, err := j.writerTime(r, true)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if len(artifact) < 1 || int64(len(artifact)) > r.Reservation.ArtifactCapacity || !validDigest(artifactSHA256) || digest(artifact) != artifactSHA256 {
		return domain.BrokerJournalRecord{}, errInvalid
	}
	if _, fenced := j.fences[r.Intent.TargetSHA256]; fenced {
		return domain.BrokerJournalRecord{}, errConflict
	}
	before, err := j.disk.read(r.OperationID+".art", r.Reservation.ArtifactCapacity)
	if err != nil || !zero(before) {
		return domain.BrokerJournalRecord{}, j.poison()
	}
	if j.disk.write(r.OperationID+".art", 0, artifact) != nil || j.checkpoint("artifact_durable") != nil {
		return domain.BrokerJournalRecord{}, j.poison()
	}
	r.ArtifactSHA256, r.ArtifactBytes = artifactSHA256, int64(len(artifact))
	r.Phase, r.Sequence, r.RecordedAtMillis = domain.BrokerOperationAdmitted, r.Sequence+1, max(now, j.highWater)
	if err := j.persist(r); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	j.fences[r.Intent.TargetSHA256] = r.OperationID
	if j.checkpoint("admitted_durable") != nil {
		return domain.BrokerJournalRecord{}, errUnavailable
	}
	return r, nil
}

func (j *Journal) ClaimDispatch(ctx context.Context, expected domain.BrokerJournalCompare) (domain.BrokerJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, err := j.compare(ctx, expected)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if r.Phase != domain.BrokerOperationAdmitted || r.DispatchClaimed || j.fences[r.Intent.TargetSHA256] != r.OperationID {
		return domain.BrokerJournalRecord{}, errConflict
	}
	if _, err := j.artifact(r); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	now, err := j.writerTime(r, false)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	r.Phase, r.DispatchClaimed = domain.BrokerOperationDispatching, true
	r.Sequence++
	r.RecordedAtMillis = max(now, j.highWater)
	// There is deliberately no successful idempotent replay of this method.
	// Its only success is one newly fsynced dispatch claim.
	if _, err := j.save(r, "dispatch_durable"); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if _, err := j.writerTime(r, false); err != nil {
		// The durable claim remains consumed and fenced even though no right
		// is returned after expiry/rollback during a slow fsync.
		return domain.BrokerJournalRecord{}, err
	}
	if ctx.Err() != nil {
		return domain.BrokerJournalRecord{}, errDenied
	}
	return r, nil
}

func (j *Journal) Complete(ctx context.Context, expected domain.BrokerJournalCompare, completion domain.BrokerJournalCompletion) (domain.BrokerJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, err := j.compare(ctx, expected)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if r.Phase == completion.Phase && r.ResultSHA256 == completion.ResultSHA256 && r.Phase != domain.BrokerOperationAdmitted && r.Phase != domain.BrokerOperationDispatching && r.Phase != "" {
		return r, nil
	}
	next := r
	next.Phase, next.ResultSHA256 = completion.Phase, completion.ResultSHA256
	next.Sequence++
	// Closeout records conservative evidence even after the writer expires or
	// time rolls back. It grants no send and never moves the durable clock back.
	next.RecordedAtMillis = j.observeTime()
	if validateRecord(next) != nil || !validSuccessor(r, next) {
		return domain.BrokerJournalRecord{}, errConflict
	}
	if next.Phase == domain.BrokerOperationNotApplied && r.DispatchClaimed && !validDigest(next.ResultSHA256) {
		return domain.BrokerJournalRecord{}, errInvalid
	}
	if err := j.persist(next); err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	if next.Phase != domain.BrokerOperationOutcomeUnknown {
		delete(j.fences, next.Intent.TargetSHA256)
	}
	if j.checkpoint("terminal_durable") != nil {
		return domain.BrokerJournalRecord{}, errUnavailable
	}
	return next, nil
}

func (j *Journal) artifact(r domain.BrokerJournalRecord) ([]byte, error) {
	data, err := j.disk.read(r.OperationID+".art", r.Reservation.ArtifactCapacity)
	if err != nil || r.ArtifactBytes < 1 || r.ArtifactBytes > int64(len(data)) || !zero(data[r.ArtifactBytes:]) || digest(data[:r.ArtifactBytes]) != r.ArtifactSHA256 {
		return nil, j.poison()
	}
	return data[:r.ArtifactBytes:r.ArtifactBytes], nil
}
