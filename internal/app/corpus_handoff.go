package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const CorpusHandoffResultSchemaV1 = 1
const QualifiedCorpusHandoffResultSchemaV1 = 1

type CorpusHandoffOptions struct {
	StoreRoot       string
	HandoffArtifact string
	Limits          corpus.Limits
}

// CorpusHandoffResult is content-free. The generation ID and exact member
// route are written only to the explicitly requested owner-private artifact.
type CorpusHandoffResult struct {
	SchemaVersion          int            `json:"schema_version"`
	Qualification          string         `json:"qualification"`
	Generation             corpus.Summary `json:"generation"`
	HandoffArtifactWritten bool           `json:"handoff_artifact_written"`
}

type QualifiedCorpusHandoffOptions struct {
	StoreRoot        string
	HandoffArtifact  string
	GeneratorVersion string
	GeneratorCommit  string
	BuildState       corpus.BuildState
	Limits           corpus.Limits
}

type QualifiedCorpusHandoffResult struct {
	SchemaVersion          int            `json:"schema_version"`
	Qualification          string         `json:"qualification"`
	ExpiresAtMillis        int64          `json:"expires_at_millis"`
	Generation             corpus.Summary `json:"generation"`
	HandoffArtifactWritten bool           `json:"handoff_artifact_written"`
}

type corpusHandoffSelection struct {
	store      *corpus.Store
	generation *corpus.Generation
	handoff    corpus.IndexerHandoff
}

// PrepareCorpusHandoff verifies the selected sealed generation and identifies
// its one canonical indexer document inventory. It performs no backend I/O and
// never mutates the store or replaces an existing artifact.
func PrepareCorpusHandoff(ctx context.Context, options CorpusHandoffOptions) (*CorpusHandoffResult, error) {
	selection, err := openCorpusHandoffSelection(ctx, options.StoreRoot, options.Limits)
	if err != nil {
		return nil, err
	}
	defer selection.close()
	written, err := writeCorpusHandoffArtifact(options.StoreRoot, options.HandoffArtifact, selection.handoff, options.Limits)
	if err != nil {
		return nil, err
	}
	return &CorpusHandoffResult{
		SchemaVersion: CorpusHandoffResultSchemaV1, Qualification: "sealed",
		Generation: selection.generation.Summary(), HandoffArtifactWritten: written,
	}, nil
}

// PrepareQualifiedCorpusHandoff performs a current Broker qualification for
// one already sealed generation. It never refreshes, reseals, publishes, or
// changes the current pointer.
func PrepareQualifiedCorpusHandoff(ctx context.Context, options QualifiedCorpusHandoffOptions, reader domain.BrokerCacheQualificationReader) (*QualifiedCorpusHandoffResult, error) {
	return prepareQualifiedCorpusHandoff(ctx, options, reader, writeCorpusHandoffArtifact, (*corpus.Store).ConfirmCurrent)
}

type corpusHandoffArtifactWriter func(string, string, corpus.IndexerHandoff, corpus.Limits) (bool, error)
type corpusHandoffCurrentConfirmer func(*corpus.Store, context.Context, *corpus.Generation) error

func prepareQualifiedCorpusHandoff(ctx context.Context, options QualifiedCorpusHandoffOptions, reader domain.BrokerCacheQualificationReader, writeArtifact corpusHandoffArtifactWriter, confirmCurrent corpusHandoffCurrentConfirmer) (*QualifiedCorpusHandoffResult, error) {
	if ctx == nil || reader == nil || strings.TrimSpace(options.GeneratorVersion) == "" || options.BuildState != corpus.BuildStateClean {
		return nil, fmt.Errorf("%w: qualified corpus handoff requires a context, Broker qualifier, and clean generator identity", domain.ErrUsage)
	}
	if writeArtifact == nil || confirmCurrent == nil {
		return nil, fmt.Errorf("%w: qualified corpus handoff requires local lifecycle operations", domain.ErrUsage)
	}
	first, err := openCorpusHandoffSelection(ctx, options.StoreRoot, options.Limits)
	if err != nil {
		return nil, qualifiedCorpusHandoffFailure("verify current generation", err)
	}
	candidate, err := qualifiedCorpusCandidate(ctx, first, options)
	if err == nil {
		err = confirmCurrent(first.store, ctx, first.generation)
	}
	first.close()
	if err != nil {
		return nil, qualifiedCorpusHandoffFailure("qualify current generation", err)
	}
	grant, err := reader.QualifyCache(ctx, candidate)
	if err != nil {
		return nil, qualifiedCorpusHandoffFailure("authorize current generation", err)
	}
	if err := reader.ValidateCacheGrant(candidate, grant); err != nil || !grant.Lease.Active(time.Now()) {
		return nil, qualifiedCorpusHandoffFailure("validate current authorization", firstBrokerReadError(err, context.DeadlineExceeded))
	}
	second, err := openCorpusHandoffSelection(ctx, options.StoreRoot, options.Limits)
	if err != nil {
		return nil, qualifiedCorpusHandoffFailure("reverify current generation", err)
	}
	defer second.close()
	confirmed, err := qualifiedCorpusCandidate(ctx, second, options)
	if err == nil && confirmed != candidate {
		err = corpus.ErrIntegrity
	}
	if err == nil {
		err = confirmCurrent(second.store, ctx, second.generation)
	}
	if err == nil {
		err = reader.ValidateCacheGrant(candidate, grant)
	}
	if err != nil || !grant.Lease.Active(time.Now()) {
		return nil, qualifiedCorpusHandoffFailure("revalidate current authorization", firstBrokerReadError(err, context.DeadlineExceeded))
	}
	if err := ctx.Err(); err != nil {
		return nil, qualifiedCorpusHandoffFailure("start exclusive handoff artifact", err)
	}
	written, err := writeArtifact(options.StoreRoot, options.HandoffArtifact, second.handoff, options.Limits)
	if err != nil {
		return nil, qualifiedCorpusHandoffFailure("write exclusive handoff artifact", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, qualifiedCorpusHandoffFailure("confirm current authorization after handoff", err)
	}
	if err := confirmCurrent(second.store, ctx, second.generation); err != nil {
		return nil, qualifiedCorpusHandoffFailure("confirm current generation after handoff", err)
	}
	if err := reader.ValidateCacheGrant(candidate, grant); err != nil {
		return nil, qualifiedCorpusHandoffFailure("revalidate current authorization after rescan", err)
	}
	if err := ctx.Err(); err != nil || !grant.Lease.Active(time.Now()) {
		return nil, qualifiedCorpusHandoffFailure("finish current handoff", firstBrokerReadError(err, context.DeadlineExceeded))
	}
	return &QualifiedCorpusHandoffResult{SchemaVersion: QualifiedCorpusHandoffResultSchemaV1, Qualification: "current_allowed", ExpiresAtMillis: grant.ReleaseDeadlineMillis, Generation: second.generation.Summary(), HandoffArtifactWritten: written}, nil
}

func openCorpusHandoffSelection(ctx context.Context, storeRoot string, limits corpus.Limits) (*corpusHandoffSelection, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: corpus handoff requires a context", domain.ErrUsage)
	}
	if strings.TrimSpace(storeRoot) == "" {
		return nil, fmt.Errorf("%w: corpus handoff requires --store", domain.ErrUsage)
	}
	store, err := corpus.Open(storeRoot, corpus.Options{Limits: limits})
	if err != nil {
		return nil, corpusHandoffFailure("open sealed generation store", err)
	}
	generation, err := store.SelectCurrent(ctx)
	if err != nil {
		_ = store.Close()
		return nil, corpusHandoffFailure("select current generation", err)
	}
	selection := &corpusHandoffSelection{store: store, generation: generation}
	manifest := generation.Manifest()
	if manifest.ProjectionSchema != corpus.IndexerSchemaV2 {
		selection.close()
		return nil, corpusHandoffFailure("qualify current projection", corpus.ErrIntegrity)
	}
	if _, qualified, qualificationErr := loadCorpusDeltaGeneration(ctx, generation, limits); qualificationErr != nil || !qualified {
		if qualificationErr == nil {
			qualificationErr = corpus.ErrIntegrity
		}
		selection.close()
		return nil, corpusHandoffFailure("qualify current projection", qualificationErr)
	}
	var documents corpus.Member
	matches := 0
	for _, member := range manifest.Members {
		if member.StableID == corpus.IndexerDocumentsStableID {
			matches++
			if member.Role != corpus.RoleDocument {
				selection.close()
				return nil, corpusHandoffFailure("qualify document inventory", corpus.ErrIntegrity)
			}
			documents = member
		}
	}
	if matches != 1 {
		selection.close()
		return nil, corpusHandoffFailure("qualify document inventory", corpus.ErrIntegrity)
	}
	selection.handoff, err = corpus.BuildIndexerHandoff(generation.ID(), generation.Receipt().GenerationDigest, manifest.ProjectionSchema, documents, limits)
	if err != nil {
		selection.close()
		return nil, corpusHandoffFailure("build sealed handoff", err)
	}
	return selection, nil
}

func qualifiedCorpusCandidate(ctx context.Context, selection *corpusHandoffSelection, options QualifiedCorpusHandoffOptions) (domain.BrokerCacheCandidate, error) {
	manifest := selection.generation.Manifest()
	if len(manifest.Qualifications) != 1 || manifest.Qualifications[0].Service != corpus.ServiceConfluence || manifest.BuildState != corpus.BuildStateClean || manifest.GeneratorVersion != options.GeneratorVersion {
		return domain.BrokerCacheCandidate{}, corpus.ErrIntegrity
	}
	binding, err := corpus.LoadCacheBindingV1(ctx, selection.generation)
	if err != nil || !binding.Reusable || corpus.VerifyCacheBindingV1(binding, selection.generation) != nil || ctx.Err() != nil {
		return domain.BrokerCacheCandidate{}, corpus.ErrIntegrity
	}
	generatorDigest, err := corpus.GeneratorIdentityDigest(options.GeneratorVersion, options.GeneratorCommit, options.BuildState)
	if err != nil || binding.GeneratorDigest != generatorDigest {
		return domain.BrokerCacheCandidate{}, corpus.ErrIntegrity
	}
	projection, qualified, err := loadCorpusDeltaGeneration(ctx, selection.generation, options.Limits)
	capture, present := projection.captures[corpus.ServiceConfluence]
	if err != nil || !qualified || !present || len(capture.Dimensions) != 4 ||
		capture.Dimensions[0] != (corpus.CaptureDimensionEvidence{Dimension: corpus.CaptureAttachments, State: corpus.CaptureNotRequested}) ||
		capture.Dimensions[1] != (corpus.CaptureDimensionEvidence{Dimension: corpus.CaptureComments, State: corpus.CaptureNotRequested}) ||
		capture.Dimensions[2] != (corpus.CaptureDimensionEvidence{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete}) ||
		capture.Dimensions[3] != (corpus.CaptureDimensionEvidence{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete}) {
		return domain.BrokerCacheCandidate{}, corpus.ErrIntegrity
	}
	return domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: capture.SelectorDigest, ProjectionSHA256: projection.receipt.ProjectionDigest, EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: selection.generation.Receipt().GenerationDigest, ContentSHA256: capture.SnapshotDigest}, nil
}

func writeCorpusHandoffArtifact(storeRoot, artifact string, handoff corpus.IndexerHandoff, limits corpus.Limits) (bool, error) {
	if artifact == "" {
		return false, nil
	}
	data, err := corpus.CanonicalIndexerHandoff(handoff, limits)
	if err != nil {
		return false, corpusHandoffFailure("encode sealed handoff", err)
	}
	if err := safepath.WriteFileExclusivePrivateOutsideRoot(storeRoot, artifact, data, 0o600); err != nil {
		return false, corpusHandoffFailure("write exclusive handoff artifact", err)
	}
	return true, nil
}

func (selection *corpusHandoffSelection) close() {
	if selection == nil {
		return
	}
	if selection.generation != nil {
		_ = selection.generation.Close()
		selection.generation = nil
	}
	if selection.store != nil {
		_ = selection.store.Close()
		selection.store = nil
	}
}

func qualifiedCorpusHandoffFailure(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	sentinel := domain.ErrCheckFailed
	for _, candidate := range []error{domain.ErrUsage, domain.ErrConfig, domain.ErrAuth, domain.ErrForbidden, domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted} {
		if errors.Is(err, candidate) {
			sentinel = candidate
			break
		}
	}
	return fmt.Errorf("%w: qualified corpus handoff could not %s", sentinel, operation)
}

func corpusHandoffFailure(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fmt.Errorf("%w: corpus handoff could not %s", domain.ErrCheckFailed, operation)
}
