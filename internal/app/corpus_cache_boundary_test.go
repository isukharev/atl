package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

func TestCorpusBuildCompletedGenerationFailsClosedAcrossPublicationTargets(t *testing.T) {
	workspaceRoot := corpusBuildPrivateRoot(t)
	workspace, err := corpus.InitializeBuildWorkspace(context.Background(), workspaceRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workspace.Close() }()

	for _, schema := range []int{corpus.BuildActiveSchemaV1, corpus.BuildActiveSchemaV2} {
		generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, nil, corpus.BuildActive{SchemaVersion: schema}, CorpusBuildOptions{})
		if generation != nil || !attempted || !errors.Is(err, corpus.ErrNoCurrent) {
			t.Fatalf("schema=%d generation=%#v attempted=%t error=%v", schema, generation, attempted, err)
		}
	}
	generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, nil, corpus.BuildActive{
		SchemaVersion: corpus.BuildActiveSchemaV3, PublicationTarget: corpus.PublicationTargetWorkspace,
	}, CorpusBuildOptions{})
	if generation != nil || !attempted || !errors.Is(err, corpus.ErrNoCurrent) {
		t.Fatalf("workspace generation=%#v attempted=%t error=%v", generation, attempted, err)
	}

	cacheRoot := corpusBuildPrivateRoot(t)
	cacheStore, err := corpus.Initialize(cacheRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cacheStore.Close() }()
	cacheActive := corpus.BuildActive{SchemaVersion: corpus.BuildActiveSchemaV3, PublicationTarget: corpus.PublicationTargetCache}
	if generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, nil, cacheActive, CorpusBuildOptions{CacheRoot: cacheRoot}); generation != nil || attempted || err != nil {
		t.Fatalf("nil cache generation=%#v attempted=%t error=%v", generation, attempted, err)
	}
	if generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, cacheStore, cacheActive, CorpusBuildOptions{}); generation != nil || attempted || err != nil {
		t.Fatalf("disabled cache generation=%#v attempted=%t error=%v", generation, attempted, err)
	}
	if generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, cacheStore, cacheActive, CorpusBuildOptions{CacheRoot: filepath.Join(cacheRoot, "missing")}); generation != nil || attempted || err == nil {
		t.Fatalf("invalid root generation=%#v attempted=%t error=%v", generation, attempted, err)
	}
	cacheActive.PublicationRootDigest = strings.Repeat("f", 64)
	if generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, cacheStore, cacheActive, CorpusBuildOptions{CacheRoot: cacheRoot}); generation != nil || attempted || err != nil {
		t.Fatalf("mismatch generation=%#v attempted=%t error=%v", generation, attempted, err)
	}
	digest, err := corpus.RootIdentityDigest(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	cacheActive.PublicationRootDigest = digest
	if generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, cacheStore, cacheActive, CorpusBuildOptions{CacheRoot: cacheRoot}); generation != nil || !attempted || !errors.Is(err, corpus.ErrNoCurrent) {
		t.Fatalf("empty cache generation=%#v attempted=%t error=%v", generation, attempted, err)
	}
	for _, active := range []corpus.BuildActive{
		{SchemaVersion: corpus.BuildActiveSchemaV3, PublicationTarget: "other"},
		{SchemaVersion: 99},
	} {
		if generation, attempted, err := selectCorpusBuildCompletedGeneration(context.Background(), workspace, cacheStore, active, CorpusBuildOptions{}); generation != nil || attempted || !errors.Is(err, corpus.ErrIntegrity) {
			t.Fatalf("active=%#v generation=%#v attempted=%t error=%v", active, generation, attempted, err)
		}
	}
}

func TestCorpusCacheLifecycleRejectsIncompleteLocalAuthority(t *testing.T) {
	for name, call := range map[string]func() error{
		"status": func() error {
			//nolint:staticcheck // Exercise the public nil-context rejection contract.
			_, err := StatusCorpusCache(nil, CorpusCacheStatusOptions{})
			return err
		},
		"preview": func() error {
			//nolint:staticcheck // Exercise the public nil-context rejection contract.
			_, err := PreviewCorpusCacheRetention(nil, CorpusCacheRetentionPreviewOptions{})
			return err
		},
		"apply": func() error {
			//nolint:staticcheck // Exercise the public nil-context rejection contract.
			_, err := ApplyCorpusCacheRetention(nil, CorpusCacheRetentionApplyOptions{})
			return err
		},
	} {
		t.Run("usage "+name, func(t *testing.T) {
			if err := call(); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	missing := filepath.Join(corpusBuildPrivateRoot(t), "missing")
	if _, err := StatusCorpusCache(context.Background(), CorpusCacheStatusOptions{StoreRoot: missing}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing status error=%v", err)
	}
	plainRoot := corpusBuildPrivateRoot(t)
	if _, err := PreviewCorpusCacheRetention(context.Background(), CorpusCacheRetentionPreviewOptions{
		StoreRoot: plainRoot, PlanArtifact: filepath.Join(corpusBuildPrivateRoot(t), "plan.json"), RetainPredecessors: 1,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("uninitialized preview error=%v", err)
	}
	emptyRoot := corpusBuildPrivateRoot(t)
	empty, err := corpus.Initialize(emptyRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := StatusCorpusCache(context.Background(), CorpusCacheStatusOptions{StoreRoot: emptyRoot})
	if err != nil || !status.Initialized || status.Current || status.Binding != "absent" {
		t.Fatalf("status=%#v error=%v", status, err)
	}
	corruptRoot := corpusBuildPrivateRoot(t)
	corrupt, err := corpus.Initialize(corruptRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := corrupt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptRoot, "current.v1.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StatusCorpusCache(context.Background(), CorpusCacheStatusOptions{StoreRoot: corruptRoot}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("corrupt current error=%v", err)
	}
	unsafeInventoryRoot := corpusBuildPrivateRoot(t)
	unsafeInventory, err := corpus.Initialize(unsafeInventoryRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unsafeInventory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(unsafeInventoryRoot, "generations", "invalid"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := StatusCorpusCache(context.Background(), CorpusCacheStatusOptions{StoreRoot: unsafeInventoryRoot}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unsafe inventory error=%v", err)
	}
	artifactRoot := corpusBuildPrivateRoot(t)
	artifactPath := filepath.Join(artifactRoot, "plan.json")
	if _, err := PreviewCorpusCacheRetention(context.Background(), CorpusCacheRetentionPreviewOptions{
		StoreRoot: emptyRoot, PlanArtifact: artifactPath, RetainPredecessors: 1,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("empty preview error=%v", err)
	}
	if _, err := ApplyCorpusCacheRetention(context.Background(), CorpusCacheRetentionApplyOptions{
		StoreRoot: emptyRoot, PlanArtifact: artifactPath, ExpectedPlanDigest: strings.Repeat("a", 64), Apply: true,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing artifact error=%v", err)
	}

	canceled := corpusCacheLifecycleFailure("synthetic operation", context.Canceled)
	if !errors.Is(canceled, context.Canceled) {
		t.Fatalf("canceled error=%v", canceled)
	}
	deadline := corpusCacheLifecycleFailure("synthetic operation", context.DeadlineExceeded)
	if !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", deadline)
	}
	failure := corpusCacheLifecycleFailure("inspect cache", corpus.ErrIntegrity)
	if failure.Error() != "corpus cache could not inspect cache" {
		t.Fatalf("error=%q", failure.Error())
	}
	if err := os.WriteFile(artifactPath, []byte("not a plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCorpusCacheRetention(context.Background(), CorpusCacheRetentionApplyOptions{
		StoreRoot: emptyRoot, PlanArtifact: artifactPath, ExpectedPlanDigest: strings.Repeat("a", 64), Apply: true,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid artifact error=%v", err)
	}
}

func TestCorpusCacheRetentionArtifactRemainsExclusiveAndStoreBound(t *testing.T) {
	cacheRoot := corpusBuildPrivateRoot(t)
	store := newCorpusBuildCacheConfluenceFixture(true)
	service := newCorpusBuildCacheTestService(store)
	options := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	options.Initialize, options.InitializeCache = true, true
	if _, err := service.Build(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	artifactRoot := corpusBuildPrivateRoot(t)
	artifactPath := filepath.Join(artifactRoot, "plan.json")
	preview, err := PreviewCorpusCacheRetention(t.Context(), CorpusCacheRetentionPreviewOptions{
		StoreRoot: cacheRoot, RetainPredecessors: 1, PlanArtifact: artifactPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewCorpusCacheRetention(t.Context(), CorpusCacheRetentionPreviewOptions{
		StoreRoot: cacheRoot, RetainPredecessors: 1, PlanArtifact: artifactPath,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("replacement preview error=%v", err)
	}
	missingStore := corpusBuildPrivateRoot(t)
	if _, err := ApplyCorpusCacheRetention(t.Context(), CorpusCacheRetentionApplyOptions{
		StoreRoot: missingStore, PlanArtifact: artifactPath,
		ExpectedPlanDigest: preview.PlanDigest, Apply: true,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("retargeted apply error=%v", err)
	}
}
