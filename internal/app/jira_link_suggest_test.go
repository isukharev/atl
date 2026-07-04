package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestSuggestLinksReturnsOnlyMissingOutwardLinks(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "links.csv")
	data := strings.Join([]string{
		"from,to,link_type,reason",
		"PROJ-1,PROJ-2,Blocks,already exists",
		"PROJ-1,PROJ-3,Blocks,missing candidate",
		"PROJ-1,PROJ-3,Blocks,duplicate row",
		"PROJ-2,PROJ-1,Relates,other source",
	}, "\n")
	if err := os.WriteFile(csvPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	svc := &JiraService{tr: partialTracker{issues: []domain.Issue{
		{Key: "PROJ-1", Links: []domain.IssueLink{{Direction: "outward", Type: "Blocks", Key: "PROJ-2"}}},
		{Key: "PROJ-2"},
	}}}

	res, err := svc.SuggestLinks(context.Background(), JiraLinkSuggestOpts{CSVPath: csvPath})
	if err != nil {
		t.Fatalf("SuggestLinks: %v", err)
	}
	if res.PlannedCount != 3 || res.Count != 2 {
		t.Fatalf("result counts = planned %d count %d, want planned 3 count 2", res.PlannedCount, res.Count)
	}
	got := res.Candidates[0].Source + ">" + res.Candidates[0].Target + "," + res.Candidates[1].Source + ">" + res.Candidates[1].Target
	if got != "PROJ-1>PROJ-3,PROJ-2>PROJ-1" {
		t.Fatalf("candidates = %+v, want missing deterministic candidates", res.Candidates)
	}
	if res.Candidates[0].Rationale != "missing candidate" || res.Candidates[0].Row != 3 {
		t.Fatalf("first candidate = %+v, want CSV rationale and row", res.Candidates[0])
	}
}

func TestSuggestLinksRejectsMissingRequiredColumns(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "bad.csv")
	if err := os.WriteFile(csvPath, []byte("source,target\nPROJ-1,PROJ-2\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	svc := &JiraService{tr: partialTracker{}}
	if _, err := svc.SuggestLinks(context.Background(), JiraLinkSuggestOpts{CSVPath: csvPath}); err == nil {
		t.Fatal("SuggestLinks missing type column: want error, got nil")
	}
}
