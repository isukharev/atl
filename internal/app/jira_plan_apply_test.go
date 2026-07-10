package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type planTracker struct {
	domain.Tracker
	issues        map[string]domain.Issue
	linked        []string
	labelsKey     string
	labelsAdd     []string
	labelsRemove  []string
	updatedKey    string
	updatedFields map[string]string
	commentKey    string
	commentBody   string
	commentsErr   error
	commentCalls  int
	linkTypes     []string
	linkTypeCalls int
}

func (t *planTracker) GetIssue(_ context.Context, key string, _ []string) (*domain.Issue, error) {
	issue, ok := t.issues[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &issue, nil
}

func (t *planTracker) Link(_ context.Context, from, to, linkType string) error {
	t.linked = append(t.linked, from+">"+to+":"+linkType)
	return nil
}

func (t *planTracker) UpdateLabels(_ context.Context, key string, add, remove []string) error {
	t.labelsKey, t.labelsAdd, t.labelsRemove = key, add, remove
	return nil
}

func (t *planTracker) Update(_ context.Context, key, _ string, _ []byte, fields map[string]string) error {
	t.updatedKey, t.updatedFields = key, fields
	return nil
}

func (t *planTracker) AddComment(_ context.Context, key string, body []byte) (*domain.Comment, error) {
	t.commentCalls++
	t.commentKey, t.commentBody = key, string(body)
	return &domain.Comment{ID: "1", Body: string(body)}, nil
}

func (t *planTracker) ListComments(context.Context, string) ([]domain.Comment, error) {
	return nil, t.commentsErr
}

func (t *planTracker) LinkTypes(context.Context) ([]string, error) {
	t.linkTypeCalls++
	return t.linkTypes, nil
}

func TestApplyPlanDryRunIsIdempotentAndBlockedByAllowlists(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"version,op,source,target,type,field,value,rationale,expected_updated",
		"1,link,PROJ-1,PROJ-2,Blocks,,,exists,2026-01-01",
		"1,link,PROJ-4,PROJ-3,Blocks,,,missing,2026-01-01",
		"1,field,PROJ-5,,,priority,High,not allowed,2026-01-01",
	}, "\n")
	if err := os.WriteFile(csvPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	tr := &planTracker{linkTypes: []string{"Blocks"}, issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}, Links: []domain.IssueLink{{Direction: "outward", Type: "Blocks", Key: "PROJ-2"}}},
		"PROJ-2": {Key: "PROJ-2", Fields: map[string]any{"updated": "2026-01-01"}},
		"PROJ-3": {Key: "PROJ-3", Fields: map[string]any{"updated": "2026-01-01"}},
		"PROJ-4": {Key: "PROJ-4", Fields: map[string]any{"updated": "2026-01-01"}},
	}}
	svc := &JiraService{tr: tr}

	res, err := svc.ApplyPlan(context.Background(), JiraPlanApplyOpts{CSVPath: csvPath, ContinueOnError: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("ApplyPlan dry-run error = %v, want ErrCheckFailed", err)
	}
	if res.Mode != "dry-run" || res.Count != 3 {
		t.Fatalf("result header = %+v, want dry-run count 3", res)
	}
	statuses := []string{res.Results[0].Status, res.Results[1].Status, res.Results[2].Status}
	if strings.Join(statuses, ",") != "already_satisfied,would_apply,blocked" {
		t.Fatalf("statuses = %v, want already_satisfied,would_apply,blocked", statuses)
	}
	if len(tr.linked) != 0 {
		t.Fatalf("dry-run linked = %+v, want no writes", tr.linked)
	}
}

func TestApplyPlanRequiresConfirmAndAppliesAllowedOps(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"version,op,source,target,type,field,value,expected_updated",
		"1,link,PROJ-1,PROJ-3,Blocks,,,2026-01-01",
		"1,label_add,PROJ-2,,, ,triaged,2026-01-01",
		"1,field,PROJ-4,,,priority,High,2026-01-01",
	}, "\n")
	if err := os.WriteFile(csvPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	tr := &planTracker{linkTypes: []string{"Blocks"}, issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Labels: []string{"backend"}, Fields: map[string]any{"priority": "Low", "updated": "2026-01-01"}},
		"PROJ-2": {Key: "PROJ-2", Labels: []string{"backend"}, Fields: map[string]any{"updated": "2026-01-01"}},
		"PROJ-3": {Key: "PROJ-3", Fields: map[string]any{"updated": "2026-01-01"}},
		"PROJ-4": {Key: "PROJ-4", Fields: map[string]any{"priority": "Low", "updated": "2026-01-01"}},
	}}
	svc := &JiraService{tr: tr}

	if _, err := svc.ApplyPlan(context.Background(), JiraPlanApplyOpts{CSVPath: csvPath, Apply: true}); err == nil {
		t.Fatal("ApplyPlan --apply without confirm: want error, got nil")
	}
	res, err := svc.ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath:     csvPath,
		Apply:       true,
		Confirm:     planApplyConfirm,
		AllowOps:    []string{"link,label_add,field"},
		AllowFields: []string{"priority"},
	})
	if err != nil {
		t.Fatalf("ApplyPlan apply: %v", err)
	}
	if strings.Join([]string{res.Results[0].Status, res.Results[1].Status, res.Results[2].Status}, ",") != "applied,applied,applied" {
		t.Fatalf("results = %+v, want all applied", res.Results)
	}
	if strings.Join(tr.linked, ",") != "PROJ-1>PROJ-3:Blocks" || tr.labelsKey != "PROJ-2" || strings.Join(tr.labelsAdd, ",") != "triaged" || tr.updatedKey != "PROJ-4" || tr.updatedFields["priority"] != "High" {
		t.Fatalf("writes not applied as expected: linked=%v labels=%s/%v fields=%v", tr.linked, tr.labelsKey, tr.labelsAdd, tr.updatedFields)
	}
}

// Regression: the adapter reports an existing link's Type as the directional
// phrase ("duplicates"), while plan rows carry the canonical type name
// ("Duplicate"). The identity check must match on TypeName, or a re-apply of a
// satisfied plan would POST a duplicate link.
func TestApplyPlanLinkIdempotentWhenPhraseDiffersFromName(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"version,op,source,target,type,expected_updated",
		"1,link,PROJ-1,PROJ-2,Duplicate,2026-01-01",
	}, "\n")
	if err := os.WriteFile(csvPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	tr := &planTracker{linkTypes: []string{"Duplicate"}, issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}, Links: []domain.IssueLink{
			{Direction: "outward", Type: "duplicates", TypeName: "Duplicate", Key: "PROJ-2"},
		}},
		"PROJ-2": {Key: "PROJ-2", Fields: map[string]any{"updated": "2026-01-01"}},
	}}
	svc := &JiraService{tr: tr}

	res, err := svc.ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: csvPath, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"link"},
	})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if res.Results[0].Status != "already_satisfied" {
		t.Fatalf("status = %q, want already_satisfied", res.Results[0].Status)
	}
	if len(tr.linked) != 0 {
		t.Fatalf("re-apply created a duplicate link: %v", tr.linked)
	}
}

func TestApplyPlanCommentNeverWritesAfterTruncatedPreflight(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "plan.csv")
	if err := os.WriteFile(csvPath, []byte("version,op,source,value,expected_updated\n1,comment,PROJ-1,hello,2026-01-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{commentsErr: fmt.Errorf("%w: incomplete comments", domain.ErrCheckFailed)}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: csvPath, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"comment"},
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("plan error = %v, want ErrCheckFailed", err)
	}
	if len(res.Results) != 1 || res.Results[0].Status != "failed" {
		t.Fatalf("result = %+v, want failed row", res)
	}
	if tr.commentCalls != 0 {
		t.Fatalf("AddComment called %d times after incomplete preflight", tr.commentCalls)
	}
}

func TestApplyPlanRejectsMissingAndUnsupportedSchemaVersion(t *testing.T) {
	for _, data := range []string{
		"op,source,expected_updated\ncomment,PROJ-1,2026-01-01\n",
		"version,op,source,value,expected_updated\n2,comment,PROJ-1,hello,2026-01-01\n",
	} {
		path := filepath.Join(t.TempDir(), "plan.csv")
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if res, err := (&JiraService{tr: &planTracker{}}).ApplyPlan(context.Background(), JiraPlanApplyOpts{CSVPath: path}); err == nil || res != nil {
			t.Fatalf("result=%+v error=%v, want schema usage error before result", res, err)
		}
	}
}

func TestApplyPlanStaleFieldFailsWithoutWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := "version,op,source,field,value,expected_updated\n1,field,PROJ-1,priority,High,old\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"priority": "Low", "updated": "new"}},
	}}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"field"}, AllowFields: []string{"priority"},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || res.Results[0].Status != "blocked" {
		t.Fatalf("result=%+v error=%v, want stale blocked", res, err)
	}
	if tr.updatedKey != "" {
		t.Fatalf("stale row wrote issue %s", tr.updatedKey)
	}
}

func TestApplyPlanValidatesLinkTypeMetadataAndExplicitException(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := "version,op,source,target,type,expected_updated\n1,link,PROJ-1,PROJ-2,Custom Link,2026-01-01\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	newTracker := func() *planTracker {
		return &planTracker{linkTypes: []string{"Blocks"}, issues: map[string]domain.Issue{
			"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}},
			"PROJ-2": {Key: "PROJ-2", Fields: map[string]any{"updated": "2026-01-01"}},
		}}
	}
	tr := newTracker()
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"link"},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || res.Results[0].Status != "blocked" || len(tr.linked) != 0 {
		t.Fatalf("metadata rejection result=%+v error=%v writes=%v", res, err, tr.linked)
	}
	tr = newTracker()
	res, err = (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"link"}, AllowLinkTypes: []string{"Custom Link"},
	})
	if err != nil || res.Results[0].Status != "applied" || len(tr.linked) != 1 {
		t.Fatalf("explicit exception result=%+v error=%v writes=%v", res, err, tr.linked)
	}
}

func TestApplyPlanFailFastAndContinueOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"version,op,source,value,expected_updated",
		"1,comment,PROJ-1,hello,2026-01-01",
		"1,label_add,PROJ-2,triaged,2026-01-01",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	newTracker := func() *planTracker {
		return &planTracker{commentsErr: domain.ErrForbidden, issues: map[string]domain.Issue{
			"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}},
			"PROJ-2": {Key: "PROJ-2", Fields: map[string]any{"updated": "2026-01-01"}},
		}}
	}
	tr := newTracker()
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"comment", "label_add"},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || res.Results[0].Status != "failed" || res.Results[1].Status != "skipped" || tr.labelsKey != "" {
		t.Fatalf("fail-fast result=%+v error=%v labels=%s", res, err, tr.labelsKey)
	}
	tr = newTracker()
	res, err = (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"comment", "label_add"}, ContinueOnError: true,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || res.Results[0].Status != "failed" || res.Results[1].Status != "applied" || tr.labelsKey != "PROJ-2" {
		t.Fatalf("continue result=%+v error=%v labels=%s", res, err, tr.labelsKey)
	}
}

func TestApplyPlanStructuredFieldComparisonIsCanonical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := "version,op,source,field,value,expected_updated\n1,field,PROJ-1,customfield_1,\"{\"\"name\"\":\"\"A\"\",\"\"id\"\":\"\"1\"\"}\",2026-01-01\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"customfield_1": map[string]any{"id": "1", "name": "A", "self": "server-added"}, "updated": "2026-01-01"}},
	}}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"field"}, AllowFields: []string{"customfield_1"},
	})
	if err != nil || res.Results[0].Status != "already_satisfied" || tr.updatedKey != "" {
		t.Fatalf("structured comparison result=%+v error=%v update=%s", res, err, tr.updatedKey)
	}
}

func TestApplyPlanRejectsMultipleRowsForOneSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"version,op,source,value,expected_updated",
		"1,label_add,PROJ-1,one,2026-01-01",
		"1,label_add,proj-1,two,2026-01-01",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}},
	}}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"label_add"}, ContinueOnError: true,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || res.Results[0].Status != "blocked" || res.Results[1].Status != "blocked" {
		t.Fatalf("result=%+v error=%v, want both dependent rows blocked", res, err)
	}
	if tr.labelsKey != "" {
		t.Fatalf("duplicate-source plan wrote labels to %s", tr.labelsKey)
	}
}

func TestApplyPlanDuplicateSourcePolicyNeedsNoMetadataCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"version,op,source,target,type,expected_updated",
		"1,link,PROJ-1,PROJ-2,Blocks,2026-01-01",
		"1,link,proj-1,PROJ-3,Blocks,2026-01-01",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{CSVPath: path})
	if !errors.Is(err, domain.ErrCheckFailed) || res == nil || tr.linkTypeCalls != 0 {
		t.Fatalf("result=%+v error=%v metadata calls=%d", res, err, tr.linkTypeCalls)
	}
}

func TestApplyPlanLinkPreviewFailsWhenTargetIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := "version,op,source,target,type,expected_updated\n1,link,PROJ-1,MISSING-2,Blocks,2026-01-01\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{linkTypes: []string{"Blocks"}, issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}},
	}}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{CSVPath: path})
	if !errors.Is(err, domain.ErrCheckFailed) || res.Results[0].Status != "failed" || len(tr.linked) != 0 {
		t.Fatalf("result=%+v error=%v writes=%v", res, err, tr.linked)
	}
	if strings.Contains(res.Results[0].Message, "http") {
		t.Fatalf("audit message exposed transport detail: %q", res.Results[0].Message)
	}
}

func TestApplyPlanFieldTextStartingWithBracketRemainsText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := "version,op,source,field,value,expected_updated\n1,field,PROJ-1,summary,[Draft],2026-01-01\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"summary": "Old", "updated": "2026-01-01"}},
	}}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"field"}, AllowFields: []string{"summary"},
	})
	if err != nil || res.Results[0].Status != "applied" || tr.updatedFields["summary"] != "[Draft]" {
		t.Fatalf("result=%+v error=%v fields=%v", res, err, tr.updatedFields)
	}
}

func TestApplyPlanPolicyValidationPrecedesEveryWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"version,op,source,target,type,field,value,expected_updated",
		"1,link,PROJ-1,PROJ-2,Blocks,,,2026-01-01",
		"1,field,PROJ-3,,,priority,High,2026-01-01",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{linkTypes: []string{"Blocks"}, issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}},
		"PROJ-2": {Key: "PROJ-2", Fields: map[string]any{"updated": "2026-01-01"}},
	}}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"link", "field"},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || res.Results[0].Status != "skipped" || res.Results[1].Status != "blocked" {
		t.Fatalf("result=%+v error=%v, want static fail-before-write", res, err)
	}
	if len(tr.linked) != 0 || tr.updatedKey != "" {
		t.Fatalf("policy-invalid plan wrote links=%v update=%s", tr.linked, tr.updatedKey)
	}
}

func TestApplyPlanStructuredFieldUpdatePreservesReviewedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	const desired = `{"id":"2","name":"B"}`
	data := "version,op,source,field,value,expected_updated\n1,field,PROJ-1,customfield_1,\"{\"\"id\"\":\"\"2\"\",\"\"name\"\":\"\"B\"\"}\",2026-01-01\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"customfield_1": map[string]any{"id": "1", "name": "A"}, "updated": "2026-01-01"}},
	}}
	res, err := (&JiraService{tr: tr}).ApplyPlan(context.Background(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"field"}, AllowFields: []string{"customfield_1"},
	})
	if err != nil || res.Results[0].Status != "applied" || tr.updatedFields["customfield_1"] != desired {
		t.Fatalf("result=%+v error=%v fields=%v", res, err, tr.updatedFields)
	}
}

func TestPlanFieldSubsetDistinguishesMissingFromExplicitNull(t *testing.T) {
	if planFieldEqual(map[string]any{}, `{"value":null}`) {
		t.Fatal("missing structured key matched an explicitly reviewed null")
	}
	if !planFieldEqual(map[string]any{"value": nil, "self": "server-added"}, `{"value":null}`) {
		t.Fatal("explicit null did not match enriched current object")
	}
}

func TestPlanFieldSubsetDoesNotTreatEmptyObjectAsWildcard(t *testing.T) {
	if planFieldEqual(map[string]any{"id": "1"}, `{}`) {
		t.Fatal("empty reviewed object matched a non-empty current object")
	}
	if planFieldEqual(map[string]any{"nested": map[string]any{"id": "1"}}, `{"nested":{}}`) {
		t.Fatal("nested empty reviewed object matched a non-empty current object")
	}
	if !planFieldEqual(map[string]any{}, `{}`) {
		t.Fatal("empty reviewed object did not match an empty current object")
	}
}
