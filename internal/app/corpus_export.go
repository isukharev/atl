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
	corpusDocumentsStableID = "indexer-v1-documents"
	corpusEdgesStableID     = "indexer-v1-edges"
	corpusReceiptStableID   = "indexer-v1-receipt"
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
}

// CorpusExportResult is content-free. It is safe for normal CLI stdout: paths,
// selectors, backend origins, object identities, titles, and bodies stay only
// inside the private generation.
type CorpusExportResult struct {
	SchemaVersion int                   `json:"schema_version"`
	Reused        bool                  `json:"reused"`
	Projection    corpus.IndexerReceipt `json:"projection"`
	Generation    corpus.Summary        `json:"generation"`
}

type corpusExportMember struct {
	spec corpus.MemberSpec
	data []byte
}

type corpusProjectionBundle struct {
	receipt        corpus.IndexerReceipt
	members        []corpusExportMember
	qualifications []corpus.Qualification
}

type corpusExportSource struct {
	service  corpus.Service
	snapshot *mirror.CorpusSnapshot
}

// ExportCorpus constructs, seals, and atomically publishes one deterministic
// indexer-v1 projection from pristine local mirror baselines. It never reads
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

	store, err := openCorpusExportStore(options)
	if err != nil {
		return nil, corpusExportFailure("open sealed generation store", err)
	}
	defer func() { _ = store.Close() }()

	current, currentErr := store.SelectCurrent(ctx)
	predecessor := ""
	if currentErr == nil {
		equivalent := corpusGenerationEquivalent(current, bundle, options)
		if equivalent {
			result := &CorpusExportResult{
				SchemaVersion: corpus.IndexerSchemaV1,
				Reused:        true, Projection: bundle.receipt, Generation: current.Summary(),
			}
			_ = current.Close()
			return result, nil
		}
		predecessor = current.Receipt().GenerationDigest
		if closeErr := current.Close(); closeErr != nil {
			return nil, corpusExportFailure("close current generation", closeErr)
		}
	} else if !errors.Is(currentErr, corpus.ErrNoCurrent) {
		return nil, corpusExportFailure("verify current generation", currentErr)
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
		ProjectionSchema:  corpus.IndexerSchemaV1,
		GeneratorVersion:  options.GeneratorVersion,
		BuildState:        options.BuildState,
		PredecessorDigest: predecessor,
		Qualifications:    bundle.qualifications,
	})
	if errors.Is(err, corpus.ErrOutcomeUnknown) {
		generation, err = store.Verify(ctx, stage.ID())
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
			} else if corpusGenerationEquivalent(selected, bundle, options) {
				summary = selected.Summary()
				err = nil
				reused = true
			}
			_ = selected.Close()
		}
	}
	if err != nil {
		return nil, corpusExportFailure("publish generation", err)
	}
	return &CorpusExportResult{
		SchemaVersion: corpus.IndexerSchemaV1,
		Reused:        reused,
		Projection:    bundle.receipt,
		Generation:    summary,
	}, nil
}

func beginCorpusExportSources(options CorpusExportOptions) ([]corpusExportSource, []*mirrorSnapshotLock, error) {
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
		sources = append(sources, corpusExportSource{service: request.service, snapshot: snapshot})
	}
	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("%w: corpus export requires --jira, --confluence, or both", domain.ErrUsage)
	}
	return sources, guards, nil
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
	if manifest.ProjectionSchema != corpus.IndexerSchemaV1 || manifest.GeneratorVersion != options.GeneratorVersion ||
		manifest.BuildState != options.BuildState || manifest.TombstoneDigest != "" ||
		!sameCorpusQualifications(manifest.Qualifications, bundle.qualifications) || len(manifest.Members) != len(bundle.members) {
		return false
	}
	expected := make(map[string]corpus.Member, len(bundle.members))
	for _, member := range bundle.members {
		expected[corpusMemberKey(member.spec)] = corpus.Member{
			Service: member.spec.Service, StableID: member.spec.StableID, Role: member.spec.Role,
			Path: member.spec.Path, Size: int64(len(member.data)), Mode: 0o600, SHA256: corpusBytesSHA256(member.data),
		}
	}
	for _, member := range manifest.Members {
		want, ok := expected[corpusMemberKey(corpus.MemberSpec{
			Service: member.Service, StableID: member.StableID, Role: member.Role, Path: member.Path,
		})]
		if !ok || want != member {
			return false
		}
	}
	return true
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

func corpusExportFailure(operation string, err error) error {
	if err == nil {
		return nil
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
