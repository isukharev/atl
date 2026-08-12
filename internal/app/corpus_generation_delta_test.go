package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestExportCorpusDerivesAndVerifiesQualifiedGenerationDelta(t *testing.T) {
	beforeRoot := t.TempDir()
	seedCorpusExportJira(t, beforeRoot, "OLD-1", "10001", "OLD/OLD-1.wiki", []byte("changed body"), map[string]any{"summary": "Before"})
	seedCorpusExportJira(t, beforeRoot, "OLD-2", "10002", "OLD/OLD-2.wiki", []byte("removed body"), map[string]any{"summary": "Removed"})
	seedCorpusExportJira(t, beforeRoot, "KEEP-4", "10004", "KEEP/KEEP-4.wiki", []byte("retained body"), map[string]any{"summary": "Retained"})
	afterRoot := t.TempDir()
	seedCorpusExportJira(t, afterRoot, "NEW-1", "10001", "NEW/moved/NEW-1.wiki", []byte("changed body"), map[string]any{"summary": "After"})
	seedCorpusExportJira(t, afterRoot, "NEW-3", "10003", "NEW/NEW-3.wiki", []byte("added body"), map[string]any{"summary": "Added"})
	seedCorpusExportJira(t, afterRoot, "KEEP-4", "10004", "KEEP/KEEP-4.wiki", []byte("retained body"), map[string]any{"summary": "Retained"})

	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeCapture := corpusExportCaptureReceipt(t, corpus.ServiceJira, beforeRoot)
	before, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: beforeRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{beforeCapture},
	})
	if err != nil {
		t.Fatal(err)
	}
	afterCapture := corpusExportCaptureReceipt(t, corpus.ServiceJira, afterRoot)
	afterOptions := CorpusExportOptions{
		JiraRoot: afterRoot, StoreRoot: storeRoot,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{afterCapture},
	}
	after, err := ExportCorpus(context.Background(), afterOptions)
	if err != nil || after.Reused || after.Generation.GenerationDigest == before.Generation.GenerationDigest {
		t.Fatalf("successor=%#v error=%v", after, err)
	}

	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifest := selected.Manifest()
	if manifest.PredecessorDigest != before.Generation.GenerationDigest || manifest.TombstoneDigest == "" {
		t.Fatalf("lineage=%#v", manifest)
	}
	delta, err := verifyCorpusGenerationDelta(context.Background(), store, selected, corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := corpus.GenerationDeltaCounts{Added: 1, Retained: 1, Changed: 1, Tombstoned: 1}
	if delta.Counts != wantCounts || delta.PredecessorGenerationDigest != before.Generation.GenerationDigest ||
		delta.SuccessorProjectionDigest != after.Projection.ProjectionDigest {
		t.Fatalf("delta=%#v", delta)
	}
	wantStates := map[string]corpus.GenerationDeltaState{
		corpusDeltaIssueID(t, "10001"): corpus.GenerationDeltaChanged,
		corpusDeltaIssueID(t, "10002"): corpus.GenerationDeltaTombstoned,
		corpusDeltaIssueID(t, "10003"): corpus.GenerationDeltaAdded,
		corpusDeltaIssueID(t, "10004"): corpus.GenerationDeltaRetained,
	}
	for _, record := range delta.Records {
		if wantStates[record.ID] != record.State {
			t.Fatalf("record=%#v states=%#v", record, wantStates)
		}
		if record.State == corpus.GenerationDeltaTombstoned && record.Reason != corpus.GenerationDeltaAbsentQualified {
			t.Fatalf("tombstone=%#v", record)
		}
		delete(wantStates, record.ID)
	}
	if len(wantStates) != 0 {
		t.Fatalf("missing states=%#v", wantStates)
	}
	_ = selected.Close()
	_ = store.Close()

	again, err := ExportCorpus(context.Background(), afterOptions)
	if err != nil || !again.Reused || again.Generation.GenerationDigest != after.Generation.GenerationDigest {
		t.Fatalf("reuse=%#v error=%v", again, err)
	}

	diff, err := DiffCorpusGeneration(context.Background(), CorpusGenerationDiffOptions{StoreRoot: storeRoot})
	if err != nil || diff.Counts != wantCounts || diff.Qualification != "qualified" ||
		diff.Reason != corpus.GenerationDeltaAbsentQualified || diff.IdentityArtifactWritten {
		t.Fatalf("diff=%#v error=%v", diff, err)
	}
	encoded, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"OLD-2", "NEW/moved", corpusDeltaIssueID(t, "10002")} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("public result contains private identity %q: %s", private, encoded)
		}
	}

	artifactDir := t.TempDir()
	if err := os.Chmod(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactDir, "identities.json")
	withArtifact, err := DiffCorpusGeneration(context.Background(), CorpusGenerationDiffOptions{
		StoreRoot: storeRoot, IdentityArtifact: artifactPath,
	})
	if err != nil || !withArtifact.IdentityArtifactWritten {
		t.Fatalf("artifact result=%#v error=%v", withArtifact, err)
	}
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := corpus.ParseGenerationDiffArtifact(artifactBytes, corpus.Limits{})
	if err != nil || artifact.Counts != wantCounts || artifact.SuccessorGenerationDigest != after.Generation.GenerationDigest {
		t.Fatalf("artifact=%#v error=%v", artifact, err)
	}
	if info, statErr := os.Stat(artifactPath); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode=%v error=%v", info, statErr)
	}
	if _, err := DiffCorpusGeneration(context.Background(), CorpusGenerationDiffOptions{
		StoreRoot: storeRoot, IdentityArtifact: artifactPath,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("replacement error=%v", err)
	}
	preserved, err := os.ReadFile(artifactPath)
	if err != nil || !bytes.Equal(preserved, artifactBytes) {
		t.Fatalf("artifact changed error=%v", err)
	}

	reappearedRoot := t.TempDir()
	seedCorpusExportJira(t, reappearedRoot, "NEW-1", "10001", "NEW/moved/NEW-1.wiki", []byte("changed body"), map[string]any{"summary": "After"})
	seedCorpusExportJira(t, reappearedRoot, "BACK-2", "10002", "BACK/BACK-2.wiki", []byte("removed body"), map[string]any{"summary": "Reappeared"})
	seedCorpusExportJira(t, reappearedRoot, "NEW-3", "10003", "NEW/NEW-3.wiki", []byte("added body"), map[string]any{"summary": "Added"})
	seedCorpusExportJira(t, reappearedRoot, "KEEP-4", "10004", "KEEP/KEEP-4.wiki", []byte("retained body"), map[string]any{"summary": "Retained"})
	reappearedCapture := corpusExportCaptureReceipt(t, corpus.ServiceJira, reappearedRoot)
	if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: reappearedRoot, StoreRoot: storeRoot,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{reappearedCapture},
	}); err != nil {
		t.Fatal(err)
	}
	store, err = corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	selected, err = store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reappeared, err := verifyCorpusGenerationDelta(context.Background(), store, selected, corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if reappeared.Counts != (corpus.GenerationDeltaCounts{Added: 1, Retained: 3}) {
		t.Fatalf("reappearance counts=%#v", reappeared.Counts)
	}
	for _, record := range reappeared.Records {
		if record.ID == corpusDeltaIssueID(t, "10002") && (record.State != corpus.GenerationDeltaAdded || record.Reason != "") {
			t.Fatalf("reappearance record=%#v", record)
		}
	}
	_ = selected.Close()
	_ = store.Close()
}

func TestCorpusDiffRefusesIdentityArtifactInsideSealedStore(t *testing.T) {
	beforeRoot := t.TempDir()
	seedCorpusExportJira(t, beforeRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("before"), map[string]any{"summary": "Before"})
	afterRoot := t.TempDir()
	seedCorpusExportJira(t, afterRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("after"), map[string]any{"summary": "After"})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: beforeRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{corpusExportCaptureReceipt(t, corpus.ServiceJira, beforeRoot)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: afterRoot, StoreRoot: storeRoot,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{corpusExportCaptureReceipt(t, corpus.ServiceJira, afterRoot)},
	}); err != nil {
		t.Fatal(err)
	}

	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sealedArtifacts := filepath.Join(storeRoot, "generations", selected.ID(), "artifacts")
	_ = selected.Close()
	_ = store.Close()

	targets := []string{filepath.Join(sealedArtifacts, "unmanifested.json")}
	aliasRoot := t.TempDir()
	if err := os.Chmod(aliasRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(aliasRoot, "sealed-artifacts")
	if err := os.Symlink(sealedArtifacts, alias); err == nil {
		targets = append(targets, filepath.Join(alias, "aliased.json"))
	}
	for _, target := range targets {
		if result, err := DiffCorpusGeneration(context.Background(), CorpusGenerationDiffOptions{
			StoreRoot: storeRoot, IdentityArtifact: target,
		}); result != nil || !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("target=%q result=%#v error=%v", target, result, err)
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("refused target exists: %v", err)
		}
	}

	store, err = corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	selected, err = store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatalf("sealed generation was corrupted: %v", err)
	}
	_ = selected.Close()
	_ = store.Close()
}

func TestExportCorpusDoesNotDeriveDeltaAcrossQualificationDrift(t *testing.T) {
	beforeRoot := t.TempDir()
	seedCorpusExportJira(t, beforeRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("before"), map[string]any{"summary": "Before"})
	afterRoot := t.TempDir()
	seedCorpusExportJira(t, afterRoot, "EX-2", "10002", "EX/EX-2.wiki", []byte("after"), map[string]any{"summary": "After"})
	beforeCapture := corpusExportCaptureReceipt(t, corpus.ServiceJira, beforeRoot)
	afterCapture := corpusExportCaptureReceipt(t, corpus.ServiceJira, afterRoot)

	tests := map[string][]corpus.CaptureReceipt{
		"structural successor": nil,
		"scope drift": {rebuildCorpusExportCapture(t, afterCapture, func(input *corpus.CaptureReceiptInput) {
			input.ScopeDigest = strings.Repeat("c", 64)
		})},
		"selector drift": {rebuildCorpusExportCapture(t, afterCapture, func(input *corpus.CaptureReceiptInput) {
			input.SelectorDigest = strings.Repeat("c", 64)
		})},
		"options drift": {rebuildCorpusExportCapture(t, afterCapture, func(input *corpus.CaptureReceiptInput) {
			input.OptionsDigest = strings.Repeat("c", 64)
		})},
	}
	for name, successorReceipts := range tests {
		t.Run(name, func(t *testing.T) {
			storeRoot := t.TempDir()
			if err := os.Chmod(storeRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
				JiraRoot: beforeRoot, StoreRoot: storeRoot, InitializeStore: true,
				GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
				CaptureReceipts: []corpus.CaptureReceipt{beforeCapture},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
				JiraRoot: afterRoot, StoreRoot: storeRoot,
				GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
				CaptureReceipts: successorReceipts,
			}); err != nil {
				t.Fatal(err)
			}
			store, err := corpus.Open(storeRoot, corpus.Options{})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := store.SelectCurrent(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if selected.Manifest().TombstoneDigest != "" || corpusGenerationTombstoneMembers(selected.Manifest()) != 0 {
				t.Fatalf("unexpected tombstone manifest=%#v", selected.Manifest())
			}
			_ = selected.Close()
			_ = store.Close()
		})
	}

	t.Run("partial dimension refuses publication", func(t *testing.T) {
		partial := rebuildCorpusExportCapture(t, afterCapture, func(input *corpus.CaptureReceiptInput) {
			for index := range input.Dimensions {
				if input.Dimensions[index].Dimension == corpus.CaptureComments {
					input.Dimensions[index].State = corpus.CapturePartial
				}
			}
		})
		storeRoot := t.TempDir()
		if err := os.Chmod(storeRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		before, err := ExportCorpus(context.Background(), CorpusExportOptions{
			JiraRoot: beforeRoot, StoreRoot: storeRoot, InitializeStore: true,
			GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
			CaptureReceipts: []corpus.CaptureReceipt{beforeCapture},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
			JiraRoot: afterRoot, StoreRoot: storeRoot,
			GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
			CaptureReceipts: []corpus.CaptureReceipt{partial},
		}); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("partial capture error=%v", err)
		}
		store, err := corpus.Open(storeRoot, corpus.Options{})
		if err != nil {
			t.Fatal(err)
		}
		selected, err := store.SelectCurrent(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if selected.Receipt().GenerationDigest != before.Generation.GenerationDigest || selected.Manifest().TombstoneDigest != "" {
			t.Fatalf("partial capture changed current=%#v", selected.Summary())
		}
		_ = selected.Close()
		_ = store.Close()
	})

	t.Run("service drift", func(t *testing.T) {
		storeRoot := t.TempDir()
		if err := os.Chmod(storeRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
			JiraRoot: beforeRoot, StoreRoot: storeRoot, InitializeStore: true,
			GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
			CaptureReceipts: []corpus.CaptureReceipt{beforeCapture},
		}); err != nil {
			t.Fatal(err)
		}
		confluenceRoot := t.TempDir()
		seedCorpusExportConfluence(t, confluenceRoot, "20001", "DOC/page.csf", []byte("<p>page</p>"), mirror.Meta{
			ID: "20001", Title: "Page", Space: "DOC", Version: 1,
		})
		confluenceCapture := corpusExportCaptureReceipt(t, corpus.ServiceConfluence, confluenceRoot)
		if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
			ConfluenceRoot: confluenceRoot, StoreRoot: storeRoot,
			GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
			CaptureReceipts: []corpus.CaptureReceipt{confluenceCapture},
		}); err != nil {
			t.Fatal(err)
		}
		store, err := corpus.Open(storeRoot, corpus.Options{})
		if err != nil {
			t.Fatal(err)
		}
		selected, err := store.SelectCurrent(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if selected.Manifest().TombstoneDigest != "" || corpusGenerationTombstoneMembers(selected.Manifest()) != 0 {
			t.Fatalf("unexpected service-drift tombstone=%#v", selected.Manifest())
		}
		_ = selected.Close()
		_ = store.Close()
	})
}

func TestExportCorpusRejectsSemanticallyInvalidCurrentDelta(t *testing.T) {
	root := t.TempDir()
	seedCorpusExportJira(t, root, "EX-1", "10001", "EX/EX-1.wiki", []byte("body"), map[string]any{"summary": "Issue"})
	capture := corpusExportCaptureReceipt(t, corpus.ServiceJira, root)
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	options := CorpusExportOptions{
		JiraRoot: root, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{capture},
	}
	if _, err := ExportCorpus(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range current.Manifest().Members {
		var data bytes.Buffer
		if _, err := current.CopyMember(context.Background(), member.Service, member.StableID, member.Role, &data); err != nil {
			t.Fatal(err)
		}
		if err := stage.Add(context.Background(), corpus.MemberSpec{
			Service: member.Service, StableID: member.StableID, Role: member.Role, Path: member.Path,
		}, bytes.NewReader(data.Bytes())); err != nil {
			t.Fatal(err)
		}
	}
	invalidDelta := []byte("{}\n")
	if err := stage.Add(context.Background(), corpus.MemberSpec{
		Service: corpus.ServiceJira, StableID: corpusDeltaStableID, Role: corpus.RoleTombstone,
		Path: corpusDeltaMemberPath(corpus.ServiceJira),
	}, bytes.NewReader(invalidDelta)); err != nil {
		t.Fatal(err)
	}
	manifest := current.Manifest()
	predecessor := current.Receipt().GenerationDigest
	_ = current.Close()
	sealed, err := stage.Seal(context.Background(), corpus.SealOptions{
		ProjectionSchema: corpus.IndexerSchemaV2, GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		PredecessorDigest: predecessor, Qualifications: manifest.Qualifications,
		TombstoneDigest: corpusBytesSHA256(invalidDelta),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = sealed.Close()
	if _, err := store.Publish(context.Background(), stage.ID()); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	options.InitializeStore = false
	if _, err := ExportCorpus(context.Background(), options); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("semantic delta error=%v", err)
	}
}

func TestCompleteCorpusDeltaCaptureRejectsPartialDimensions(t *testing.T) {
	receipt := rebuildCorpusExportCapture(t, corpusExportCaptureReceipt(t, corpus.ServiceJira, func() string {
		root := t.TempDir()
		seedCorpusExportJira(t, root, "EX-1", "10001", "EX/EX-1.wiki", []byte("body"), map[string]any{"summary": "Issue"})
		return root
	}()), func(input *corpus.CaptureReceiptInput) {
		for index := range input.Dimensions {
			if input.Dimensions[index].Dimension == corpus.CaptureComments {
				input.Dimensions[index].State = corpus.CapturePartial
			}
		}
	})
	if completeCorpusDeltaCapture(receipt, corpus.Limits{}) {
		t.Fatal("partial capture was accepted as complete delta evidence")
	}
}

func rebuildCorpusExportCapture(t *testing.T, receipt corpus.CaptureReceipt, mutate func(*corpus.CaptureReceiptInput)) corpus.CaptureReceipt {
	t.Helper()
	started, err := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	input := corpus.CaptureReceiptInput{
		Service: receipt.Service, ScopeDigest: receipt.ScopeDigest,
		SelectorDigest: receipt.SelectorDigest, OptionsDigest: receipt.OptionsDigest,
		SelectionDigest: receipt.SelectionDigest, SnapshotDigest: receipt.SnapshotDigest,
		StartedAt: started, CompletedAt: completed, Total: receipt.Total, Completed: receipt.Completed,
		Usage: receipt.Usage, Dimensions: append([]corpus.CaptureDimensionEvidence(nil), receipt.Dimensions...),
	}
	mutate(&input)
	rebuilt, err := corpus.BuildCaptureReceipt(input, corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return rebuilt
}

func corpusDeltaIssueID(t *testing.T, providerID string) string {
	t.Helper()
	origin, err := backendid.OriginSHA256("https://backend.example.test")
	if err != nil {
		t.Fatal(err)
	}
	id, err := corpus.StableObjectID(origin, corpus.ServiceJira, corpus.ObjectIssue, providerID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func corpusGenerationTombstoneMembers(manifest corpus.Manifest) int {
	total := 0
	for _, member := range manifest.Members {
		if member.Role == corpus.RoleTombstone {
			total++
		}
	}
	return total
}
