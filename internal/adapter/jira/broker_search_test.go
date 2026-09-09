package jira

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const brokerProjectFixture = `{"id":"7","key":"EXAMPLE","name":"Synthetic project","self":"https://example.invalid/project/7","description":"discarded supporting metadata","components":[],"issueTypes":[],"versions":[],"roles":{},"avatarUrls":{},"projectKeys":["EXAMPLE"],"archived":false}`

const brokerProjectIdentityPageFixture = `{"expand":"","startAt":0,"maxResults":2,"total":3,"issues":[{"id":"20","key":"EXAMPLE-20","fields":{"project":{"id":"7","key":"EXAMPLE"},"updated":"2026-09-08T10:00:00.000+0000"}},{"id":"3","key":"EXAMPLE-3","fields":{"project":{"id":"7","key":"EXAMPLE"},"updated":"2026-09-08T10:00:00.000+0000"}}]}`

func brokerProjectBusinessPageFixture() string {
	return strings.ReplaceAll(brokerProjectIdentityPageFixture,
		`"project":`, `"description":null,"summary":"Summary","project":`)
}

func brokerProjectPageTestAdapter(t *testing.T, responses []string, statuses []int, expected []string) (*Jira, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := requests
		requests++
		if index >= len(expected) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodGet || r.URL.RequestURI() != expected[index] || r.ContentLength != 0 {
			t.Errorf("request %d method=%s target=%s bytes=%d want=%s", index, r.Method, r.URL.RequestURI(), r.ContentLength, expected[index])
		}
		status := http.StatusOK
		if index < len(statuses) {
			status = statuses[index]
		}
		if status == http.StatusFound {
			w.Header().Set("Location", "/redirected")
		}
		w.WriteHeader(status)
		if index < len(responses) {
			_, _ = io.WriteString(w, responses[index])
		}
	}))
	t.Cleanup(server.Close)
	return New(server.URL, "synthetic-token", "test"), &requests
}

func brokerProjectIdentityTarget() string {
	return "/rest/api/2/search?fields=project%2Cupdated&jql=project+%3D+7+ORDER+BY+id+ASC&maxResults=2&startAt=0"
}

func brokerProjectBusinessTarget() string {
	return "/rest/api/2/search?fields=description%2Cproject%2Csummary%2Cupdated&jql=project+%3D+7+ORDER+BY+id+ASC&maxResults=2&startAt=0"
}

func TestBrokerJiraProjectPageUsesOnlyClosedServerConstructedSelector(t *testing.T) {
	adapter, requests := brokerProjectPageTestAdapter(t,
		[]string{brokerProjectFixture, brokerProjectIdentityPageFixture, brokerProjectBusinessPageFixture()}, nil,
		[]string{"/rest/api/2/project/EXAMPLE", brokerProjectIdentityTarget(), brokerProjectBusinessTarget()})
	project, err := adapter.QualifyBrokerProject(t.Context(), "EXAMPLE")
	if err != nil || project != (domain.BrokerJiraProjectIdentityV2{ID: "7", Key: "EXAMPLE", Complete: true}) {
		t.Fatalf("project=%+v err=%v", project, err)
	}
	identityPage, err := adapter.QualifyBrokerProjectIssuePage(t.Context(), project.ID, 0, 2)
	if err != nil || !identityPage.Complete || identityPage.StartAt != 0 || identityPage.MaxResults != 2 || identityPage.Total != 3 || identityPage.CoordinateExhausted || identityPage.PaginationStalled ||
		len(identityPage.Issues) != 2 || identityPage.Issues[0].ID != "20" || identityPage.Issues[1].ID != "3" {
		t.Fatalf("identity page=%+v err=%v", identityPage, err)
	}
	page, err := adapter.ReadBrokerProjectIssuePage(t.Context(), project.ID, []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary, domain.BrokerProjectPageFieldDescription}, 0, 2)
	if err != nil || !page.Complete || len(page.Issues) != 2 || page.Issues[0].Identity.ID != "20" || len(page.Issues[0].Fields) != 2 ||
		page.Issues[0].Fields[0].Field != domain.BrokerJiraIssueFieldDescription || !page.Issues[0].Fields[0].Null || page.Issues[0].Fields[1].Value != "Summary" || *requests != 3 {
		t.Fatalf("page=%+v err=%v requests=%d", page, err, *requests)
	}
}

func TestBrokerJiraProjectPageTruthForTerminalAndStalledCoordinates(t *testing.T) {
	for _, test := range []struct {
		name      string
		response  string
		exhausted bool
		stalled   bool
	}{
		{name: "terminal empty", response: `{"startAt":0,"maxResults":2,"total":0,"issues":[]}`, exhausted: true},
		{name: "stalled", response: `{"startAt":0,"maxResults":2,"total":3,"issues":[]}`, stalled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, requests := brokerProjectPageTestAdapter(t, []string{test.response}, nil, []string{brokerProjectIdentityTarget()})
			page, err := adapter.QualifyBrokerProjectIssuePage(t.Context(), "7", 0, 2)
			if err != nil || !page.Complete || page.Issues == nil || page.CoordinateExhausted != test.exhausted || page.PaginationStalled != test.stalled || *requests != 1 {
				t.Fatalf("page=%+v err=%v requests=%d", page, err, *requests)
			}
		})
	}
}

func TestBrokerJiraProjectPageAllowsExplicitIdentityOnlyBusinessFields(t *testing.T) {
	adapter, requests := brokerProjectPageTestAdapter(t, []string{brokerProjectIdentityPageFixture}, nil, []string{brokerProjectIdentityTarget()})
	page, err := adapter.ReadBrokerProjectIssuePage(t.Context(), "7", []domain.BrokerProjectPageField{}, 0, 2)
	if err != nil || len(page.Issues) != 2 || page.Issues[0].Fields == nil || len(page.Issues[0].Fields) != 0 || *requests != 1 {
		t.Fatalf("page=%+v err=%v requests=%d", page, err, *requests)
	}
}

func TestBrokerJiraProjectPageRejectsMalformedScopeAndPaging(t *testing.T) {
	pageCases := map[string]string{
		"null start":        `{"startAt":null,"maxResults":2,"total":0,"issues":[]}`,
		"null total":        `{"startAt":0,"maxResults":2,"total":null,"issues":[]}`,
		"wrong start":       strings.Replace(brokerProjectIdentityPageFixture, `"startAt":0`, `"startAt":1`, 1),
		"larger max":        strings.Replace(brokerProjectIdentityPageFixture, `"maxResults":2`, `"maxResults":3`, 1),
		"total below rows":  strings.Replace(brokerProjectIdentityPageFixture, `"total":3`, `"total":1`, 1),
		"duplicate id":      strings.Replace(brokerProjectIdentityPageFixture, `"id":"3"`, `"id":"20"`, 1),
		"duplicate key":     strings.Replace(brokerProjectIdentityPageFixture, `"key":"EXAMPLE-3"`, `"key":"EXAMPLE-20"`, 1),
		"sibling project":   strings.Replace(brokerProjectIdentityPageFixture, `"id":"7","key":"EXAMPLE"`, `"id":"8","key":"OTHER"`, 1),
		"missing updated":   strings.Replace(brokerProjectIdentityPageFixture, `,"updated":"2026-09-08T10:00:00.000+0000"`, ``, 1),
		"extra issue field": strings.Replace(brokerProjectIdentityPageFixture, `"project":`, `"summary":"hidden","project":`, 1),
		"warning":           strings.Replace(brokerProjectIdentityPageFixture, `"issues":`, `"warningMessages":["warning"],"issues":`, 1),
		"unknown root":      strings.Replace(brokerProjectIdentityPageFixture, `"issues":`, `"private":"hidden","issues":`, 1),
	}
	for name, response := range pageCases {
		t.Run(name, func(t *testing.T) {
			adapter, requests := brokerProjectPageTestAdapter(t, []string{response}, nil, []string{brokerProjectIdentityTarget()})
			page, err := adapter.QualifyBrokerProjectIssuePage(t.Context(), "7", 0, 2)
			if !errors.Is(err, domain.ErrCheckFailed) || page.Complete || *requests != 1 {
				t.Fatalf("page=%+v err=%v requests=%d", page, err, *requests)
			}
		})
	}
	projectCases := map[string]string{
		"key drift":        strings.Replace(brokerProjectFixture, `"key":"EXAMPLE"`, `"key":"OTHER"`, 1),
		"missing id":       strings.Replace(brokerProjectFixture, `"id":"7",`, ``, 1),
		"numeric id":       strings.Replace(brokerProjectFixture, `"id":"7"`, `"id":7`, 1),
		"unknown member":   strings.Replace(brokerProjectFixture, `"name":`, `"private":"hidden","name":`, 1),
		"oversized member": strings.Replace(brokerProjectFixture, `"Synthetic project"`, `"`+strings.Repeat("x", brokerJiraProjectSupportMaxString+1)+`"`, 1),
	}
	for name, response := range projectCases {
		t.Run(name, func(t *testing.T) {
			adapter, requests := brokerProjectPageTestAdapter(t, []string{response}, nil, []string{"/rest/api/2/project/EXAMPLE"})
			project, err := adapter.QualifyBrokerProject(t.Context(), "EXAMPLE")
			if !errors.Is(err, domain.ErrCheckFailed) || project.Complete || *requests != 1 {
				t.Fatalf("project=%+v err=%v requests=%d", project, err, *requests)
			}
		})
	}
}

func TestBrokerJiraProjectPageRejectsInputsBeforeHTTP(t *testing.T) {
	adapter, requests := brokerProjectPageTestAdapter(t, nil, nil, nil)
	for _, key := range []string{"example", "A", `EXAMPLE\" OR project=PRIVATE`, "https://example.invalid"} {
		if _, err := adapter.QualifyBrokerProject(t.Context(), key); err == nil {
			t.Errorf("accepted project key %q", key)
		}
	}
	for _, id := range []string{"", "0", "01", "EXAMPLE", "7/other"} {
		if _, err := adapter.QualifyBrokerProjectIssuePage(t.Context(), id, 0, 2); err == nil {
			t.Errorf("accepted project id %q", id)
		}
	}
	for _, selected := range [][]domain.BrokerProjectPageField{nil, {"unknown"}, {"summary", "summary"}} {
		if _, err := adapter.ReadBrokerProjectIssuePage(t.Context(), "7", selected, 0, 2); err == nil {
			t.Errorf("accepted fields %v", selected)
		}
	}
	for _, bounds := range [][2]int{{-1, 2}, {domain.BrokerProjectPageMaxStartAt + 1, 2}, {0, 0}, {0, 16}} {
		if _, err := adapter.QualifyBrokerProjectIssuePage(t.Context(), "7", bounds[0], bounds[1]); err == nil {
			t.Errorf("accepted bounds %v", bounds)
		}
	}
	if *requests != 0 {
		t.Fatalf("invalid inputs made %d HTTP requests", *requests)
	}
}

func TestBrokerJiraProjectPageNeverRetriesRedirectsOrLeaksDiagnostics(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		adapter, requests := brokerProjectPageTestAdapter(t, []string{`discarded-response-canary`}, []int{status}, []string{"/rest/api/2/project/EXAMPLE"})
		var trace bytes.Buffer
		adapter.c = httpx.New(adapter.c.Base(), "synthetic-token", "test", httpx.WithTrace(&trace))
		_, err := adapter.QualifyBrokerProject(context.Background(), "EXAMPLE")
		if err == nil || *requests != 1 {
			t.Fatalf("status=%d err=%v requests=%d", status, err, *requests)
		}
		for _, formatted := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), trace.String()} {
			if strings.Contains(formatted, "canary") || strings.Contains(formatted, "/rest/api/") || strings.Contains(formatted, "EXAMPLE") {
				t.Fatalf("unsafe diagnostic: %q", formatted)
			}
		}
	}
}

func FuzzDecodeBrokerJiraProjectPage(f *testing.F) {
	f.Add([]byte(brokerProjectIdentityPageFixture))
	f.Add([]byte(`{"startAt":0,"maxResults":2,"total":0,"issues":[]}`))
	f.Add([]byte(`{"startAt":0,"maxResults":2,"total":1,"issues":[{"id":"1","key":"EXAMPLE-1","fields":null}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > int(brokerJiraProjectPageIdentityBytes) {
			return
		}
		page, err := decodeBrokerJiraProjectIdentityPage(data, "7", 0, 2)
		if err == nil && (!page.Complete || len(page.Issues) > 2) {
			t.Fatalf("accepted invalid page=%+v", page)
		}
	})
}
