package jira

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestGuardedUpdateReadPrepareWriteUsesExactNumericID(t *testing.T) {
	var requests []string
	var putBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/field":
			_, _ = io.WriteString(w, `[{"id":"customfield_1","name":"Example","custom":true}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/OPS-1":
			_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-09-08T00:00:00.000+0000","summary":"Old","description":"old wiki","customfield_1":1}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/10":
			putBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	catalog, err := adapter.ReadGuardedUpdateFieldCatalog(t.Context(), []string{"customfield_1"})
	if err != nil || !catalog.Complete || len(catalog.Fields) != 1 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	issue, err := adapter.ReadGuardedUpdateIssue(t.Context(), "OPS-1", []string{"summary", "description", "customfield_1"})
	if err != nil || !issue.Complete || issue.ID != "10" || issue.Key != "OPS-1" || issue.Project != "OPS" || len(issue.Fields) != 3 {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	prepared, err := adapter.PrepareGuardedUpdate(domain.JiraGuardedUpdatePreparationRequest{
		Summary: "New", SummaryPresent: true,
		Description: []byte("new wiki"), DescriptionPresent: true, DescriptionSource: "wiki",
		Fields:    map[string]domain.JiraFieldInput{"customfield_1": {Value: "9007199254740993", ExplicitJSON: true}},
		Qualified: catalog.Fields,
	})
	wantPayload := `{"fields":{"customfield_1":9007199254740993,"description":"new wiki","summary":"New"}}`
	if err != nil || string(prepared.Payload) != wantPayload || len(prepared.Fields) != 3 {
		t.Fatalf("prepared=%+v payload=%s err=%v", prepared.Fields, prepared.Payload, err)
	}
	requests = nil
	err = adapter.WriteGuardedUpdate(t.Context(), domain.JiraGuardedUpdateWrite{
		ID: issue.ID, Key: issue.Key, Project: issue.Project, Qualified: catalog.Fields, Prepared: prepared,
	})
	wantAuth := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{{
		Service: "jira", Kind: "issue", Key: "OPS-1", Project: "OPS",
	}}}
	if err != nil || !bytes.Equal(putBody, prepared.Payload) || !reflect.DeepEqual(requests, []string{"PUT /rest/api/2/issue/10"}) ||
		len(authorizer.requests) != 1 || !reflect.DeepEqual(authorizer.requests[0], wantAuth) {
		t.Fatalf("err=%v requests=%v body=%s auth=%+v", err, requests, putBody, authorizer.requests)
	}
}

func TestGuardedUpdatePreparationRejectsUnqualifiedAndLossyInputs(t *testing.T) {
	qualified := []domain.JiraGuardedFieldCatalogEntry{{ID: "customfield_1", Custom: true}}
	tests := []domain.JiraGuardedUpdatePreparationRequest{
		{Fields: map[string]domain.JiraFieldInput{"priority": {Value: `{"id":"1"}`}}},
		{Fields: map[string]domain.JiraFieldInput{"summary": {Value: "override"}}, Qualified: qualified},
		{Fields: map[string]domain.JiraFieldInput{"customfield_1": {Value: `{"a":1,"a":2}`}}, Qualified: qualified},
		{Fields: map[string]domain.JiraFieldInput{"customfield_1": {Value: `{"a":`}}, Qualified: qualified},
		{Fields: map[string]domain.JiraFieldInput{"customfield_1": {Value: `true`, ExplicitJSON: true}}},
		{SummaryPresent: true},
		{DescriptionPresent: true, DescriptionSource: "none"},
		{},
	}
	adapter := &Jira{}
	for index, request := range tests {
		if _, err := adapter.PrepareGuardedUpdate(request); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func TestGuardedUpdateWriterRejectsTamperingBeforeIO(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	adapter := New(server.URL, "token", "test")
	prepared, err := adapter.PrepareGuardedUpdate(domain.JiraGuardedUpdatePreparationRequest{
		Summary: "New", SummaryPresent: true, DescriptionSource: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared.Payload = append([]byte(nil), prepared.Payload...)
	prepared.Payload[len(prepared.Payload)-2] ^= 1
	err = adapter.WriteGuardedUpdate(t.Context(), domain.JiraGuardedUpdateWrite{
		ID: "10", Key: "OPS-1", Project: "OPS", Prepared: prepared,
	})
	var attempted interface{ DiagnosticWriteAttempted() bool }
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &attempted) || attempted.DiagnosticWriteAttempted() || requests.Load() != 0 {
		t.Fatalf("err=%v attempted=%T requests=%d", err, attempted, requests.Load())
	}
}

func TestGuardedUpdatePreparationCapsSummaryAndDescriptionAggregate(t *testing.T) {
	_, err := (&Jira{}).PrepareGuardedUpdate(domain.JiraGuardedUpdatePreparationRequest{
		Summary: "x", SummaryPresent: true,
		Description: make([]byte, domain.JiraGuardedUpdateMaxInputBytes), DescriptionPresent: true, DescriptionSource: "wiki",
	})
	if err == nil || !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("aggregate overflow error=%v", err)
	}
}

func TestGuardedUpdateReadComposesCallerBudget(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-09-08T00:00:00.000+0000","summary":"Old"}}`)
	}))
	defer server.Close()
	adapter := New(server.URL, "token", "test")

	noAttempts, _ := domain.NewReadBudget(0, 1024)
	_, err := adapter.ReadGuardedUpdateIssue(domain.WithReadBudget(t.Context(), noAttempts), "OPS-1", []string{"summary"})
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) || requests.Load() != 0 {
		t.Fatalf("attempt error=%v requests=%d", err, requests.Load())
	}
	oneTinyResponse, _ := domain.NewReadBudget(1, 8)
	_, err = adapter.ReadGuardedUpdateIssue(domain.WithReadBudget(t.Context(), oneTinyResponse), "OPS-1", []string{"summary"})
	if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || requests.Load() != 1 {
		t.Fatalf("response error=%v requests=%d", err, requests.Load())
	}
}

func TestGuardedUpdateWritePolicyDenialIsNoAttempt(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{err: domain.ErrForbidden}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	prepared, err := adapter.PrepareGuardedUpdate(domain.JiraGuardedUpdatePreparationRequest{
		Summary: "New", SummaryPresent: true, DescriptionSource: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.WriteGuardedUpdate(t.Context(), domain.JiraGuardedUpdateWrite{ID: "10", Key: "OPS-1", Project: "OPS", Prepared: prepared})
	var attempted interface{ DiagnosticWriteAttempted() bool }
	if !errors.Is(err, domain.ErrForbidden) || !errors.As(err, &attempted) || attempted.DiagnosticWriteAttempted() || requests.Load() != 0 || len(authorizer.requests) != 1 {
		t.Fatalf("err=%v attempted=%T requests=%d auth=%d", err, attempted, requests.Load(), len(authorizer.requests))
	}
}

func TestGuardedUpdateWriteRefusesMutatingRedirect(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/rest/api/2/issue/11")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(&recordingWriteAuthorizer{}))
	prepared, err := adapter.PrepareGuardedUpdate(domain.JiraGuardedUpdatePreparationRequest{
		Summary: "New", SummaryPresent: true, DescriptionSource: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.WriteGuardedUpdate(t.Context(), domain.JiraGuardedUpdateWrite{ID: "10", Key: "OPS-1", Project: "OPS", Prepared: prepared})
	if err == nil || requests.Load() != 1 {
		t.Fatalf("redirect err=%v requests=%d", err, requests.Load())
	}
}

func TestDecodeGuardedUpdateIssueRejectsIncompleteEvidence(t *testing.T) {
	fields := []string{"customfield_1", "description", "summary"}
	valid := `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-09-08T00:00:00.000+0000","summary":"S","description":null,"customfield_1":{"id":"1"}}}`
	tests := map[string]string{
		"valid":              valid,
		"missing field":      strings.Replace(valid, `,"description":null`, "", 1),
		"moved key":          strings.Replace(valid, `"key":"OPS-1"`, `"key":"ALT-1"`, 1),
		"timezone-less":      strings.Replace(valid, `2026-09-08T00:00:00.000+0000`, `2026-09-08T00:00:00`, 1),
		"duplicate":          strings.Replace(valid, `"id":"10"`, `"id":"10","id":"11"`, 1),
		"trailing":           valid + `{}`,
		"lossy":              strings.Replace(valid, `"summary":"S"`, `"summary":"\ud800"`, 1),
		"object summary":     strings.Replace(valid, `"summary":"S"`, `"summary":{"text":"S"}`, 1),
		"object description": strings.Replace(valid, `"description":null`, `"description":{"type":"doc"}`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			issue, err := decodeGuardedUpdateIssue([]byte(body), fields)
			if name == "valid" {
				if err != nil || !issue.Complete || issue.Fields["description"].Value != nil {
					t.Fatalf("issue=%+v err=%v", issue, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted issue=%+v", issue)
			}
		})
	}
}

func FuzzDecodeGuardedUpdateIssue(f *testing.F) {
	for _, seed := range []string{
		`{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-09-08T00:00:00.000+0000","summary":"S","description":null,"customfield_1":1}}`,
		`{"id":"10","id":"11","key":"OPS-1","fields":{}}`,
		`{"id":"10","key":"OPS-1","fields":{"summary":"\ud800"}}`,
	} {
		f.Add([]byte(seed))
	}
	fields := []string{"customfield_1", "description", "summary"}
	f.Fuzz(func(t *testing.T, body []byte) {
		issue, err := decodeGuardedUpdateIssue(body, fields)
		if err == nil && (!issue.Complete || issue.ID == "" || issue.Key == "" || issue.Project == "" || len(issue.Fields) != len(fields)) {
			t.Fatalf("incomplete successful evidence=%+v", issue)
		}
	})
}
