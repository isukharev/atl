package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func TestConfluenceCompletePullAttachmentEvidenceUsesV4Progress(t *testing.T) {
	m := New(t.TempDir())
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	checkpoint := CompletePullCheckpoint{
		Service: CompletePullServiceConfluence, SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10"}, NextIndex: 1,
		Includes: CompletePullIncludeProgress{
			EvidenceComplete: true,
			Attachments:      CompletePullIncludeAggregate{Published: 1, Partial: 1, Reason: domain.ConfluencePullIncludeReasonBodyIncomplete},
		},
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	progressPath, _ := m.completePullProgressPath(completePullTestHash)
	encoded, err := os.ReadFile(progressPath)
	if err != nil || !strings.Contains(string(encoded), `"schema_version": 4`) || !strings.Contains(string(encoded), `"attachments"`) {
		t.Fatalf("progress=%s error=%v", encoded, err)
	}
	got, found, err := m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !found || got.Includes.Attachments != checkpoint.Includes.Attachments {
		t.Fatalf("checkpoint=%+v found=%t error=%v", got, found, err)
	}
	legacyWithAttachment := fmt.Sprintf(`{"schema_version":3,"service":"confluence","selector_sha256":%q,"options_sha256":%q,"selection_sha256":%q,"next_index":1,"includes":{"evidence_complete":true,"assets":{"published":0,"partial":0},"comments":{"published":0,"partial":0},"attachments":{"published":1,"partial":0}}}`,
		checkpoint.SelectorSHA256, checkpoint.OptionsSHA256, checkpoint.SelectionSHA256)
	if err := os.WriteFile(progressPath, []byte(legacyWithAttachment), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CompletePullCheckpoint(completePullTestHash); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("legacy attachment progress error=%v", err)
	}
	attachmentlessV4 := fmt.Sprintf(`{"schema_version":4,"service":"confluence","selector_sha256":%q,"options_sha256":%q,"selection_sha256":%q,"next_index":1,"includes":{"evidence_complete":true,"assets":{"published":0,"partial":0},"comments":{"published":0,"partial":0}}}`,
		checkpoint.SelectorSHA256, checkpoint.OptionsSHA256, checkpoint.SelectionSHA256)
	if err := os.WriteFile(progressPath, []byte(attachmentlessV4), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CompletePullCheckpoint(completePullTestHash); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("attachmentless v4 progress error=%v", err)
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
	for _, schema := range []int{0, completePullConfluenceProgressSchema4 + 1} {
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

func TestConfluenceCompletePullProgressHandlesEmptyEvidenceHonestly(t *testing.T) {
	for _, tc := range []struct {
		name         string
		includes     string
		nextIndex    int
		wantErr      bool
		wantComplete bool
	}{
		{name: "omitted", wantErr: true},
		{name: "null", includes: `,"includes":null`, wantErr: true},
		{name: "empty object with legacy prefix", includes: `,"includes":{}`, nextIndex: 1},
		{name: "empty object without prefix", includes: `,"includes":{}`, wantComplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			body := fmt.Sprintf(`{"schema_version":3,"service":"confluence","selector_sha256":%q,"options_sha256":%q,"selection_sha256":%q,"next_index":%d%s}`,
				checkpoint.SelectorSHA256, checkpoint.OptionsSHA256, checkpoint.SelectionSHA256, tc.nextIndex, tc.includes)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrCheckFailed) {
					t.Fatalf("checkpoint=%+v found=%t error=%v", got, found, err)
				}
				return
			}
			if err != nil || !found || got.NextIndex != tc.nextIndex || got.Includes.EvidenceComplete != tc.wantComplete ||
				got.Includes.Assets != (CompletePullIncludeAggregate{}) || got.Includes.Comments != (CompletePullIncludeAggregate{}) {
				t.Fatalf("checkpoint=%+v found=%t error=%v", got, found, err)
			}
		})
	}
}

func TestConfluenceCompletePullIncludeProgressCannotRegressOrFabricateEvidence(t *testing.T) {
	newCheckpoint := func(t *testing.T, root string, progress CompletePullIncludeProgress, nextIndex int) (*Mirror, CompletePullCheckpoint) {
		t.Helper()
		m := New(root)
		checkpoint := CompletePullCheckpoint{
			Service: CompletePullServiceConfluence, SelectorSHA256: completePullTestHash,
			OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10", "20"},
			NextIndex: nextIndex, Includes: progress,
		}
		if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
			t.Fatal(err)
		}
		return m, checkpoint
	}

	t.Run("erase current evidence", func(t *testing.T) {
		m, checkpoint := newCheckpoint(t, t.TempDir(), CompletePullIncludeProgress{
			EvidenceComplete: true, Comments: CompletePullIncludeAggregate{Published: 1},
		}, 1)
		checkpoint.Includes.Comments = CompletePullIncludeAggregate{}
		if err := m.SaveCompletePullCheckpoint(checkpoint); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("erase error=%v", err)
		}
	})

	t.Run("leave requested suffix uncovered", func(t *testing.T) {
		m, checkpoint := newCheckpoint(t, t.TempDir(), CompletePullIncludeProgress{
			EvidenceComplete: true, Comments: CompletePullIncludeAggregate{Published: 1},
		}, 1)
		checkpoint.NextIndex = 2
		if err := m.SaveCompletePullCheckpoint(checkpoint); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("uncovered suffix error=%v", err)
		}
		got, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
		if err != nil || !found || got.NextIndex != 1 || got.Includes.Comments.Published != 1 {
			t.Fatalf("checkpoint=%+v found=%t error=%v", got, found, err)
		}
	})

	t.Run("advance unrequested dimension without evidence", func(t *testing.T) {
		m, checkpoint := newCheckpoint(t, t.TempDir(), CompletePullIncludeProgress{EvidenceComplete: true}, 0)
		checkpoint.NextIndex = 1
		if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
			t.Fatalf("unrequested advance error=%v", err)
		}
		got, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
		if err != nil || !found || got.NextIndex != 1 || !got.Includes.EvidenceComplete ||
			got.Includes.Comments != (CompletePullIncludeAggregate{}) || got.Includes.Assets != (CompletePullIncludeAggregate{}) {
			t.Fatalf("checkpoint=%+v found=%t error=%v", got, found, err)
		}
	})

	t.Run("improve legacy prefix", func(t *testing.T) {
		m, checkpoint := newCheckpoint(t, t.TempDir(), CompletePullIncludeProgress{}, 1)
		checkpoint.NextIndex = 2
		checkpoint.Includes = CompletePullIncludeProgress{
			EvidenceComplete: true, Comments: CompletePullIncludeAggregate{Published: 1},
		}
		if err := m.SaveCompletePullCheckpoint(checkpoint); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("improvement error=%v", err)
		}
	})

	t.Run("attach evidence to legacy page", func(t *testing.T) {
		m, checkpoint := newCheckpoint(t, t.TempDir(), CompletePullIncludeProgress{}, 1)
		checkpoint.NextIndex = 2
		checkpoint.Includes.Comments = CompletePullIncludeAggregate{Published: 2}
		if err := m.SaveCompletePullCheckpoint(checkpoint); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("fabrication error=%v", err)
		}
	})

	t.Run("extend legacy prefix conservatively", func(t *testing.T) {
		m, checkpoint := newCheckpoint(t, t.TempDir(), CompletePullIncludeProgress{}, 1)
		checkpoint.NextIndex = 2
		checkpoint.Includes.Comments = CompletePullIncludeAggregate{Published: 1}
		if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
			t.Fatalf("extension error=%v", err)
		}
		got, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
		if err != nil || !found || got.NextIndex != 2 || got.Includes.EvidenceComplete || got.Includes.Comments.Published != 1 {
			t.Fatalf("checkpoint=%+v found=%t error=%v", got, found, err)
		}
	})

	t.Run("attachment bytes require a newly published page", func(t *testing.T) {
		previous := CompletePullIncludeProgress{
			EvidenceComplete: true,
			Attachments:      CompletePullIncludeAggregate{Published: 1, BodyBytes: 3},
		}
		next := previous
		next.Attachments.BodyBytes = 4
		if err := validateCompletePullIncludeAdvance(previous, 1, next, 1); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("body-only advance error=%v", err)
		}
	})
}

func TestAttachmentSidecarCapturedBytesRejectsOverLimitBeforeBodyReader(t *testing.T) {
	sidecar := AttachmentSidecarV1{Attachments: []AttachmentSidecarRecord{{
		ID: "7", DeclaredSize: 4,
		Body: AttachmentSidecarBody{
			State: AttachmentBodyCaptured, Path: "DOC/page/page.attachments/7.body", Size: 4, SHA256: strings.Repeat("a", 64),
		},
	}}}
	readers := 0
	if _, err := attachmentSidecarCapturedBytes(sidecar, "DOC/page/page.attachments/", 3, func(string, int64, string) error {
		readers++
		return nil
	}); err == nil || readers != 0 {
		t.Fatalf("over-limit attachment error=%v readers=%d", err, readers)
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

func TestConfluenceCompletePullAttachmentJournalUsesV6(t *testing.T) {
	m, checkpoint, entries := completePullJournalFixture(t, "10")
	checkpoint.Includes.EvidenceComplete = true
	evidence := []domain.ConfluencePullIncludeEvidence{{
		Dimension: domain.ConfluencePullIncludeAttachments, Qualification: domain.ConfluencePullIncludePartial,
		Reason: domain.ConfluencePullIncludeReasonBodyIncomplete,
	}}
	entries[0].Includes = &evidence
	if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
		t.Fatal(err)
	}
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil || !found || journal.SchemaVersion != completePullConfluenceJournalSchema6 {
		t.Fatalf("journal=%+v found=%t err=%v", journal, found, err)
	}
	got, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
	want := CompletePullIncludeAggregate{Published: 1, Partial: 1, Reason: domain.ConfluencePullIncludeReasonBodyIncomplete}
	if err != nil || got.NextIndex != 1 || !got.Includes.EvidenceComplete || got.Includes.Attachments != want {
		t.Fatalf("checkpoint=%+v error=%v", got, err)
	}
	entries[0].Includes = &[]domain.ConfluencePullIncludeEvidence{{
		Dimension: domain.ConfluencePullIncludeAssets, Qualification: domain.ConfluencePullIncludeQualified,
	}}
	if err := validateCompletePullConfluenceEntrySchema(completePullConfluenceJournalSchema6, entries[0]); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("attachmentless v6 journal error=%v", err)
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

func TestConfluenceCompletePullCurrentJournalAndPublicationKeepEmptyEvidenceNonNull(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	empty := make([]domain.ConfluencePullIncludeEvidence, 0)
	entry.Includes = &empty
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	intentPath := filepath.Join(dir, "intent.json")
	intentBytes, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(intentBytes), `"schema_version": 7`) ||
		!strings.Contains(string(intentBytes), `"includes": []`) || strings.Contains(string(intentBytes), `"includes": null`) ||
		!strings.Contains(string(intentBytes), `"pre_size": 9`) {
		t.Fatalf("intent=%s", intentBytes)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	journalPath, _ := m.completePullJournalPath(checkpoint.SelectorSHA256)
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(journalBytes), `"schema_version": 5`) ||
		!strings.Contains(string(journalBytes), `"includes": []`) || strings.Contains(string(journalBytes), `"includes": null`) {
		t.Fatalf("journal=%s", journalBytes)
	}

	var journal completePullJournal
	if err := json.Unmarshal(journalBytes, &journal); err != nil {
		t.Fatal(err)
	}
	journal.Entries[0].Includes = nil
	corrupt, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing current evidence error=%v", err)
	}
}
