package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

func TestIssueFieldEvidenceResolvesExactNameAndBoundsNarrative(t *testing.T) {
	value := strings.Repeat("evidence🙂\n", 4000)
	tracker := &jiraFieldInspectTracker{
		defs: []domain.FieldDef{{ID: "customfield_1", Name: "Delivery Notes", Custom: true, Schema: "string"}},
		issue: &domain.Issue{ID: "10001", Key: "PROJ-1", Fields: map[string]any{
			"updated": "2026-07-01T10:00:00.000+0000", "customfield_1": value,
		}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFieldEvidence(context.Background(), "PROJ-1", JiraIssueFieldEvidenceOpts{
		Selector: "Delivery Notes", MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	encodedValue, _ := json.Marshal(result.Value)
	if result.SchemaVersion != 1 || result.Issue.ID != "10001" || result.Issue.Updated == "" || result.Field.ID != "customfield_1" || result.Field.Name != "Delivery Notes" {
		t.Fatalf("identity=%+v field=%+v", result.Issue, result.Field)
	}
	if result.Complete || !result.Truncated || len(encodedValue) != result.EmittedValueBytes || result.EmittedValueBytes > 1024 || result.OriginalValueBytes <= result.EmittedValueBytes {
		t.Fatalf("result=%+v encoded=%d", result, len(encodedValue))
	}
	if text := result.Value.(string); !utf8.ValidString(text) || text == "" {
		t.Fatalf("bounded text is not a non-empty UTF-8 prefix: %q", text)
	}
	if strings.Join(tracker.requested, ",") != "customfield_1,updated" || tracker.fieldReads != 1 {
		t.Fatalf("requested=%v fieldReads=%d", tracker.requested, tracker.fieldReads)
	}
}

func TestIssueFieldEvidenceCompactsUsersWithoutPII(t *testing.T) {
	tracker := &jiraFieldInspectTracker{
		defs: []domain.FieldDef{{ID: "assignee", Name: "Assignee", Schema: "user"}},
		issue: &domain.Issue{Key: "PROJ-1", Fields: map[string]any{
			"updated":  "2026-07-01T10:00:00Z",
			"assignee": map[string]any{"name": "alice", "displayName": "Alice", "emailAddress": "private@example.test", "avatarUrls": map[string]any{"48x48": "https://example.test/avatar"}, "self": "https://example.test/user"},
		}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFieldEvidence(context.Background(), "PROJ-1", JiraIssueFieldEvidenceOpts{Selector: "assignee"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	text := string(encoded)
	for _, forbidden := range []string{"private@example.test", "avatar", "https://"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("bounded evidence leaked %q: %s", forbidden, text)
		}
	}
	if !result.Complete || result.Truncated || !strings.Contains(text, `"kind":"user"`) {
		t.Fatalf("result=%s", text)
	}
}

func TestIssueFieldEvidenceTechnicalIDSkipsCatalog(t *testing.T) {
	tracker := &jiraFieldInspectTracker{
		issue: &domain.Issue{Key: "PROJ-1", Fields: map[string]any{
			"updated": "2026-07-01T10:00:00Z", "customfield_123": "evidence",
		}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFieldEvidence(context.Background(), "PROJ-1", JiraIssueFieldEvidenceOpts{Selector: "customfield_123"})
	if err != nil || tracker.fieldReads != 0 || result.Field.ID != "customfield_123" || result.Field.Name != "customfield_123" {
		t.Fatalf("result=%+v fieldReads=%d err=%v", result, tracker.fieldReads, err)
	}
}

func TestIssueFieldEvidenceFailsClosedBeforeIssueRead(t *testing.T) {
	tracker := &jiraFieldInspectTracker{defs: []domain.FieldDef{
		{ID: "customfield_1", Name: "Risk"}, {ID: "customfield_2", Name: "Risk"},
	}}
	_, err := (&JiraService{tr: tracker}).IssueFieldEvidence(context.Background(), "PROJ-1", JiraIssueFieldEvidenceOpts{Selector: "Risk"})
	if !errors.Is(err, domain.ErrCheckFailed) || len(tracker.requested) != 0 {
		t.Fatalf("ambiguous err=%v requested=%v", err, tracker.requested)
	}
	_, err = (&JiraService{tr: tracker}).IssueFieldEvidence(context.Background(), "PROJ-1", JiraIssueFieldEvidenceOpts{Selector: "customfield_1", MaxBytes: 128})
	if !errors.Is(err, domain.ErrUsage) || len(tracker.requested) != 0 {
		t.Fatalf("bound err=%v requested=%v", err, tracker.requested)
	}
}

func TestIssueFieldEvidenceRequiresUpdatedProvenance(t *testing.T) {
	tracker := &jiraFieldInspectTracker{
		defs:  []domain.FieldDef{{ID: "summary", Name: "Summary"}},
		issue: &domain.Issue{Key: "PROJ-1", Fields: map[string]any{"summary": "Plan"}},
	}
	_, err := (&JiraService{tr: tracker}).IssueFieldEvidence(context.Background(), "PROJ-1", JiraIssueFieldEvidenceOpts{Selector: "Summary"})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "updated provenance") {
		t.Fatalf("err=%v", err)
	}
}

func TestBoundedCompactJiraFieldValueAlwaysHonorsEncodedCap(t *testing.T) {
	value := make([]any, 200)
	for i := range value {
		value[i] = map[string]any{"value": strings.Repeat(`"\\`, 1000), "id": strings.Repeat("x", 1000)}
	}
	compact, emitted, truncated, err := boundedCompactJiraFieldValue(value, JiraIssueFieldEvidenceMinMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(compact)
	if !truncated || emitted != len(encoded) || emitted > JiraIssueFieldEvidenceMinMaxBytes {
		t.Fatalf("emitted=%d encoded=%d truncated=%t value=%s", emitted, len(encoded), truncated, encoded)
	}
}

func TestLongestJSONEncodedStringPrefixMatchesBruteForce(t *testing.T) {
	for _, value := range []string{`plain text`, `quotes " and slash \\`, "emoji🙂🙂\ncontrol\tend"} {
		for cap := 2; cap <= len(value)*6+2; cap++ {
			got := longestJSONEncodedStringPrefix(value, cap)
			want := ""
			for end := range len(value) + 1 {
				candidate := value[:end]
				if !utf8.ValidString(candidate) {
					continue
				}
				encoded, _ := json.Marshal(candidate)
				if len(encoded) <= cap {
					want = candidate
				}
			}
			if got != want {
				t.Fatalf("value=%q cap=%d got=%q want=%q", value, cap, got, want)
			}
		}
	}
}
