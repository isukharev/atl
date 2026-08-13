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

const CorpusCacheLifecycleSchemaV1 = 1

type CorpusCacheStatusOptions struct {
	StoreRoot string
	Limits    corpus.Limits
}

type CorpusCacheStatusResult struct {
	SchemaVersion int                      `json:"schema_version"`
	Initialized   bool                     `json:"initialized"`
	Current       bool                     `json:"current"`
	Binding       string                   `json:"binding"`
	Retention     corpus.RetentionStatusV1 `json:"retention"`
}

// StatusCorpusCache verifies the entire sealed inventory through the minimum
// safe one-predecessor preview and reports only aggregate/categorical state.
func StatusCorpusCache(ctx context.Context, options CorpusCacheStatusOptions) (*CorpusCacheStatusResult, error) {
	if ctx == nil || strings.TrimSpace(options.StoreRoot) == "" {
		return nil, fmt.Errorf("%w: corpus cache status requires --store", domain.ErrUsage)
	}
	store, err := corpus.Open(options.StoreRoot, corpus.Options{Limits: options.Limits})
	if err != nil {
		return nil, corpusCacheLifecycleFailure("open cache store", err)
	}
	defer func() { _ = store.Close() }()
	inventoryStatus, err := store.RetentionInventoryStatusV1(ctx)
	if err != nil {
		return nil, corpusCacheLifecycleFailure("verify cache inventory", err)
	}
	current, err := store.SelectCurrent(ctx)
	if errors.Is(err, corpus.ErrNoCurrent) {
		return &CorpusCacheStatusResult{
			SchemaVersion: CorpusCacheLifecycleSchemaV1, Initialized: true,
			Binding: "absent", Retention: inventoryStatus,
		}, nil
	}
	if err != nil {
		return nil, corpusCacheLifecycleFailure("select current generation", err)
	}
	defer func() { _ = current.Close() }()
	if err := verifyCorpusGenerationTombstoneState(ctx, store, current, options.Limits); err != nil {
		return nil, corpusCacheLifecycleFailure("verify current generation", err)
	}
	bindingState := "unsupported"
	bindingSpec := corpus.CacheBindingMemberSpec()
	bindingCount := 0
	for _, member := range current.Manifest().Members {
		if member.Service == bindingSpec.Service && member.StableID == bindingSpec.StableID && member.Role == bindingSpec.Role {
			bindingCount++
			if member.Path != bindingSpec.Path {
				return nil, corpusCacheLifecycleFailure("verify cache binding", corpus.ErrIntegrity)
			}
		}
	}
	if bindingCount > 1 {
		return nil, corpusCacheLifecycleFailure("verify cache binding", corpus.ErrIntegrity)
	}
	if bindingCount == 1 {
		binding, loadErr := corpus.LoadCacheBindingV1(ctx, current)
		if loadErr != nil {
			return nil, corpusCacheLifecycleFailure("load cache binding", loadErr)
		}
		if verifyErr := corpus.VerifyCacheBindingV1(binding, current); verifyErr != nil {
			return nil, corpusCacheLifecycleFailure("verify cache binding", verifyErr)
		}
		bindingState = "ineligible"
		if binding.Reusable {
			bindingState = "reusable"
		}
	}
	plan, err := corpus.BuildRetentionPlanV1(ctx, store, corpus.RetentionPolicyV1{
		SchemaVersion: corpus.RetentionPolicySchemaV1, RetainPredecessors: 1,
	})
	if err != nil {
		return nil, corpusCacheLifecycleFailure("verify cache inventory", err)
	}
	planStatus := plan.Status()
	currentSummary := current.Summary()
	if planStatus.SealedGenerations != inventoryStatus.SealedGenerations || planStatus.UnsealedStages != inventoryStatus.UnsealedStages ||
		plan.Current.GenerationID != current.ID() || plan.Current.GenerationDigest != currentSummary.GenerationDigest {
		return nil, corpusCacheLifecycleFailure("verify cache inventory", corpus.ErrIntegrity)
	}
	if err := store.ConfirmCurrent(ctx, current); err != nil {
		return nil, corpusCacheLifecycleFailure("confirm current generation", err)
	}
	return &CorpusCacheStatusResult{
		SchemaVersion: CorpusCacheLifecycleSchemaV1, Initialized: true, Current: true,
		Binding: bindingState, Retention: planStatus,
	}, nil
}

type CorpusCacheRetentionPreviewOptions struct {
	StoreRoot          string
	RetainPredecessors int
	PlanArtifact       string
	Limits             corpus.Limits
}

type CorpusCacheRetentionPreviewResult struct {
	SchemaVersion       int                      `json:"schema_version"`
	Status              corpus.RetentionStatusV1 `json:"status"`
	PlanDigest          string                   `json:"plan_digest"`
	PlanArtifactWritten bool                     `json:"plan_artifact_written"`
}

func PreviewCorpusCacheRetention(ctx context.Context, options CorpusCacheRetentionPreviewOptions) (*CorpusCacheRetentionPreviewResult, error) {
	if ctx == nil || strings.TrimSpace(options.StoreRoot) == "" || strings.TrimSpace(options.PlanArtifact) == "" || options.RetainPredecessors < 1 {
		return nil, fmt.Errorf("%w: retention preview requires --store, --plan-artifact, and positive --retain-predecessors", domain.ErrUsage)
	}
	store, err := corpus.Open(options.StoreRoot, corpus.Options{Limits: options.Limits})
	if err != nil {
		return nil, corpusCacheLifecycleFailure("open cache store", err)
	}
	defer func() { _ = store.Close() }()
	plan, err := corpus.BuildRetentionPlanV1(ctx, store, corpus.RetentionPolicyV1{
		SchemaVersion: corpus.RetentionPolicySchemaV1, RetainPredecessors: options.RetainPredecessors,
	})
	if err != nil {
		return nil, corpusCacheLifecycleFailure("build retention plan", err)
	}
	data, err := corpus.CanonicalRetentionPlanV1(plan, options.Limits)
	if err != nil {
		return nil, corpusCacheLifecycleFailure("encode retention plan", err)
	}
	if err := safepath.WriteFileExclusivePrivateOutsideRoot(options.StoreRoot, options.PlanArtifact, data, 0o600); err != nil {
		return nil, corpusCacheLifecycleFailure("write exclusive retention plan", err)
	}
	return &CorpusCacheRetentionPreviewResult{
		SchemaVersion: CorpusCacheLifecycleSchemaV1, Status: plan.Status(), PlanDigest: plan.PlanDigest, PlanArtifactWritten: true,
	}, nil
}

type CorpusCacheRetentionApplyOptions struct {
	StoreRoot          string
	PlanArtifact       string
	ExpectedPlanDigest string
	Apply              bool
	Limits             corpus.Limits
}

type CorpusCacheRetentionApplyResult struct {
	SchemaVersion int                      `json:"schema_version"`
	Status        corpus.RetentionStatusV1 `json:"status"`
}

func ApplyCorpusCacheRetention(ctx context.Context, options CorpusCacheRetentionApplyOptions) (*CorpusCacheRetentionApplyResult, error) {
	if ctx == nil || strings.TrimSpace(options.StoreRoot) == "" || strings.TrimSpace(options.PlanArtifact) == "" ||
		strings.TrimSpace(options.ExpectedPlanDigest) == "" || !options.Apply {
		return nil, fmt.Errorf("%w: retention apply requires --store, --plan-artifact, --expected-plan-digest, and --apply", domain.ErrUsage)
	}
	data, err := safepath.ReadFilePrivateOutsideRoot(options.StoreRoot, options.PlanArtifact, corpus.MaxRetentionPlanBytesV1)
	if err != nil {
		return nil, corpusCacheLifecycleFailure("read reviewed retention plan", err)
	}
	store, err := corpus.Open(options.StoreRoot, corpus.Options{Limits: options.Limits})
	if err != nil {
		return nil, corpusCacheLifecycleFailure("open cache store", err)
	}
	defer func() { _ = store.Close() }()
	status, err := store.ApplyRetentionPlanV1(ctx, data, options.ExpectedPlanDigest)
	if err != nil {
		return nil, corpusCacheLifecycleFailure("apply retention plan", err)
	}
	return &CorpusCacheRetentionApplyResult{SchemaVersion: CorpusCacheLifecycleSchemaV1, Status: status}, nil
}

func corpusCacheLifecycleFailure(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return &corpusCacheLifecycleError{operation: operation, cause: err}
}

type corpusCacheLifecycleError struct {
	operation string
	cause     error
}

func (failure *corpusCacheLifecycleError) Error() string {
	return "corpus cache could not " + failure.operation
}

func (failure *corpusCacheLifecycleError) Unwrap() error {
	return failure.cause
}

func (failure *corpusCacheLifecycleError) Is(target error) bool {
	return target == domain.ErrCheckFailed
}

func (failure *corpusCacheLifecycleError) DiagnosticAmbiguousWrite() bool {
	return failure != nil && errors.Is(failure.cause, corpus.ErrOutcomeUnknown)
}
