package jira

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestReadIssueSnapshotProjectionRequestsOnlyQualifiedSections(t *testing.T) {
	for _, projection := range []domain.IssueSnapshotProjection{
		{Fields: []string{"summary"}},
		{Fields: []string{"summary", "issuelinks"}},
		{Fields: []string{"summary", "attachment"}},
		{Fields: []string{"summary", "issuelinks", "attachment"}},
		{Fields: []string{"summary"}, Properties: true},
		{Fields: []string{"*all"}, SupportingFieldsReason: "hierarchy_discovery"},
	} {
		t.Run(strings.Join(projection.Fields, ","), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != "GET" || r.URL.Path != "/rest/api/2/issue/PROJ-1" {
					t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
				}
				want := map[string][]string{"fields": {strings.Join(projection.Fields, ",")}, "expand": {"names,schema"}}
				if projection.Properties {
					want["properties"] = []string{"*all"}
				}
				if !reflect.DeepEqual(map[string][]string(r.URL.Query()), want) {
					t.Errorf("query=%v want=%v", r.URL.Query(), want)
				}
				_, _ = w.Write([]byte(`{"id":"1","key":"PROJ-1","fields":{"summary":"Seed"},"names":{},"schema":{},"properties":{"unsolicited":"PROJ-9"}}`))
			}))
			defer server.Close()
			snapshot, err := New(server.URL, "token", "test").ReadIssueSnapshotProjection(t.Context(), "PROJ-1", projection)
			if err != nil || requests != 1 || snapshot.Properties == nil {
				t.Fatalf("snapshot=%+v requests=%d error=%v", snapshot, requests, err)
			}
			if !projection.Properties && len(snapshot.Properties) != 0 {
				t.Fatal("unrequested properties retained")
			}
		})
	}
}

func TestReadIssueSnapshotProjectionAllowsOnlyOmittedUnrequestedProperties(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"id":"1","key":"PROJ-1","fields":{"summary":"Seed"},"names":{},"schema":{}}`))
	}))
	defer server.Close()
	client := New(server.URL, "token", "test")
	_, err := client.ReadIssueSnapshotProjection(t.Context(), "PROJ-1", domain.IssueSnapshotProjection{Fields: []string{"summary"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ReadIssueSnapshotProjection(t.Context(), "PROJ-1", domain.IssueSnapshotProjection{Fields: []string{"summary"}, Properties: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing requested properties error=%v", err)
	}
	for _, projection := range []domain.IssueSnapshotProjection{
		{}, {Fields: []string{"description"}}, {Fields: []string{"summary", "summary"}},
		{Fields: []string{"summary"}, SupportingFieldsReason: "hierarchy_discovery"},
		{Fields: []string{"*all"}, SupportingFieldsReason: "unknown"},
	} {
		_, err := client.ReadIssueSnapshotProjection(t.Context(), "PROJ-1", projection)
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("projection=%+v error=%v", projection, err)
		}
	}
	if requests != 2 {
		t.Fatalf("invalid projections made requests: %d", requests)
	}
}
