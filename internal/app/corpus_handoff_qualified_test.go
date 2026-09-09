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

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

type corpusHandoffQualifierStub struct {
	deadline      int64
	candidate     domain.BrokerCacheCandidate
	qualifyCalls  int
	validateCalls int
	failValidate  int
	afterQualify  func()
	onValidate    func(int)
	credential    []byte
}

func (stub *corpusHandoffQualifierStub) sessionCredential() []byte {
	if len(stub.credential) == 0 {
		return []byte("synthetic-stub-session")
	}
	return stub.credential
}

func (stub *corpusHandoffQualifierStub) QualifyCache(_ context.Context, candidate domain.BrokerCacheCandidate) (domain.BrokerCacheGrant, error) {
	stub.qualifyCalls++
	stub.candidate = candidate
	digest, _ := brokercontract.CacheCandidateSHA256V1(candidate)
	if stub.afterQualify != nil {
		stub.afterQualify()
	}
	return domain.BrokerCacheGrant{CandidateSHA256: digest, Decision: domain.BrokerCacheQualification{Status: domain.BrokerDecisionAllowed}, ReleaseDeadlineMillis: stub.deadline, Lease: domain.NewBrokerCacheGrantLease(stub.sessionCredential(), time.UnixMilli(stub.deadline))}, nil
}

func (stub *corpusHandoffQualifierStub) ValidateCacheGrant(candidate domain.BrokerCacheCandidate, grant domain.BrokerCacheGrant) error {
	stub.validateCalls++
	if stub.onValidate != nil {
		stub.onValidate(stub.validateCalls)
	}
	digest, err := brokercontract.CacheCandidateSHA256V1(candidate)
	if err != nil || digest != grant.CandidateSHA256 || stub.failValidate == stub.validateCalls || !grant.Lease.Matches(stub.sessionCredential(), time.Now()) {
		return domain.ErrCheckFailed
	}
	return nil
}

func seedQualifiedCorpusHandoffCache(t *testing.T) (string, *CorpusBuildResult) {
	t.Helper()
	cacheRoot := corpusBuildPrivateRoot(t)
	service := newCorpusBuildCacheTestService(newCorpusBuildCacheConfluenceFixture(true))
	options := corpusBuildCacheTestOptions(corpusBuildPrivateRoot(t), cacheRoot)
	options.Initialize, options.InitializeCache = true, true
	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	return cacheRoot, result
}

func qualifiedCorpusHandoffOptions(root, artifact string) QualifiedCorpusHandoffOptions {
	return QualifiedCorpusHandoffOptions{StoreRoot: root, HandoffArtifact: artifact, GeneratorVersion: "test-v1", GeneratorCommit: strings.Repeat("a", 40), BuildState: corpus.BuildStateClean}
}

func TestPrepareQualifiedCorpusHandoffUsesVerifiedGenerationAndWritesOnlyRoute(t *testing.T) {
	root, built := seedQualifiedCorpusHandoffCache(t)
	now := time.Now().UTC()
	artifactRoot := corpusBuildPrivateRoot(t)
	artifact := filepath.Join(artifactRoot, "handoff.json")
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli()}
	result, err := PrepareQualifiedCorpusHandoff(t.Context(), qualifiedCorpusHandoffOptions(root, artifact), qualifier)
	if err != nil || result == nil || result.Qualification != "current_allowed" || !result.HandoffArtifactWritten || result.Generation.GenerationDigest != built.Generation.GenerationDigest || qualifier.qualifyCalls != 1 || qualifier.validateCalls != 3 {
		t.Fatalf("result=%#v qualifier=%#v error=%v", result, qualifier, err)
	}
	if qualifier.candidate.Operation != domain.BrokerOperationConfluencePageRead || qualifier.candidate.GenerationSHA256 != built.Generation.GenerationDigest || qualifier.candidate.EvidenceSchemaSHA256 != brokercontract.CacheEvidenceSchemaSHA256V1() {
		t.Fatalf("candidate=%#v", qualifier.candidate)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, artifactRoot, "DOC", "fixture-confluence-principal", "Page 10"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("result leaked %q: %s", private, encoded)
		}
	}
	body, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := corpus.ParseIndexerHandoff(body, corpus.Limits{})
	if err != nil || handoff.GenerationDigest != built.Generation.GenerationDigest {
		t.Fatalf("handoff=%#v error=%v", handoff, err)
	}
	store, err := corpus.Open(root, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.SelectCurrent(t.Context())
	if err != nil || current.Receipt().GenerationDigest != built.Generation.GenerationDigest {
		t.Fatalf("current=%#v error=%v", current, err)
	}
	_ = current.Close()
	_ = store.Close()
}

func TestPrepareQualifiedCorpusHandoffRejectsIneligibleBeforeAuthority(t *testing.T) {
	root, _ := seedQualifiedCorpusHandoffCache(t)
	now := time.Now().UTC()
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli()}
	options := qualifiedCorpusHandoffOptions(root, filepath.Join(corpusBuildPrivateRoot(t), "handoff.json"))
	options.GeneratorCommit = strings.Repeat("b", 40)
	result, err := PrepareQualifiedCorpusHandoff(t.Context(), options, qualifier)
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || qualifier.qualifyCalls != 0 {
		t.Fatalf("result=%#v calls=%d error=%v", result, qualifier.qualifyCalls, err)
	}
}

func TestPrepareQualifiedCorpusHandoffRejectsTamperedBytesBeforeAuthority(t *testing.T) {
	root, _ := seedQualifiedCorpusHandoffCache(t)
	store, err := corpus.Open(root, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.SelectCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	generationID := current.ID()
	var path string
	for _, member := range current.Manifest().Members {
		if member.Role == corpus.RoleDocument {
			path = member.Path
			break
		}
	}
	_ = current.Close()
	_ = store.Close()
	if path == "" {
		t.Fatal("document member missing")
	}
	if err := os.WriteFile(filepath.Join(root, "generations", generationID, "artifacts", filepath.FromSlash(path)), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli()}
	result, err := PrepareQualifiedCorpusHandoff(t.Context(), qualifiedCorpusHandoffOptions(root, filepath.Join(corpusBuildPrivateRoot(t), "handoff.json")), qualifier)
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || qualifier.qualifyCalls != 0 {
		t.Fatalf("result=%#v calls=%d error=%v", result, qualifier.qualifyCalls, err)
	}
}

func TestPrepareQualifiedCorpusHandoffPostWriteFailureLeavesNonauthoritativeRoute(t *testing.T) {
	root, built := seedQualifiedCorpusHandoffCache(t)
	now := time.Now().UTC()
	artifact := filepath.Join(corpusBuildPrivateRoot(t), "handoff.json")
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli(), failValidate: 3}
	result, err := PrepareQualifiedCorpusHandoff(t.Context(), qualifiedCorpusHandoffOptions(root, artifact), qualifier)
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, statErr := os.Stat(artifact); statErr != nil {
		t.Fatalf("non-authoritative route was removed: %v", statErr)
	}
	store, err := corpus.Open(root, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.SelectCurrent(t.Context())
	if err != nil || current.Receipt().GenerationDigest != built.Generation.GenerationDigest {
		t.Fatalf("current changed: current=%#v error=%v", current, err)
	}
	_ = current.Close()
	_ = store.Close()
}

func TestPrepareQualifiedCorpusHandoffPointerDriftWritesNoRoute(t *testing.T) {
	root, _ := seedQualifiedCorpusHandoffCache(t)
	now := time.Now().UTC()
	artifact := filepath.Join(corpusBuildPrivateRoot(t), "handoff.json")
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli(), afterQualify: func() {
		if err := os.WriteFile(filepath.Join(root, "current.v1.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := PrepareQualifiedCorpusHandoff(t.Context(), qualifiedCorpusHandoffOptions(root, artifact), qualifier)
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, statErr := os.Lstat(artifact); !os.IsNotExist(statErr) {
		t.Fatalf("route exists after pointer drift: %v", statErr)
	}
}

func TestPrepareQualifiedCorpusHandoffCancellationBeforeWriteCreatesNoRoute(t *testing.T) {
	root, _ := seedQualifiedCorpusHandoffCache(t)
	now := time.Now()
	artifact := filepath.Join(corpusBuildPrivateRoot(t), "handoff.json")
	ctx, cancel := context.WithCancel(t.Context())
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli(), onValidate: func(call int) {
		if call == 2 {
			cancel()
		}
	}}
	result, err := PrepareQualifiedCorpusHandoff(ctx, qualifiedCorpusHandoffOptions(root, artifact), qualifier)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, statErr := os.Lstat(artifact); !os.IsNotExist(statErr) {
		t.Fatalf("route exists after pre-write cancellation: %v", statErr)
	}
}

func TestPrepareQualifiedCorpusHandoffCancellationDuringWriteCannotSucceed(t *testing.T) {
	root, _ := seedQualifiedCorpusHandoffCache(t)
	now := time.Now()
	artifact := filepath.Join(corpusBuildPrivateRoot(t), "handoff.json")
	ctx, cancel := context.WithCancel(t.Context())
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli()}
	writer := func(storeRoot, target string, handoff corpus.IndexerHandoff, limits corpus.Limits) (bool, error) {
		written, err := writeCorpusHandoffArtifact(storeRoot, target, handoff, limits)
		cancel()
		return written, err
	}
	result, err := prepareQualifiedCorpusHandoff(ctx, qualifiedCorpusHandoffOptions(root, artifact), qualifier, writer, (*corpus.Store).ConfirmCurrent)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, statErr := os.Stat(artifact); statErr != nil {
		t.Fatalf("non-authoritative route missing after in-write cancellation: %v", statErr)
	}
}

func TestPrepareQualifiedCorpusHandoffReconfirmsCurrentAfterWrite(t *testing.T) {
	root, _ := seedQualifiedCorpusHandoffCache(t)
	now := time.Now()
	artifact := filepath.Join(corpusBuildPrivateRoot(t), "handoff.json")
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli()}
	confirmCalls := 0
	confirm := func(store *corpus.Store, ctx context.Context, generation *corpus.Generation) error {
		confirmCalls++
		if confirmCalls == 3 {
			if err := os.WriteFile(filepath.Join(root, "current.v1.json"), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return store.ConfirmCurrent(ctx, generation)
	}
	result, err := prepareQualifiedCorpusHandoff(t.Context(), qualifiedCorpusHandoffOptions(root, artifact), qualifier, writeCorpusHandoffArtifact, confirm)
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || confirmCalls != 3 {
		t.Fatalf("result=%#v confirms=%d error=%v", result, confirmCalls, err)
	}
	if _, statErr := os.Stat(artifact); statErr != nil {
		t.Fatalf("non-authoritative route missing after post-write drift: %v", statErr)
	}
}

func TestPrepareQualifiedCorpusHandoffRejectsCredentialRotationDuringFinalRescan(t *testing.T) {
	root, _ := seedQualifiedCorpusHandoffCache(t)
	now := time.Now()
	artifact := filepath.Join(corpusBuildPrivateRoot(t), "handoff.json")
	qualifier := &corpusHandoffQualifierStub{deadline: now.Add(time.Minute).UnixMilli()}
	confirmCalls := 0
	confirm := func(store *corpus.Store, ctx context.Context, generation *corpus.Generation) error {
		confirmCalls++
		err := store.ConfirmCurrent(ctx, generation)
		if confirmCalls == 3 {
			qualifier.credential = []byte("replacement-stub-session")
		}
		return err
	}
	result, err := prepareQualifiedCorpusHandoff(t.Context(), qualifiedCorpusHandoffOptions(root, artifact), qualifier, writeCorpusHandoffArtifact, confirm)
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || confirmCalls != 3 || qualifier.validateCalls != 3 {
		t.Fatalf("result=%#v confirms=%d validates=%d error=%v", result, confirmCalls, qualifier.validateCalls, err)
	}
	if _, statErr := os.Stat(artifact); statErr != nil {
		t.Fatalf("non-authoritative route missing after credential rotation: %v", statErr)
	}
}
