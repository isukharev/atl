package mirror

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestConfluenceCompletePullIncludeProgressCurrentRoundTrip(t *testing.T) {
	m := New(t.TempDir())
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	want := CompletePullCheckpoint{
		Service: CompletePullServiceConfluence, SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10", "20"}, NextIndex: 2,
		Includes: CompletePullIncludeProgress{
			EvidenceComplete: true,
			Assets:           CompletePullIncludeAggregate{Published: 2},
			Comments: CompletePullIncludeAggregate{
				Published: 2, Partial: 1, Reason: domain.ConfluencePullIncludeReasonInventoryIncomplete,
			},
		},
	}
	if err := m.SaveCompletePullCheckpoint(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !found || got.NextIndex != want.NextIndex || got.Includes != want.Includes {
		t.Fatalf("got=%+v found=%t err=%v", got, found, err)
	}
	progressPath, _ := m.completePullProgressPath(completePullTestHash)
	encoded, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"schema_version": 3`) || !strings.Contains(string(encoded), `"evidence_complete": true`) ||
		strings.Contains(string(encoded), "title") || strings.Contains(string(encoded), "body") || strings.Contains(string(encoded), "url") {
		t.Fatalf("progress=%s", encoded)
	}
}

func TestConfluenceCompletePullLegacyProgressNeverFabricatesEvidence(t *testing.T) {
	for _, nextIndex := range []int{0, 1} {
		t.Run(fmt.Sprintf("next_%d", nextIndex), func(t *testing.T) {
			m := New(t.TempDir())
			if err := m.EnsureScaffold(); err != nil {
				t.Fatal(err)
			}
			checkpoint := CompletePullCheckpoint{
				Service: CompletePullServiceConfluence, SelectorSHA256: completePullTestHash,
				OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10"},
				Includes: CompletePullIncludeProgress{EvidenceComplete: true},
			}
			if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			path, _ := m.completePullProgressPath(completePullTestHash)
			legacy := fmt.Sprintf(`{"schema_version":1,"selector_sha256":%q,"options_sha256":%q,"selection_sha256":%q,"next_index":%d}`,
				checkpoint.SelectorSHA256, checkpoint.OptionsSHA256, checkpoint.SelectionSHA256, nextIndex)
			if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			got, found, err := m.CompletePullCheckpoint(completePullTestHash)
			if err != nil || !found || got.NextIndex != nextIndex || got.Includes.EvidenceComplete != (nextIndex == 0) ||
				got.Includes.Assets != (CompletePullIncludeAggregate{}) || got.Includes.Comments != (CompletePullIncludeAggregate{}) {
				t.Fatalf("got=%+v found=%t err=%v", got, found, err)
			}
		})
	}
}

func TestConfluenceCompletePullProgressRejectsUnversionedAndFuture(t *testing.T) {
	for _, schema := range []int{0, completePullConfluenceProgressSchema + 1} {
		t.Run(fmt.Sprintf("schema_%d", schema), func(t *testing.T) {
			m := New(t.TempDir())
			if err := m.EnsureScaffold(); err != nil {
				t.Fatal(err)
			}
			checkpoint := CompletePullCheckpoint{
				Service: CompletePullServiceConfluence, SelectorSHA256: completePullTestHash,
				OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10"},
				Includes: CompletePullIncludeProgress{EvidenceComplete: true},
			}
			if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			path, _ := m.completePullProgressPath(completePullTestHash)
			body := fmt.Sprintf(`{"schema_version":%d,"service":"confluence","selector_sha256":%q,"options_sha256":%q,"selection_sha256":%q,"next_index":0,"includes":{"evidence_complete":true,"assets":{"published":0,"partial":0},"comments":{"published":0,"partial":0}}}`,
				schema, checkpoint.SelectorSHA256, checkpoint.OptionsSHA256, checkpoint.SelectionSHA256)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.CompletePullCheckpoint(completePullTestHash); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("schema=%d err=%v", schema, err)
			}
		})
	}
}

func TestConfluenceCompletePullJournalRestoresIncludeAggregate(t *testing.T) {
	m, checkpoint, entries := completePullJournalFixture(t, "10")
	checkpoint.Includes.EvidenceComplete = true
	evidence := []domain.ConfluencePullIncludeEvidence{
		{Dimension: domain.ConfluencePullIncludeAssets, Qualification: domain.ConfluencePullIncludeQualified},
		{Dimension: domain.ConfluencePullIncludeComments, Qualification: domain.ConfluencePullIncludePartial, Reason: domain.ConfluencePullIncludeReasonInventoryIncomplete},
	}
	entries[0].Includes = &evidence
	if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
		t.Fatal(err)
	}
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil || !found || journal.SchemaVersion != completePullConfluenceJournalSchema {
		t.Fatalf("journal=%+v found=%t err=%v", journal, found, err)
	}
	got, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil || got.NextIndex != 1 || !got.Includes.EvidenceComplete ||
		got.Includes.Assets != (CompletePullIncludeAggregate{Published: 1}) ||
		got.Includes.Comments != (CompletePullIncludeAggregate{Published: 1, Partial: 1, Reason: domain.ConfluencePullIncludeReasonInventoryIncomplete}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestConfluenceCompletePullLegacyJournalDemotesAggregateEvidence(t *testing.T) {
	m, checkpoint, entries := completePullJournalFixture(t, "10")
	checkpoint.Includes.EvidenceComplete = true
	if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
		t.Fatal(err)
	}
	got, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil || got.NextIndex != 1 || got.Includes.EvidenceComplete {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
