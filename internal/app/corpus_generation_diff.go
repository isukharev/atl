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

const CorpusGenerationDiffSchemaV1 = 1

type CorpusGenerationDiffOptions struct {
	StoreRoot        string
	IdentityArtifact string
	Limits           corpus.Limits
}

// CorpusGenerationDiffResult is content-free. Stable object identities and
// service bindings remain in the sealed delta and, when explicitly requested,
// one exclusive owner-private artifact.
type CorpusGenerationDiffResult struct {
	SchemaVersion               int                          `json:"schema_version"`
	Qualification               string                       `json:"qualification"`
	Reason                      corpus.GenerationDeltaReason `json:"reason,omitempty"`
	Counts                      corpus.GenerationDeltaCounts `json:"counts"`
	PredecessorGenerationDigest string                       `json:"predecessor_generation_digest"`
	SuccessorGenerationDigest   string                       `json:"successor_generation_digest"`
	TombstoneDigest             string                       `json:"tombstone_digest"`
	IdentityArtifactWritten     bool                         `json:"identity_artifact_written"`
}

// DiffCorpusGeneration verifies the current sealed generation, its exact
// predecessor, and the canonical qualified membership delta between them. It
// performs no backend I/O and never removes or replaces local state.
func DiffCorpusGeneration(ctx context.Context, options CorpusGenerationDiffOptions) (*CorpusGenerationDiffResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: corpus diff requires a context", domain.ErrUsage)
	}
	if strings.TrimSpace(options.StoreRoot) == "" {
		return nil, fmt.Errorf("%w: corpus diff requires --store", domain.ErrUsage)
	}
	store, err := corpus.Open(options.StoreRoot, corpus.Options{Limits: options.Limits})
	if err != nil {
		return nil, corpusGenerationDiffFailure("open sealed generation store", err)
	}
	defer func() { _ = store.Close() }()
	successor, err := store.SelectCurrent(ctx)
	if err != nil {
		return nil, corpusGenerationDiffFailure("select current generation", err)
	}
	defer func() { _ = successor.Close() }()
	delta, err := verifyCorpusGenerationDelta(ctx, store, successor, options.Limits)
	if err != nil {
		return nil, corpusGenerationDiffFailure("verify qualified generation delta", err)
	}
	artifact, err := corpus.BuildGenerationDiffArtifact(
		delta, successor.Receipt().GenerationDigest, successor.Manifest().TombstoneDigest, options.Limits,
	)
	if err != nil {
		return nil, corpusGenerationDiffFailure("build qualified generation diff", err)
	}
	written := false
	if options.IdentityArtifact != "" {
		data, encodeErr := corpus.CanonicalGenerationDiffArtifact(artifact, options.Limits)
		if encodeErr != nil {
			return nil, corpusGenerationDiffFailure("encode identity artifact", encodeErr)
		}
		if writeErr := safepath.WriteFileExclusivePrivateOutsideRoot(options.StoreRoot, options.IdentityArtifact, data, 0o600); writeErr != nil {
			return nil, corpusGenerationDiffFailure("write exclusive identity artifact", writeErr)
		}
		written = true
	}
	return &CorpusGenerationDiffResult{
		SchemaVersion: CorpusGenerationDiffSchemaV1, Qualification: "qualified",
		Reason: corpus.GenerationDeltaAbsentQualified, Counts: delta.Counts,
		PredecessorGenerationDigest: delta.PredecessorGenerationDigest,
		SuccessorGenerationDigest:   successor.Receipt().GenerationDigest,
		TombstoneDigest:             successor.Manifest().TombstoneDigest,
		IdentityArtifactWritten:     written,
	}, nil
}

func corpusGenerationDiffFailure(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fmt.Errorf("%w: corpus diff could not %s", domain.ErrCheckFailed, operation)
}
