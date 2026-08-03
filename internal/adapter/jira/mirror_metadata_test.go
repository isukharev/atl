package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestPlanIssueMetadataBatchesBoundsCountAndEscapedSelectorBytes(t *testing.T) {
	j := &Jira{}
	short := make([]string, 101)
	for i := range short {
		short[i] = fmt.Sprintf("PROJ-%d", i+1)
	}
	batches, err := j.PlanIssueMetadataBatches(short)
	if err != nil || !reflect.DeepEqual(issueMetadataBatchLengths(batches), []int{100, 1}) {
		t.Fatalf("batches=%v err=%v", issueMetadataBatchLengths(batches), err)
	}

	long := make([]string, 101)
	for i := range long {
		long[i] = strings.Repeat(`x"\`, 70)
	}
	batches, err = j.PlanIssueMetadataBatches(long)
	if err != nil {
		t.Fatal(err)
	}
	var flattened []string
	for _, batch := range batches {
		parts := make([]string, len(batch))
		for i, key := range batch {
			parts[i], err = quoteJiraJQLString(key)
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(batch) == 0 || len(batch) > jiraIssueMetadataBatchMaxKeys || len(strings.Join(parts, ",")) > jiraIssueMetadataSelectorMaxBytes {
			t.Fatalf("batch size=%d selector_bytes=%d", len(batch), len(strings.Join(parts, ",")))
		}
		flattened = append(flattened, batch...)
	}
	if !reflect.DeepEqual(flattened, long) {
		t.Fatal("batch planning changed canonical input order")
	}
	if _, err := j.PlanIssueMetadataBatches([]string{strings.Repeat(`\`, jiraIssueMetadataSelectorMaxBytes)}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized selector error=%v", err)
	}
	if _, err := j.PlanIssueMetadataBatches([]string{"PROJ-1\nOR key is not EMPTY"}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("control-character selector error=%v", err)
	}
}

func issueMetadataBatchLengths(batches [][]string) []int {
	out := make([]int, len(batches))
	for i := range batches {
		out[i] = len(batches[i])
	}
	return out
}

func TestReadIssueMetadataBatchBuildsEscapedQualifiedQueryOnce(t *testing.T) {
	keys := []string{"PROJ-1", `X" OR project=SECRET`, `A\B-2`}
	wantJQL := `key in ("PROJ-1","X\" OR project=SECRET","A\\B-2")`
	wantFields := "description,customfield_2,customfield_1"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		query := request.URL.Query()
		if query.Get("jql") != wantJQL || query.Get("fields") != wantFields || query.Get("maxResults") != "3" || query.Get("startAt") != "0" {
			t.Errorf("query=%v", query)
		}
		_, _ = fmt.Fprintf(w, `{"issues":[{"id":"3","key":%q,"fields":{"description":"three"}},{"id":"1","key":%q,"fields":{"description":"one"}},{"id":"2","key":%q,"fields":{"description":null}}],"startAt":0,"maxResults":3,"total":3}`, keys[2], keys[0], keys[1])
	}))
	defer server.Close()

	got, err := newTestJira(server).ReadIssueMetadataBatch(context.Background(), keys, strings.Split(wantFields, ","))
	if err != nil || !got.Complete || got.PartialReason != "" || len(got.Issues) != 3 || requests != 1 || got.Issues[2].Body != "" {
		t.Fatalf("requests=%d batch=%+v err=%v", requests, got, err)
	}
}

func TestReadIssueMetadataBatchPreservesClosedPartialReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[],"startAt":0,"maxResults":2,"total":2}`))
	}))
	defer server.Close()
	got, err := newTestJira(server).ReadIssueMetadataBatch(context.Background(), []string{"PROJ-1", "PROJ-2"}, []string{"description"})
	if err != nil || got.Complete || got.PartialReason != domain.IssueSearchPartialPaginationStalled {
		t.Fatalf("batch=%+v err=%v", got, err)
	}
}

func TestReadIssueMetadataBatchRejectsBoundsBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	j := newTestJira(server)
	tooMany := make([]string, jiraIssueMetadataBatchMaxKeys+1)
	if _, err := j.ReadIssueMetadataBatch(context.Background(), tooMany, []string{"description"}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("too-many error=%v", err)
	}
	if _, err := j.ReadIssueMetadataBatch(context.Background(), []string{strings.Repeat(`\`, jiraIssueMetadataSelectorMaxBytes)}, []string{"description"}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized error=%v", err)
	}
	if requests != 0 {
		t.Fatalf("requests=%d", requests)
	}
}
