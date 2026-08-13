package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	corpusDocumentsStableID = corpus.IndexerDocumentsStableID
	corpusEdgesStableID     = "indexer-v1-edges"
	corpusReceiptStableID   = "indexer-v1-receipt"
	corpusArtifactsStableID = "indexer-v2-artifacts"
	corpusReceiptV2StableID = "indexer-v2-receipt"
	corpusCaptureStableID   = "capture-v1-receipt"
)

// CorpusExportOptions selects already-initialized local mirrors and one
// owner-private sealed-generation store. Export is deliberately independent of
// global configuration, credentials, adapters, and backend clients.
type CorpusExportOptions struct {
	JiraRoot          string
	ConfluenceRoot    string
	StoreRoot         string
	InitializeStore   bool
	AllowUnreconciled bool
	GeneratorVersion  string
	BuildState        corpus.BuildState
	Limits            corpus.Limits
	SnapshotLimits    mirror.CorpusSnapshotLimits
	// CaptureReceipts upgrades every selected source from structural evidence to
	// a ready qualification. When non-empty, the set must cover all sources.
	CaptureReceipts []corpus.CaptureReceipt
}

// CorpusExportResult is content-free. It is safe for normal CLI stdout: paths,
// selectors, backend origins, object identities, titles, and bodies stay only
// inside the private generation.
type CorpusExportResult struct {
	SchemaVersion int                     `json:"schema_version"`
	Reused        bool                    `json:"reused"`
	Projection    corpus.IndexerReceiptV2 `json:"projection"`
	Generation    corpus.Summary          `json:"generation"`
}

type corpusExportMember struct {
	spec corpus.MemberSpec
	data []byte
}

type corpusProjectionBundle struct {
	receipt        corpus.IndexerReceiptV2
	members        []corpusExportMember
	qualifications []corpus.Qualification
}

type corpusExportSource struct {
	service      corpus.Service
	snapshot     *mirror.CorpusSnapshot
	capture      *corpus.CaptureReceipt
	captureBytes []byte
}

// ExportCorpus constructs, seals, and atomically publishes one deterministic
// indexer-v2 projection from pristine local mirror baselines. It never reads
// ambient working native/Markdown files and never performs backend I/O.
func ExportCorpus(ctx context.Context, options CorpusExportOptions) (*CorpusExportResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: corpus export requires a context", domain.ErrUsage)
	}
	if strings.TrimSpace(options.StoreRoot) == "" {
		return nil, fmt.Errorf("%w: corpus export requires --store", domain.ErrUsage)
	}
	if strings.TrimSpace(options.GeneratorVersion) == "" {
		return nil, fmt.Errorf("%w: corpus export requires a generator version", domain.ErrUsage)
	}
	if options.BuildState != corpus.BuildStateClean && options.BuildState != corpus.BuildStateModified && options.BuildState != corpus.BuildStateUnknown {
		return nil, fmt.Errorf("%w: corpus export build state is invalid", domain.ErrUsage)
	}

	sources, guards, err := beginCorpusExportSources(options)
	if err != nil {
		return nil, err
	}
	guardsOpen := true
	defer func() {
		if guardsOpen {
			_, _ = finishCorpusExportGuards(guards)
		}
	}()

	bundle, err := projectCorpusSnapshots(ctx, sources, options.Limits)
	if err == nil {
		for _, source := range sources {
			if validateErr := source.snapshot.Revalidate(); validateErr != nil {
				err = validateErr
				break
			}
		}
	}
	retry, finishErr := finishCorpusExportGuards(guards)
	guardsOpen = false
	if err != nil {
		return nil, corpusExportFailure("project pristine mirror evidence", err)
	}
	if finishErr != nil {
		return nil, corpusExportFailure("release mirror snapshot locks", finishErr)
	}
	if retry {
		return nil, corpusExportFailure("project pristine mirror evidence", errors.New("mirror changed during export"))
	}
	if err := preflightCorpusExportMembers(bundle.members, options.Limits); err != nil {
		return nil, corpusExportFailure("validate projected generation bounds", err)
	}

	store, err := openCorpusExportStore(options)
	if err != nil {
		return nil, corpusExportFailure("open sealed generation store", err)
	}
	defer func() { _ = store.Close() }()

	current, currentErr := store.SelectCurrent(ctx)
	predecessor := ""
	tombstoneDigest := ""
	if currentErr == nil {
		if verifyErr := verifyCorpusGenerationTombstoneState(ctx, store, current, options.Limits); verifyErr != nil {
			_ = current.Close()
			return nil, corpusExportFailure("verify current generation delta", verifyErr)
		}
		equivalent := corpusGenerationEquivalent(current, bundle, options)
		if equivalent {
			result := &CorpusExportResult{
				SchemaVersion: corpus.IndexerSchemaV2,
				Reused:        true, Projection: bundle.receipt, Generation: current.Summary(),
			}
			_ = current.Close()
			return result, nil
		}
		predecessor = current.Receipt().GenerationDigest
		deltaMember, deltaErr := deriveCorpusGenerationDelta(ctx, current, bundle, options.Limits)
		if deltaErr != nil {
			_ = current.Close()
			return nil, corpusExportFailure("derive qualified generation delta", deltaErr)
		}
		if deltaMember != nil {
			bundle.members = append(bundle.members, *deltaMember)
			sortCorpusExportMembers(bundle.members)
			tombstoneDigest = corpusBytesSHA256(deltaMember.data)
		}
		if closeErr := current.Close(); closeErr != nil {
			return nil, corpusExportFailure("close current generation", closeErr)
		}
	} else if !errors.Is(currentErr, corpus.ErrNoCurrent) {
		return nil, corpusExportFailure("verify current generation", currentErr)
	}
	if err := preflightCorpusExportMembers(bundle.members, options.Limits); err != nil {
		return nil, corpusExportFailure("validate qualified generation delta bounds", err)
	}

	stage, err := store.Begin()
	if err != nil {
		return nil, corpusExportFailure("begin generation", err)
	}
	for _, member := range bundle.members {
		if err := stage.Add(ctx, member.spec, bytes.NewReader(member.data)); err != nil {
			return nil, corpusExportFailure("stage generation member", err)
		}
	}
	generation, err := stage.Seal(ctx, corpus.SealOptions{
		ProjectionSchema:  corpus.IndexerSchemaV2,
		GeneratorVersion:  options.GeneratorVersion,
		BuildState:        options.BuildState,
		PredecessorDigest: predecessor,
		Qualifications:    bundle.qualifications,
		TombstoneDigest:   tombstoneDigest,
	})
	if errors.Is(err, corpus.ErrOutcomeUnknown) {
		sealErr := err
		generation, err = store.Verify(ctx, stage.ID())
		if err != nil {
			err = errors.Join(sealErr, err)
		}
	}
	if err != nil {
		return nil, corpusExportFailure("seal generation", err)
	}
	wantDigest := generation.Receipt().GenerationDigest
	if closeErr := generation.Close(); closeErr != nil {
		return nil, corpusExportFailure("close sealed generation", closeErr)
	}

	summary, err := store.Publish(ctx, stage.ID())
	reused := false
	if errors.Is(err, corpus.ErrOutcomeUnknown) || errors.Is(err, corpus.ErrStalePredecessor) {
		selected, selectErr := store.SelectCurrent(ctx)
		if selectErr == nil {
			if selected.ID() == stage.ID() && selected.Receipt().GenerationDigest == wantDigest {
				summary = selected.Summary()
				err = nil
			} else {
				selectErr = verifyCorpusGenerationTombstoneState(ctx, store, selected, options.Limits)
				if selectErr == nil && corpusGenerationEquivalent(selected, bundle, options) {
					summary = selected.Summary()
					err = nil
					reused = true
				}
				if selectErr != nil {
					_ = selected.Close()
					return nil, corpusExportFailure("verify concurrent generation delta", selectErr)
				}
			}
			_ = selected.Close()
		}
	}
	if err != nil {
		return nil, corpusExportFailure("publish generation", err)
	}
	return &CorpusExportResult{
		SchemaVersion: corpus.IndexerSchemaV2,
		Reused:        reused,
		Projection:    bundle.receipt,
		Generation:    summary,
	}, nil
}

func beginCorpusExportSources(options CorpusExportOptions) ([]corpusExportSource, []*mirrorSnapshotLock, error) {
	captures := make(map[corpus.Service]corpus.CaptureReceipt, len(options.CaptureReceipts))
	for _, receipt := range options.CaptureReceipts {
		if err := corpus.VerifyCaptureReceipt(receipt, options.Limits); err != nil {
			return nil, nil, corpusExportFailure("verify capture receipt", err)
		}
		if _, duplicate := captures[receipt.Service]; duplicate {
			return nil, nil, corpusExportFailure("verify capture receipt set", corpus.ErrIntegrity)
		}
		captures[receipt.Service] = receipt
	}
	requested := []struct {
		service         corpus.Service
		snapshotService string
		candidate       string
		lockPath        func(string) string
	}{
		{corpus.ServiceConfluence, mirror.CorpusSnapshotConfluence, options.ConfluenceRoot,
			func(root string) string { return filepath.Join(root, ".atl", confluenceMutationLockName) }},
		{corpus.ServiceJira, mirror.CorpusSnapshotJira, options.JiraRoot, jiraPendingFieldsLockPath},
	}
	sources := make([]corpusExportSource, 0, 2)
	guards := make([]*mirrorSnapshotLock, 0, 2)
	for _, request := range requested {
		if strings.TrimSpace(request.candidate) == "" {
			continue
		}
		root, ok := MirrorRootOf(request.candidate)
		if !ok {
			_, _ = finishCorpusExportGuards(guards)
			return nil, nil, fmt.Errorf("%w: %s corpus source is not an initialized mirror", domain.ErrUsage, request.service)
		}
		guard, err := beginMirrorSnapshotLock(root, request.lockPath(root))
		if err != nil {
			_, _ = finishCorpusExportGuards(guards)
			return nil, nil, corpusExportFailure("acquire mirror snapshot lock", err)
		}
		guards = append(guards, guard)
		snapshot, err := mirror.New(root).BeginCorpusSnapshot(request.snapshotService, mirror.CorpusSnapshotOptions{
			Limits: options.SnapshotLimits, AllowUnreconciled: options.AllowUnreconciled,
		})
		if err != nil {
			_, _ = finishCorpusExportGuards(guards)
			return nil, nil, corpusExportFailure("capture pristine mirror snapshot", err)
		}
		source := corpusExportSource{service: request.service, snapshot: snapshot}
		if receipt, present := captures[request.service]; present {
			if err := validateCorpusCaptureSource(snapshot, receipt, options.Limits); err != nil {
				_, _ = finishCorpusExportGuards(guards)
				return nil, nil, corpusExportFailure("reconcile capture receipt", err)
			}
			canonical, err := corpus.CanonicalCaptureReceipt(receipt, options.Limits)
			if err != nil {
				_, _ = finishCorpusExportGuards(guards)
				return nil, nil, corpusExportFailure("encode capture receipt", err)
			}
			receiptCopy := receipt
			receiptCopy.Dimensions = append([]corpus.CaptureDimensionEvidence(nil), receipt.Dimensions...)
			source.capture = &receiptCopy
			source.captureBytes = canonical
			delete(captures, request.service)
		}
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("%w: corpus export requires --jira, --confluence, or both", domain.ErrUsage)
	}
	if len(options.CaptureReceipts) != 0 {
		if len(captures) != 0 || len(options.CaptureReceipts) != len(sources) {
			_, _ = finishCorpusExportGuards(guards)
			return nil, nil, corpusExportFailure("verify capture receipt set", corpus.ErrIntegrity)
		}
		for _, source := range sources {
			if source.capture == nil {
				_, _ = finishCorpusExportGuards(guards)
				return nil, nil, corpusExportFailure("verify capture receipt set", corpus.ErrIntegrity)
			}
		}
	}
	return sources, guards, nil
}

func validateCorpusCaptureSource(snapshot *mirror.CorpusSnapshot, receipt corpus.CaptureReceipt, limits corpus.Limits) error {
	if snapshot == nil || !snapshot.Reconciled() || string(receipt.Service) != snapshot.Service() ||
		receipt.SnapshotDigest != snapshot.Fingerprint() || receipt.Total != snapshot.Len() || receipt.Completed != snapshot.Len() {
		return corpus.ErrIntegrity
	}
	providerIDs := make([]string, 0, snapshot.Len())
	for _, item := range snapshot.Inventory() {
		providerIDs = append(providerIDs, item.ProviderID)
	}
	selection, err := corpus.CaptureSelectionDigest(receipt.Service, providerIDs)
	if err != nil || selection != receipt.SelectionDigest {
		return corpus.ErrIntegrity
	}
	if err := validateCorpusCaptureDimensions(snapshot, receipt.Dimensions); err != nil {
		return err
	}
	return corpus.VerifyCaptureReceipt(receipt, limits)
}

func finishCorpusExportGuards(guards []*mirrorSnapshotLock) (bool, error) {
	retry := false
	var result error
	for index := len(guards) - 1; index >= 0; index-- {
		currentRetry, err := guards[index].finish()
		retry = retry || currentRetry
		if err != nil {
			result = errors.Join(result, err)
		}
	}
	return retry, result
}

func openCorpusExportStore(options CorpusExportOptions) (*corpus.Store, error) {
	storeOptions := corpus.Options{Limits: options.Limits}
	if options.InitializeStore {
		return corpus.Initialize(options.StoreRoot, storeOptions)
	}
	return corpus.Open(options.StoreRoot, storeOptions)
}

func corpusGenerationEquivalent(generation *corpus.Generation, bundle corpusProjectionBundle, options CorpusExportOptions) bool {
	if generation == nil {
		return false
	}
	manifest := generation.Manifest()
	if manifest.ProjectionSchema != corpus.IndexerSchemaV2 || manifest.GeneratorVersion != options.GeneratorVersion ||
		manifest.BuildState != options.BuildState ||
		!sameCorpusQualifications(manifest.Qualifications, bundle.qualifications) {
		return false
	}
	expected := make(map[string]corpus.Member, len(bundle.members))
	for _, member := range bundle.members {
		if member.spec.Role == corpus.RoleTombstone {
			continue
		}
		expected[corpusMemberKey(member.spec)] = corpus.Member{
			Service: member.spec.Service, StableID: member.spec.StableID, Role: member.spec.Role,
			Path: member.spec.Path, Size: int64(len(member.data)), Mode: 0o600, SHA256: corpusBytesSHA256(member.data),
		}
	}
	matched := 0
	tombstones := 0
	for _, member := range manifest.Members {
		if member.Role == corpus.RoleTombstone {
			tombstones++
			continue
		}
		want, ok := expected[corpusMemberKey(corpus.MemberSpec{
			Service: member.Service, StableID: member.StableID, Role: member.Role, Path: member.Path,
		})]
		if !ok || want != member {
			return false
		}
		matched++
	}
	if matched != len(expected) {
		return false
	}
	if manifest.TombstoneDigest == "" {
		return tombstones == 0
	}
	return tombstones == 1
}

func sameCorpusQualifications(left, right []corpus.Qualification) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func corpusMemberKey(spec corpus.MemberSpec) string {
	return string(spec.Service) + "\x00" + spec.StableID + "\x00" + string(spec.Role) + "\x00" + spec.Path
}

func corpusBytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func preflightCorpusExportMembers(members []corpusExportMember, limits corpus.Limits) error {
	defaults := corpus.DefaultLimits()
	if limits.MaxMembers == 0 {
		limits.MaxMembers = defaults.MaxMembers
	}
	if limits.MaxMemberBytes == 0 {
		limits.MaxMemberBytes = defaults.MaxMemberBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MaxMembers < 0 || limits.MaxMemberBytes < 0 || limits.MaxTotalBytes < 0 || len(members) > limits.MaxMembers {
		return corpus.ErrIntegrity
	}
	var total int64
	for _, member := range members {
		size := int64(len(member.data))
		if size > limits.MaxMemberBytes || size > limits.MaxTotalBytes-total {
			return corpus.ErrIntegrity
		}
		total += size
	}
	return nil
}

func corpusExportFailure(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, corpus.ErrOutcomeUnknown) {
		switch {
		case errors.Is(err, context.Canceled):
			return fmt.Errorf("%w: %w: %w: corpus export could not %s", domain.ErrCheckFailed, corpus.ErrOutcomeUnknown, context.Canceled, operation)
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("%w: %w: %w: corpus export could not %s", domain.ErrCheckFailed, corpus.ErrOutcomeUnknown, context.DeadlineExceeded, operation)
		default:
			return fmt.Errorf("%w: %w: corpus export could not %s", domain.ErrCheckFailed, corpus.ErrOutcomeUnknown, operation)
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	// Snapshot, filesystem, JSON, and codec errors may contain private paths or
	// values. The detailed evidence remains in the private roots; normal command
	// diagnostics expose only this closed operation category.
	return fmt.Errorf("%w: corpus export could not %s", domain.ErrCheckFailed, operation)
}

func sortCorpusExportMembers(members []corpusExportMember) {
	sort.Slice(members, func(i, j int) bool {
		left, right := members[i].spec, members[j].spec
		if left.Service != right.Service {
			return left.Service < right.Service
		}
		if left.StableID != right.StableID {
			return left.StableID < right.StableID
		}
		if left.Role != right.Role {
			return left.Role < right.Role
		}
		return left.Path < right.Path
	})
}
