package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraFieldBatchTracker struct {
	domain.Tracker
	catalog       domain.FieldCatalogSnapshot
	pages         map[string]domain.IssueSearchPage
	err           error
	catalogCalls  int
	searchCalls   int
	responseBytes int64
	jql           string
	fields        []string
	cursors       []string
}

type cancelingJiraFieldBatchValue struct{ cancel context.CancelFunc }

func (v cancelingJiraFieldBatchValue) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`"late projection"`), nil
}

func (t *jiraFieldBatchTracker) ReadFieldCatalog(ctx context.Context) (domain.FieldCatalogSnapshot, error) {
	t.catalogCalls++
	if err := chargeJiraFieldBatchTestRead(ctx, t.responseBytes); err != nil {
		return domain.FieldCatalogSnapshot{}, err
	}
	if t.err != nil {
		return domain.FieldCatalogSnapshot{}, t.err
	}
	return t.catalog, nil
}

func (t *jiraFieldBatchTracker) SearchQualified(ctx context.Context, jql string, fields []string, limit int, cursor string) (domain.IssueSearchPage, error) {
	t.searchCalls++
	t.jql = jql
	t.fields = append([]string(nil), fields...)
	t.cursors = append(t.cursors, cursor)
	if limit != JiraFieldBatchMaxKeys {
		return domain.IssueSearchPage{}, fmt.Errorf("unexpected limit %d", limit)
	}
	if err := chargeJiraFieldBatchTestRead(ctx, t.responseBytes); err != nil {
		return domain.IssueSearchPage{}, err
	}
	if t.err != nil {
		return domain.IssueSearchPage{}, t.err
	}
	return t.pages[cursor], nil
}

func chargeJiraFieldBatchTestRead(ctx context.Context, responseBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	budget := domain.ReadBudgetFromContext(ctx)
	if err := budget.TakeAttempt(); err != nil {
		return err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	if responseBytes > remaining {
		finish(remaining)
		return domain.ErrReadResponseBudgetExhausted
	}
	finish(responseBytes)
	return nil
}

func fieldBatchCatalog(defs ...domain.FieldDef) domain.FieldCatalogSnapshot {
	return domain.FieldCatalogSnapshot{Fields: defs, Complete: true}
}

func TestIssueFieldBatchProjectsOrderedClosedStatesAndMissingRows(t *testing.T) {
	tracker := &jiraFieldBatchTracker{
		catalog: fieldBatchCatalog(
			domain.FieldDef{ID: "customfield_1", Name: "Notes", Custom: true, Schema: "string"},
			domain.FieldDef{ID: "customfield_2", Name: "Nullable", Custom: true, Schema: "string"},
			domain.FieldDef{ID: "customfield_3", Name: "Empty list", Custom: true, Schema: "array"},
			domain.FieldDef{ID: "customfield_4", Name: "Not returned", Custom: true, Schema: "string"},
		),
		pages: map[string]domain.IssueSearchPage{
			"": {
				Issues: []domain.Issue{{ID: "10002", Key: "PROJ-2", Fields: map[string]any{
					"updated": "2026-08-20T12:00:00Z", "customfield_1": "evidence", "customfield_2": nil, "customfield_3": []any{},
				}}},
				Next: "1", Total: 1, TotalKnown: true,
			},
			"1": {Complete: true, Total: 1, TotalKnown: true},
		},
		responseBytes: 7,
	}
	result, err := (&JiraService{tr: tracker}).IssueFieldBatch(t.Context(), JiraFieldBatchOpts{
		Keys:      []string{"PROJ-1", "PROJ-2"},
		Selectors: []string{"Notes", "customfield_2", "Empty list", "Not returned"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || !result.Reconciled || result.Operation != "jira_issue_field_batch" ||
		result.Projection != "compact" || result.Selection.KeyCount != 2 || result.Selection.FieldCount != 4 ||
		result.Usage != (JiraFieldBatchUsage{Requests: 3, ResponseBytes: 21, SearchPages: 2, FoundCount: 1, MissingCount: 1}) {
		t.Fatalf("result header=%+v usage=%+v", result, result.Usage)
	}
	if got := []string{result.Fields[0].ID, result.Fields[1].ID, result.Fields[2].ID, result.Fields[3].ID}; strings.Join(got, ",") != "customfield_1,customfield_2,customfield_3,customfield_4" {
		t.Fatalf("field order=%v", got)
	}
	missing, found := result.Issues[0], result.Issues[1]
	if missing.RequestedKey != "PROJ-1" || missing.Found || missing.Reason != "missing_or_inaccessible" ||
		missing.ID != "" || missing.Key != "" || missing.Updated != "" || missing.Cells != nil {
		t.Fatalf("missing row=%+v", missing)
	}
	if found.RequestedKey != "PROJ-2" || !found.Found || found.ID != "10002" || found.Key != "PROJ-2" ||
		len(found.Cells) != 4 || found.Cells[0].State != "value" || found.Cells[1].State != "null" ||
		found.Cells[2].State != "empty" || found.Cells[3].State != "absent" {
		t.Fatalf("found row=%+v", found)
	}
	if found.Cells[1].Value == nil || *found.Cells[1].Value != nil || found.Cells[3].Value != nil ||
		found.Cells[3].OriginalValueBytes != 0 || found.Cells[3].EmittedValueBytes != 0 {
		t.Fatalf("null=%+v absent=%+v", found.Cells[1], found.Cells[3])
	}
	if tracker.jql != `key in ("PROJ-1","PROJ-2") ORDER BY key ASC` || strings.Join(tracker.fields, ",") != "customfield_1,customfield_2,customfield_3,customfield_4,updated" ||
		strings.Join(tracker.cursors, ",") != ",1" {
		t.Fatalf("jql=%q fields=%v cursors=%v", tracker.jql, tracker.fields, tracker.cursors)
	}
	if len(result.EncodedJSON()) == 0 || len(result.EncodedJSON()) > JiraFieldBatchMaxOutputBytes {
		t.Fatalf("encoded bytes=%d", len(result.EncodedJSON()))
	}
}

func TestIssueFieldBatchRejectsLocalBoundsBeforeIO(t *testing.T) {
	validKeys := make([]string, JiraFieldBatchMaxKeys+1)
	for index := range validKeys {
		validKeys[index] = fmt.Sprintf("PROJ-%d", index+1)
	}
	validFields := make([]string, JiraFieldBatchMaxFields+1)
	for index := range validFields {
		validFields[index] = fmt.Sprintf("customfield_%d", index+1)
	}
	tests := []JiraFieldBatchOpts{
		{Selectors: []string{"summary"}},
		{Keys: validKeys, Selectors: []string{"summary"}},
		{Keys: []string{"proj-1"}, Selectors: []string{"summary"}},
		{Keys: []string{" PROJ-1"}, Selectors: []string{"summary"}},
		{Keys: []string{"PROJ-1" + strings.Repeat(" ", JiraFieldBatchMaxKeyBytes)}, Selectors: []string{"summary"}},
		{Keys: []string{"PROJ-1", "PROJ-1"}, Selectors: []string{"summary"}},
		{Keys: []string{"PROJ-1"}},
		{Keys: []string{"PROJ-1"}, Selectors: validFields},
		{Keys: []string{"PROJ-1"}, Selectors: []string{"summary", "summary"}},
		{Keys: []string{"PROJ-1"}, Selectors: []string{" summary"}},
		{Keys: []string{"PROJ-1"}, Selectors: []string{"summary" + strings.Repeat(" ", JiraFieldBatchMaxSelectorBytes)}},
		{Keys: []string{"PROJ-1"}, Selectors: []string{strings.Repeat("x", JiraFieldBatchMaxSelectorBytes+1)}},
	}
	for index, opts := range tests {
		tracker := &jiraFieldBatchTracker{}
		_, err := (&JiraService{tr: tracker}).IssueFieldBatch(t.Context(), opts)
		if !errors.Is(err, domain.ErrUsage) || tracker.catalogCalls != 0 || tracker.searchCalls != 0 {
			t.Fatalf("case %d err=%v catalog=%d search=%d", index, err, tracker.catalogCalls, tracker.searchCalls)
		}
	}
}

func TestIssueFieldBatchAcceptsMaximumSelectionAcrossMaximumPages(t *testing.T) {
	keys := make([]string, JiraFieldBatchMaxKeys)
	defs := make([]domain.FieldDef, JiraFieldBatchMaxFields)
	pages := make(map[string]domain.IssueSearchPage, JiraFieldBatchMaxSearchPages)
	for index := range defs {
		defs[index] = domain.FieldDef{ID: fmt.Sprintf("customfield_%d", index+1), Name: fmt.Sprintf("Field %d", index+1), Custom: true}
	}
	for index := range keys {
		keys[index] = fmt.Sprintf("PROJ-%d", index+1)
		cursor := ""
		if index > 0 {
			cursor = strconv.Itoa(index)
		}
		page := domain.IssueSearchPage{
			Issues: []domain.Issue{{
				ID: strconv.Itoa(10001 + index), Key: keys[index],
				Fields: map[string]any{"updated": "2026-08-20T12:00:00Z"},
			}},
			Total: JiraFieldBatchMaxKeys, TotalKnown: true,
		}
		if index+1 == JiraFieldBatchMaxSearchPages {
			page.Complete = true
		} else {
			page.Next = strconv.Itoa(index + 1)
		}
		pages[cursor] = page
	}
	selectors := make([]string, len(defs))
	for index := range defs {
		selectors[index] = defs[index].ID
	}
	tracker := &jiraFieldBatchTracker{catalog: fieldBatchCatalog(defs...), pages: pages}
	result, err := (&JiraService{tr: tracker}).IssueFieldBatch(t.Context(), JiraFieldBatchOpts{Keys: keys, Selectors: selectors})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || !result.Reconciled || result.Selection != (JiraFieldBatchSelection{KeyCount: 25, FieldCount: 8}) ||
		result.Usage.Requests != 26 || result.Usage.SearchPages != 25 || result.Usage.FoundCount != 25 || tracker.searchCalls != 25 {
		t.Fatalf("result=%+v usage=%+v search=%d", result, result.Usage, tracker.searchCalls)
	}
}

func TestIssueFieldBatchRejectsCatalogAmbiguityAndSelectorConvergence(t *testing.T) {
	tests := []struct {
		name      string
		catalog   domain.FieldCatalogSnapshot
		selectors []string
	}{
		{name: "partial", catalog: domain.FieldCatalogSnapshot{PartialReason: "static"}, selectors: []string{"summary"}},
		{name: "duplicate folded id", catalog: fieldBatchCatalog(domain.FieldDef{ID: "Summary", Name: "One"}, domain.FieldDef{ID: "summary", Name: "Two"}), selectors: []string{"summary"}},
		{name: "oversized member", catalog: fieldBatchCatalog(domain.FieldDef{ID: "summary", Name: strings.Repeat("n", JiraFieldBatchMaxCatalogMemberBytes+1)}), selectors: []string{"summary"}},
		{name: "ambiguous name", catalog: fieldBatchCatalog(domain.FieldDef{ID: "customfield_1", Name: "Risk"}, domain.FieldDef{ID: "customfield_2", Name: "Risk"}), selectors: []string{"Risk"}},
		{name: "converged", catalog: fieldBatchCatalog(domain.FieldDef{ID: "customfield_1", Name: "Risk"}), selectors: []string{"Risk", "customfield_1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := &jiraFieldBatchTracker{catalog: test.catalog}
			_, err := (&JiraService{tr: tracker}).IssueFieldBatch(t.Context(), JiraFieldBatchOpts{Keys: []string{"PROJ-1"}, Selectors: test.selectors})
			if err == nil || tracker.catalogCalls != 1 || tracker.searchCalls != 0 || err.Error() != "jira field batch read failed" {
				t.Fatalf("err=%v catalog=%d search=%d", err, tracker.catalogCalls, tracker.searchCalls)
			}
		})
	}
}

func TestIssueFieldBatchRejectsSearchDriftAndMalformedIdentity(t *testing.T) {
	validIssue := domain.Issue{ID: "10001", Key: "PROJ-1", Fields: map[string]any{"summary": "ok", "updated": "2026-08-20T12:00:00Z"}}
	tests := []struct {
		name  string
		pages map[string]domain.IssueSearchPage
	}{
		{name: "total drift", pages: map[string]domain.IssueSearchPage{
			"":  {Issues: []domain.Issue{validIssue}, Next: "1", Total: 2, TotalKnown: true},
			"1": {Complete: true, Total: 1, TotalKnown: true},
		}},
		{name: "cursor stalled", pages: map[string]domain.IssueSearchPage{
			"": {Next: "0", Total: 1, TotalKnown: true},
		}},
		{name: "cursor jump", pages: map[string]domain.IssueSearchPage{
			"": {Issues: []domain.Issue{validIssue}, Next: "2", Total: 2, TotalKnown: true},
		}},
		{name: "unrequested key", pages: map[string]domain.IssueSearchPage{
			"": {Issues: []domain.Issue{{ID: "10002", Key: "OTHER-2", Fields: map[string]any{"updated": "2026-08-20T12:00:00Z"}}}, Complete: true, Total: 1, TotalKnown: true},
		}},
		{name: "non-numeric id", pages: map[string]domain.IssueSearchPage{
			"": {Issues: []domain.Issue{{ID: "opaque", Key: "PROJ-1", Fields: map[string]any{"updated": "2026-08-20T12:00:00Z"}}}, Complete: true, Total: 1, TotalKnown: true},
		}},
		{name: "duplicate numeric id", pages: map[string]domain.IssueSearchPage{
			"": {Issues: []domain.Issue{
				{ID: "10001", Key: "PROJ-1", Fields: map[string]any{"updated": "2026-08-20T12:00:00Z"}},
				{ID: "10001", Key: "PROJ-2", Fields: map[string]any{"updated": "2026-08-20T12:00:00Z"}},
			}, Complete: true, Total: 2, TotalKnown: true},
		}},
		{name: "missing provenance", pages: map[string]domain.IssueSearchPage{
			"": {Issues: []domain.Issue{{ID: "10001", Key: "PROJ-1", Fields: map[string]any{"summary": "ok"}}}, Complete: true, Total: 1, TotalKnown: true},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := &jiraFieldBatchTracker{catalog: fieldBatchCatalog(domain.FieldDef{ID: "summary", Name: "Summary"}), pages: test.pages}
			_, err := (&JiraService{tr: tracker}).IssueFieldBatch(t.Context(), JiraFieldBatchOpts{Keys: []string{"PROJ-1", "PROJ-2"}, Selectors: []string{"summary"}})
			if !errors.Is(err, domain.ErrCheckFailed) || err.Error() != "jira field batch read failed" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestIssueFieldBatchComposesParentAttemptByteAndDeadlineLimits(t *testing.T) {
	newTracker := func() *jiraFieldBatchTracker {
		return &jiraFieldBatchTracker{
			catalog:       fieldBatchCatalog(domain.FieldDef{ID: "summary", Name: "Summary"}),
			pages:         map[string]domain.IssueSearchPage{"": {Complete: true, TotalKnown: true}},
			responseBytes: 2,
		}
	}
	parent, _ := domain.NewReadBudget(1, 100)
	tracker := newTracker()
	_, err := (&JiraService{tr: tracker}).IssueFieldBatch(domain.WithReadBudget(t.Context(), parent), JiraFieldBatchOpts{Keys: []string{"PROJ-1"}, Selectors: []string{"summary"}})
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) || parent.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 2}) {
		t.Fatalf("attempt err=%v usage=%+v", err, parent.Usage())
	}

	parent, _ = domain.NewReadBudget(10, 3)
	tracker = newTracker()
	_, err = (&JiraService{tr: tracker}).IssueFieldBatch(domain.WithReadBudget(t.Context(), parent), JiraFieldBatchOpts{Keys: []string{"PROJ-1"}, Selectors: []string{"summary"}})
	if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || parent.Usage() != (domain.ReadBudgetUsage{Attempts: 2, ResponseBytes: 3}) {
		t.Fatalf("byte err=%v usage=%+v", err, parent.Usage())
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	tracker = newTracker()
	_, err = (&JiraService{tr: tracker}).IssueFieldBatch(canceled, JiraFieldBatchOpts{Keys: []string{"PROJ-1"}, Selectors: []string{"summary"}})
	if !errors.Is(err, context.Canceled) || err.Error() != "jira field batch read failed" {
		t.Fatalf("deadline err=%v", err)
	}
}

func TestIssueFieldBatchDeadlineCoversLateProjection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	tracker := &jiraFieldBatchTracker{
		catalog: fieldBatchCatalog(domain.FieldDef{ID: "summary", Name: "Summary"}),
		pages: map[string]domain.IssueSearchPage{"": {
			Issues: []domain.Issue{{ID: "10001", Key: "PROJ-1", Fields: map[string]any{
				"updated": "2026-08-20T12:00:00Z", "summary": cancelingJiraFieldBatchValue{cancel: cancel},
			}}},
			Complete: true, Total: 1, TotalKnown: true,
		}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFieldBatch(ctx, JiraFieldBatchOpts{Keys: []string{"PROJ-1"}, Selectors: []string{"summary"}})
	if result != nil || !errors.Is(err, context.Canceled) || err.Error() != "jira field batch read failed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestIssueFieldBatchClippingQualifiesCellAndTopLevelCompleteness(t *testing.T) {
	raw := strings.Repeat("x", JiraFieldBatchMaxCellBytes+100)
	tracker := &jiraFieldBatchTracker{
		catalog: fieldBatchCatalog(domain.FieldDef{ID: "summary", Name: "Summary"}),
		pages: map[string]domain.IssueSearchPage{"": {
			Issues: []domain.Issue{{ID: "10001", Key: "PROJ-1", Fields: map[string]any{
				"updated": "2026-08-20T12:00:00Z", "summary": raw,
			}}},
			Complete: true, Total: 1, TotalKnown: true,
		}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFieldBatch(t.Context(), JiraFieldBatchOpts{Keys: []string{"PROJ-1"}, Selectors: []string{"summary"}})
	if err != nil {
		t.Fatal(err)
	}
	cell := result.Issues[0].Cells[0]
	emitted, ok := (*cell.Value).(string)
	if result.Complete || !result.Reconciled || cell.State != "value" || cell.Complete || !cell.Truncated ||
		cell.OriginalValueBytes != len(raw)+2 || cell.EmittedValueBytes != JiraFieldBatchMaxCellBytes || !ok || len(emitted) != JiraFieldBatchMaxCellBytes-2 {
		t.Fatalf("complete=%t reconciled=%t cell=%+v emitted=%d/%t", result.Complete, result.Reconciled, cell, len(emitted), ok)
	}
}

func TestEncodeJiraFieldBatchResultRejectsWholeOutputBeforePublication(t *testing.T) {
	result := &JiraFieldBatchResult{Issues: []JiraFieldBatchIssue{{RequestedKey: strings.Repeat("x", 256)}}}
	if _, err := encodeJiraFieldBatchResult(result, 32); !errors.Is(err, domain.ErrOutputLimit) {
		t.Fatalf("err=%v", err)
	}
	if result.encodedJSON != nil {
		t.Fatalf("overflow published %d bytes", len(result.encodedJSON))
	}
}

func TestJiraFieldBatchErrorPreservesClassWithoutExposingCause(t *testing.T) {
	private := &privateCreateMetadataCause{message: "private backend response canary"}
	err := contentFreeJiraFieldBatchError(fmt.Errorf("read failed: %w: %w", domain.ErrCheckFailed, private))
	if !errors.Is(err, domain.ErrCheckFailed) || err.Error() != "jira field batch read failed" {
		t.Fatalf("error=%v", err)
	}
	var exposed *privateCreateMetadataCause
	if errors.Unwrap(err) != nil || errors.As(err, &exposed) {
		t.Fatalf("private cause escaped the content-free boundary: %#v", err)
	}
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%q", err)} {
		if strings.Contains(rendered, private.message) {
			t.Fatalf("formatted error leaked private cause: %s", rendered)
		}
	}
}
