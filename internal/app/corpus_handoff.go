package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const CorpusHandoffResultSchemaV1 = 1

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

// PrepareCorpusHandoff verifies the selected sealed generation and identifies
// its one canonical indexer document inventory. It performs no backend I/O and
// never mutates the store or replaces an existing artifact.
func PrepareCorpusHandoff(ctx context.Context, options CorpusHandoffOptions) (*CorpusHandoffResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: corpus handoff requires a context", domain.ErrUsage)
	}
	if strings.TrimSpace(options.StoreRoot) == "" {
		return nil, fmt.Errorf("%w: corpus handoff requires --store", domain.ErrUsage)
	}
	store, err := corpus.Open(options.StoreRoot, corpus.Options{Limits: options.Limits})
	if err != nil {
		return nil, corpusHandoffFailure("open sealed generation store", err)
	}
	defer func() { _ = store.Close() }()
	generation, err := store.SelectCurrent(ctx)
	if err != nil {
		return nil, corpusHandoffFailure("select current generation", err)
	}
	defer func() { _ = generation.Close() }()

	manifest := generation.Manifest()
	if manifest.ProjectionSchema != corpus.IndexerSchemaV2 {
		return nil, corpusHandoffFailure("qualify current projection", corpus.ErrIntegrity)
	}
	if _, qualified, qualificationErr := loadCorpusDeltaGeneration(ctx, generation, options.Limits); qualificationErr != nil || !qualified {
		if qualificationErr == nil {
			qualificationErr = corpus.ErrIntegrity
		}
		return nil, corpusHandoffFailure("qualify current projection", qualificationErr)
	}
	var documents corpus.Member
	matches := 0
	for _, member := range manifest.Members {
		if member.StableID == corpus.IndexerDocumentsStableID {
			matches++
			if member.Role != corpus.RoleDocument {
				return nil, corpusHandoffFailure("qualify document inventory", corpus.ErrIntegrity)
			}
			documents = member
		}
	}
	if matches != 1 {
		return nil, corpusHandoffFailure("qualify document inventory", corpus.ErrIntegrity)
	}
	handoff, err := corpus.BuildIndexerHandoff(
		generation.ID(), generation.Receipt().GenerationDigest, manifest.ProjectionSchema, documents, options.Limits,
	)
	if err != nil {
		return nil, corpusHandoffFailure("build sealed handoff", err)
	}
	written := false
	if options.HandoffArtifact != "" {
		data, encodeErr := corpus.CanonicalIndexerHandoff(handoff, options.Limits)
		if encodeErr != nil {
			return nil, corpusHandoffFailure("encode sealed handoff", encodeErr)
		}
		if writeErr := safepath.WriteFileExclusivePrivateOutsideRoot(options.StoreRoot, options.HandoffArtifact, data, 0o600); writeErr != nil {
			return nil, corpusHandoffFailure("write exclusive handoff artifact", writeErr)
		}
		written = true
	}
	return &CorpusHandoffResult{
		SchemaVersion: CorpusHandoffResultSchemaV1, Qualification: "sealed",
		Generation: generation.Summary(), HandoffArtifactWritten: written,
	}, nil
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
