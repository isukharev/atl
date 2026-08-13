package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

func (store *corpusBuildConfluenceStore) ReadConfluenceCorpusMetadata(ctx context.Context, space string, maxPages int) (domain.ConfluenceCorpusMetadataInventory, error) {
	if space != "DOC" || maxPages != 2 {
		return domain.ConfluenceCorpusMetadataInventory{}, domain.ErrCheckFailed
	}
	if store.budgeted {
		if err := consumeCorpusBuildRead(ctx, 23); err != nil {
			return domain.ConfluenceCorpusMetadataInventory{}, err
		}
	}
	store.pageMu.Lock()
	defer store.pageMu.Unlock()
	rows := make([]domain.ConfluenceCorpusMetadata, 0, len(store.pages))
	for _, id := range []string{"10", "20"} {
		page := store.pages[id]
		if page == nil || page.Restricted == nil {
			return domain.ConfluenceCorpusMetadataInventory{}, domain.ErrCheckFailed
		}
		rows = append(rows, domain.ConfluenceCorpusMetadata{
			ID: page.ID, Type: page.Type, Title: page.Title, Space: page.SpaceKey,
			Version: page.Version, Updated: page.Updated, Parent: page.Parent,
			Ancestors: append([]string{}, page.Ancestors...), AncestorIDs: append([]string{}, page.AncestorIDs...),
			Labels: append([]string{}, page.Labels...), Restricted: *page.Restricted,
			URL: "https://confluence.example.test/pages/" + id,
		})
	}
	return domain.ConfluenceCorpusMetadataInventory{Rows: rows, Complete: true}, nil
}

func TestCorpusCacheColdBootstrapThenMetadataOnlyHit(t *testing.T) {
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	firstRoot := corpusBuildPrivateRoot(t)
	firstOptions := corpusBuildCacheTestOptions(firstRoot, cacheRoot)
	firstOptions.Initialize = true
	firstOptions.InitializeCache = true

	first, err := service.Build(t.Context(), firstOptions)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != "new" || first.Reused || first.Cache == nil || first.Cache.Status != corpusCacheStatusPublished ||
		first.Cache.ProbeUsage != (corpus.CaptureUsage{Attempts: 2, ResponseBytes: 46}) || len(store.getIDs) != 2 {
		t.Fatalf("cold result=%#v body_reads=%v", first, store.getIDs)
	}

	secondRoot := corpusBuildPrivateRoot(t)
	secondOptions := corpusBuildCacheTestOptions(secondRoot, cacheRoot)
	secondOptions.Initialize = true
	second, err := service.Build(t.Context(), secondOptions)
	if err != nil {
		t.Fatal(err)
	}
	if second.Source != "cache" || !second.Reused || second.Cache == nil || second.Cache.Status != corpusCacheStatusHit ||
		second.Cache.ProbeUsage != (corpus.CaptureUsage{Attempts: 3, ResponseBytes: 51}) || len(store.getIDs) != 2 ||
		len(second.Services) != 1 || second.Services[0].Status != "reused" {
		t.Fatalf("hit result=%#v body_reads=%v", second, store.getIDs)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{cacheRoot, firstRoot, secondRoot, "DOC", "fixture-confluence-principal", "Page 10"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("cache result leaked %q: %s", private, encoded)
		}
	}
}

func TestCorpusCacheMetadataDriftFallsBackToColdGeneration(t *testing.T) {
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	firstOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	firstOptions.Initialize, firstOptions.InitializeCache = true, true
	first, err := service.Build(t.Context(), firstOptions)
	if err != nil {
		t.Fatal(err)
	}

	store.pages["10"].Version++
	store.pages["10"].Body = []byte("<p>changed</p>")
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
	store.getIDs = nil
	secondOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	secondOptions.Initialize = true
	second, err := service.Build(t.Context(), secondOptions)
	if err != nil {
		t.Fatal(err)
	}
	if second.Source != "new" || second.Cache == nil || second.Cache.Reason != corpusCacheReasonChanged ||
		len(store.getIDs) != 2 || second.Generation.GenerationDigest == first.Generation.GenerationDigest {
		t.Fatalf("cold refresh result=%#v body_reads=%v", second, store.getIDs)
	}
}

func TestCorpusCacheCompatibilityDriftNeverUsesMetadataOnlyHit(t *testing.T) {
	for name, drift := range map[string]func(*CorpusBuildService, *corpusBuildConfluenceStore){
		"principal": func(_ *CorpusBuildService, store *corpusBuildConfluenceStore) {
			store.identity.ID = "different-fixture-principal"
		},
		"trust": func(service *CorpusBuildService, _ *corpusBuildConfluenceStore) {
			service.confluenceTrustDigest = strings.Repeat("c", 64)
		},
		"generator": func(service *CorpusBuildService, _ *corpusBuildConfluenceStore) {
			service.generatorCommit = strings.Repeat("d", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			cacheRoot := corpusBuildPrivateRoot(t)
			store := newCorpusBuildCacheConfluenceFixture(true)
			service := newCorpusBuildCacheTestService(store)
			firstOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
			firstOptions.Initialize, firstOptions.InitializeCache = true, true
			if _, err := service.Build(t.Context(), firstOptions); err != nil {
				t.Fatal(err)
			}

			drift(service, store)
			store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
			store.getIDs = nil
			secondOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
			secondOptions.Initialize = true
			result, err := service.Build(t.Context(), secondOptions)
			if err != nil {
				t.Fatal(err)
			}
			if result.Source != "new" || result.Reused || result.Cache == nil ||
				result.Cache.Reason != corpusCacheReasonIncompatible || len(store.getIDs) != 2 {
				t.Fatalf("result=%#v body_reads=%v", result, store.getIDs)
			}
		})
	}
}

func TestCorpusCacheUnknownTrustSealsIneligibleAndNeverHits(t *testing.T) {
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	service.confluenceTrustDigest = ""
	firstOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	firstOptions.Initialize, firstOptions.InitializeCache = true, true
	if _, err := service.Build(t.Context(), firstOptions); err != nil {
		t.Fatal(err)
	}
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
	store.getIDs = nil
	secondOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	secondOptions.Initialize = true
	second, err := service.Build(t.Context(), secondOptions)
	if err != nil {
		t.Fatal(err)
	}
	if second.Cache == nil || second.Cache.Reason != corpusCacheReasonIneligible || len(store.getIDs) != 2 {
		t.Fatalf("result=%#v body_reads=%v", second, store.getIDs)
	}
}

type forbiddenCorpusCacheMetadata struct{}

func (forbiddenCorpusCacheMetadata) ReadConfluenceCorpusMetadata(context.Context, string, int) (domain.ConfluenceCorpusMetadataInventory, error) {
	return domain.ConfluenceCorpusMetadataInventory{}, domain.ErrForbidden
}

func TestCorpusCacheForbiddenMetadataFallsBackToColdIneligibleGeneration(t *testing.T) {
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	firstOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	firstOptions.Initialize, firstOptions.InitializeCache = true, true
	if _, err := service.Build(t.Context(), firstOptions); err != nil {
		t.Fatal(err)
	}
	service.confluence.corpusMetadata = forbiddenCorpusCacheMetadata{}
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
	store.getIDs = nil
	secondOptions := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	secondOptions.Initialize = true
	result, err := service.Build(t.Context(), secondOptions)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "new" || result.Cache == nil || result.Cache.Reason != corpusCacheReasonUnqualified || len(store.getIDs) != 2 {
		t.Fatalf("result=%#v body_reads=%v", result, store.getIDs)
	}
	status, err := StatusCorpusCache(t.Context(), CorpusCacheStatusOptions{StoreRoot: cacheRoot})
	if err != nil || status.Binding != "ineligible" {
		t.Fatalf("status=%#v error=%v", status, err)
	}
}

func TestCorpusCacheBuildOptionsRequireCompleteIndependentPolicy(t *testing.T) {
	valid := corpusBuildCacheTestOptions("/synthetic/workspace", "/synthetic/cache")
	for name, mutate := range map[string]func(*CorpusBuildOptions){
		"missing requests": func(options *CorpusBuildOptions) { options.CacheMaxRequests = 0 },
		"missing bytes":    func(options *CorpusBuildOptions) { options.CacheMaxResponseBytes = 0 },
		"missing deadline": func(options *CorpusBuildOptions) { options.CacheDeadline = 0 },
		"policy no root":   func(options *CorpusBuildOptions) { options.CacheRoot = "" },
	} {
		t.Run(name, func(t *testing.T) {
			options := valid
			mutate(&options)
			if err := ValidateCorpusBuildOptions(options); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCorpusCacheCompletedAttemptBindsPublicationRoot(t *testing.T) {
	firstCache := corpusBuildPrivateRoot(t)
	options := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), firstCache)
	_, services, err := corpusBuildServices(options)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	rootDigest, err := corpus.RootIdentityDigest(firstCache)
	if err != nil {
		t.Fatal(err)
	}
	active := corpus.BuildActive{
		SchemaVersion: corpus.BuildActiveSchemaV3, AttemptID: strings.Repeat("1", 32),
		Status: corpus.BuildAttemptCompleted, OptionsDigest: strings.Repeat("a", 64), Services: services,
		StartedAt: corpus.NewBuildActiveTime(started), Deadline: corpus.NewBuildActiveTime(started.Add(options.Deadline)),
		MaxAttempts: options.MaxRequests, MaxResponseBytes: options.MaxResponseBytes,
		PublicationTarget: corpus.PublicationTargetCache, PublicationRootDigest: rootDigest,
		GenerationDigest: strings.Repeat("b", 64),
	}
	for index := range active.Services {
		active.Services[index].ScopeDigest = strings.Repeat("c", 64)
		active.Services[index].StartedAt = active.StartedAt
		active.Services[index].ReceiptDigest = strings.Repeat("d", 64)
	}
	if err := validateCorpusBuildActiveBinding(active, options); err != nil {
		t.Fatal(err)
	}
	options.CacheRoot = corpusBuildPrivateRoot(t)
	if err := validateCorpusBuildActiveBinding(active, options); !errors.Is(err, corpus.ErrIntegrity) {
		t.Fatalf("retargeted cache error=%v", err)
	}
}

func TestCorpusCacheCompletedWorkspaceCanTransitionToCache(t *testing.T) {
	workspaceRoot := corpusBuildPrivateRoot(t)
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	options := corpusBuildTestOptions(workspaceRoot)
	options.Initialize = true
	options.ConfluenceSpace, options.MaxConfluencePages = "DOC", 2
	first, err := service.Build(t.Context(), options)
	if err != nil || first.Cache != nil {
		t.Fatalf("workspace result=%#v error=%v", first, err)
	}
	if active := loadCorpusBuildActiveForTest(t, workspaceRoot, options); active.SchemaVersion != corpus.BuildActiveSchemaV2 {
		t.Fatalf("workspace active=%#v", active)
	}

	options.Initialize = false
	options.CacheRoot, options.InitializeCache = cacheRoot, true
	options.CacheMaxRequests, options.CacheMaxResponseBytes, options.CacheDeadline = 10, 1<<20, 30*time.Second
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
	second, err := service.Build(t.Context(), options)
	if err != nil || second.Cache == nil || second.Cache.Status != corpusCacheStatusPublished {
		t.Fatalf("cache transition result=%#v error=%v", second, err)
	}
	active := loadCorpusBuildActiveForTest(t, workspaceRoot, options)
	if active.SchemaVersion != corpus.BuildActiveSchemaV3 || active.PublicationTarget != corpus.PublicationTargetCache {
		t.Fatalf("cache active=%#v", active)
	}
}

func TestCorpusCacheRestartCanTransitionBothPublicationTargets(t *testing.T) {
	workspaceRoot := corpusBuildPrivateRoot(t)
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	options := corpusBuildTestOptions(workspaceRoot)
	options.Initialize = true
	options.ConfluenceSpace, options.MaxConfluencePages = "DOC", 2
	store.currentErr = errors.New("synthetic principal interruption")
	if result, err := service.Build(t.Context(), options); result != nil || err == nil {
		t.Fatalf("interrupted result=%#v error=%v", result, err)
	}
	if active := loadCorpusBuildActiveForTest(t, workspaceRoot, options); active.SchemaVersion != corpus.BuildActiveSchemaV2 {
		t.Fatalf("interrupted active=%#v", active)
	}

	store.currentErr = nil
	options.Initialize, options.Restart = false, true
	options.CacheRoot, options.InitializeCache = cacheRoot, true
	options.CacheMaxRequests, options.CacheMaxResponseBytes, options.CacheDeadline = 10, 1<<20, 30*time.Second
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
	cached, err := service.Build(t.Context(), options)
	if err != nil || cached.Cache == nil || cached.Cache.Status != corpusCacheStatusPublished || cached.Source != "restarted" {
		t.Fatalf("cache restart result=%#v error=%v", cached, err)
	}
	active := loadCorpusBuildActiveForTest(t, workspaceRoot, options)
	if active.SchemaVersion != corpus.BuildActiveSchemaV3 || active.PublicationTarget != corpus.PublicationTargetCache {
		t.Fatalf("cache active=%#v", active)
	}

	options.Restart = false
	options.CacheRoot = ""
	options.InitializeCache = false
	options.CacheMaxRequests, options.CacheMaxResponseBytes, options.CacheDeadline = 0, 0, 0
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
	workspaceResult, err := service.Build(t.Context(), options)
	if err != nil || workspaceResult.Cache != nil || workspaceResult.Source != "new" {
		t.Fatalf("workspace transition result=%#v error=%v", workspaceResult, err)
	}
	active = loadCorpusBuildActiveForTest(t, workspaceRoot, options)
	if active.SchemaVersion != corpus.BuildActiveSchemaV3 || active.PublicationTarget != corpus.PublicationTargetWorkspace || active.PublicationRootDigest != "" {
		t.Fatalf("workspace active=%#v", active)
	}
}

func TestCorpusCacheLifecyclePreviewsAndAppliesExactFiniteRetention(t *testing.T) {
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	var generations []string
	for index := range 3 {
		if index > 0 {
			id := "10"
			if index == 2 {
				id = "20"
			}
			store.pages[id].Version++
			store.pages[id].Body = []byte("<p>changed synthetic body</p>")
			store.searchSequence = []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")}
		}
		options := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
		options.Initialize = true
		options.InitializeCache = index == 0
		result, err := service.Build(t.Context(), options)
		if err != nil {
			t.Fatal(err)
		}
		generations = append(generations, result.Generation.GenerationDigest)
	}
	if generations[0] == generations[1] || generations[1] == generations[2] || generations[0] == generations[2] {
		t.Fatalf("generation digests were not distinct: %v", generations)
	}

	status, err := StatusCorpusCache(t.Context(), CorpusCacheStatusOptions{StoreRoot: cacheRoot})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || !status.Current || status.Binding != "reusable" ||
		status.Retention.SealedGenerations != 3 || status.Retention.ProtectedGenerations != 2 ||
		status.Retention.CandidateGenerations != 1 || status.Retention.UnsealedStages != 0 {
		t.Fatalf("status=%#v", status)
	}

	artifactRoot := corpusBuildPrivateRoot(t)
	artifactPath := filepath.Join(artifactRoot, "retention-plan.json")
	preview, err := PreviewCorpusCacheRetention(t.Context(), CorpusCacheRetentionPreviewOptions{
		StoreRoot: cacheRoot, RetainPredecessors: 1, PlanArtifact: artifactPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.PlanArtifactWritten || preview.Status.CandidateGenerations != 1 || len(preview.PlanDigest) != 64 {
		t.Fatalf("preview=%#v", preview)
	}
	if info, err := os.Stat(artifactPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan mode=%v error=%v", info, err)
	}
	if _, err := ApplyCorpusCacheRetention(t.Context(), CorpusCacheRetentionApplyOptions{
		StoreRoot: cacheRoot, PlanArtifact: artifactPath,
		ExpectedPlanDigest: strings.Repeat("f", 64), Apply: true,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("wrong digest error=%v", err)
	}
	unchanged, err := StatusCorpusCache(t.Context(), CorpusCacheStatusOptions{StoreRoot: cacheRoot})
	if err != nil || unchanged.Retention.SealedGenerations != 3 {
		t.Fatalf("wrong digest changed cache: status=%#v error=%v", unchanged, err)
	}

	applied, err := ApplyCorpusCacheRetention(t.Context(), CorpusCacheRetentionApplyOptions{
		StoreRoot: cacheRoot, PlanArtifact: artifactPath,
		ExpectedPlanDigest: preview.PlanDigest, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Status.Complete || applied.Status.RemovedThisApply != 1 || applied.Status.RemainingCandidates != 0 {
		t.Fatalf("apply=%#v", applied)
	}
	resumed, err := ApplyCorpusCacheRetention(t.Context(), CorpusCacheRetentionApplyOptions{
		StoreRoot: cacheRoot, PlanArtifact: artifactPath,
		ExpectedPlanDigest: preview.PlanDigest, Apply: true,
	})
	if err != nil || !resumed.Status.Complete || resumed.Status.RemovedThisApply != 0 {
		t.Fatalf("resume=%#v error=%v", resumed, err)
	}
	final, err := StatusCorpusCache(t.Context(), CorpusCacheStatusOptions{StoreRoot: cacheRoot})
	if err != nil || final.Retention.SealedGenerations != 2 || final.Retention.CandidateGenerations != 0 {
		t.Fatalf("final status=%#v error=%v", final, err)
	}
	encoded, err := json.Marshal([]any{status, preview, applied, resumed, final})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{cacheRoot, artifactRoot, "DOC", "fixture-confluence-principal", "Page 10"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("lifecycle output leaked %q: %s", private, encoded)
		}
	}
}

func TestCorpusCacheLifecycleMarksOnlyUnknownApplyOutcomeAmbiguous(t *testing.T) {
	unknown := corpusCacheLifecycleFailure("apply retention plan", corpus.ErrOutcomeUnknown)
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if !errors.Is(unknown, domain.ErrCheckFailed) || !errors.Is(unknown, corpus.ErrOutcomeUnknown) ||
		!errors.As(unknown, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
		t.Fatalf("unknown outcome error=%T %v, ambiguous=%v", unknown, unknown, ambiguous)
	}

	ordinary := corpusCacheLifecycleFailure("verify cache inventory", corpus.ErrIntegrity)
	ambiguous = nil
	if !errors.As(ordinary, &ambiguous) || ambiguous.DiagnosticAmbiguousWrite() {
		t.Fatalf("ordinary error=%T %v, ambiguous=%v", ordinary, ordinary, ambiguous)
	}
}

func newCorpusBuildCacheConfluenceFixture(budgeted bool) *corpusBuildConfluenceStore {
	store := newCorpusBuildConfluenceFixture(budgeted)
	restricted := false
	store.pages["10"].Updated = "2026-08-13T00:00:00Z"
	store.pages["10"].Restricted = &restricted
	store.pages["10"].Labels = []string{"alpha"}
	store.pages["20"].Updated = "2026-08-13T00:00:00Z"
	store.pages["20"].Restricted = &restricted
	store.pages["20"].Labels = []string{"beta"}
	store.pages["20"].Parent = "10"
	store.pages["20"].Ancestors = []string{store.pages["10"].Title}
	store.pages["20"].AncestorIDs = []string{"10"}
	return store
}

func newCorpusBuildCacheTestService(store *corpusBuildConfluenceStore) *CorpusBuildService {
	return NewCorpusBuildService(CorpusBuildDependencies{
		Confluence: &ConfluenceService{
			store: store, corpusMetadata: store, baseURL: confluenceTestBackendURL,
			requestMaxInFlight: 2, requestsPerSecond: 100,
		},
		GeneratorVersion: "test-v1", GeneratorCommit: strings.Repeat("a", 40),
		BuildState: corpus.BuildStateClean, ConfluenceTrustDigest: strings.Repeat("b", 64),
	})
}

func corpusBuildCacheTestOptions(root, cacheRoot string) CorpusBuildOptions {
	options := corpusBuildTestOptions(root)
	options.CacheRoot = cacheRoot
	options.CacheMaxRequests = 10
	options.CacheMaxResponseBytes = 1 << 20
	options.CacheDeadline = 30 * time.Second
	options.ConfluenceSpace = "DOC"
	options.MaxConfluencePages = 2
	return options
}
