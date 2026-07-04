package app

import (
	"context"
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
	t.commentKey, t.commentBody = key, string(body)
	return &domain.Comment{ID: "1", Body: string(body)}, nil
}

func TestApplyPlanDryRunIsIdempotentAndBlockedByAllowlists(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "plan.csv")
	data := strings.Join([]string{
		"op,source,target,type,field,value,rationale",
		"link,PROJ-1,PROJ-2,Blocks,,,exists",
		"link,PROJ-1,PROJ-3,Blocks,,,missing",
		"field,PROJ-1,,,priority,High,not allowed",
	}, "\n")
	if err := os.WriteFile(csvPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	tr := &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Links: []domain.IssueLink{{Direction: "outward", Type: "Blocks", Key: "PROJ-2"}}},
	}}
	svc := &JiraService{tr: tr}

	res, err := svc.ApplyPlan(context.Background(), JiraPlanApplyOpts{CSVPath: csvPath})
	if err != nil {
		t.Fatalf("ApplyPlan dry-run: %v", err)
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
		"op,source,target,type,field,value",
		"link,PROJ-1,PROJ-3,Blocks,,",
		"label_add,PROJ-1,,, ,triaged",
		"field,PROJ-1,,,priority,High",
	}, "\n")
	if err := os.WriteFile(csvPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	tr := &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Labels: []string{"backend"}, Fields: map[string]any{"priority": "Low"}},
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
	if strings.Join(tr.linked, ",") != "PROJ-1>PROJ-3:Blocks" || tr.labelsKey != "PROJ-1" || strings.Join(tr.labelsAdd, ",") != "triaged" || tr.updatedFields["priority"] != "High" {
		t.Fatalf("writes not applied as expected: linked=%v labels=%s/%v fields=%v", tr.linked, tr.labelsKey, tr.labelsAdd, tr.updatedFields)
	}
}
