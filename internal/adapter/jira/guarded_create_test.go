package jira

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestCreatePreparationPreservesLegacyAndGuardedWireBytes(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"key":"OPS-9"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"9007199254740993","key":"OPS-9"}`)
	}))
	defer server.Close()
	adapter := New(server.URL, "token", "test")
	inputs := map[string]domain.JiraFieldInput{
		"customfield_1": {Value: `9007199254740993`, ExplicitJSON: true},
		"labels":        {Value: `["one","two"]`},
	}
	legacy, err := adapter.Create(t.Context(), "OPS", "10", "Summary", []byte("wiki"), inputs)
	if err != nil || legacy.Key != "OPS-9" {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
	prepared, err := adapter.PrepareGuardedCreate(domain.JiraGuardedCreatePreparationRequest{
		ProjectKey: "OPS", IssueTypeID: "10", Summary: "Summary", Description: []byte("wiki"), DescriptionPresent: true, Fields: inputs,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"fields":{"customfield_1":9007199254740993,"description":"wiki","issuetype":{"id":"10"},"labels":["one","two"],"project":{"key":"OPS"},"summary":"Summary"}}`
	if string(prepared.Payload) != want || bodies[0] != want {
		t.Fatalf("prepared=%s legacy=%s want=%s", prepared.Payload, bodies[0], want)
	}
	ack, err := adapter.WriteGuardedCreate(domain.WithSingleAttempt(t.Context()), domain.JiraGuardedCreateWrite{Payload: prepared.Payload, ProjectID: "7", ProjectKey: "OPS"})
	if err != nil || ack.ID != "9007199254740993" || ack.Key != "OPS-9" || bodies[1] != want {
		t.Fatalf("ack=%+v err=%v guarded=%s", ack, err, bodies[1])
	}
}

func TestLegacyCreateCanonicalizesAuthorizedProjectBeforePreparingAndAcceptsEmptySuccess(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	issue, err := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer)).Create(
		t.Context(), " ops ", "10", "Summary", []byte("wiki"), nil,
	)
	if err != nil || issue == nil || issue.Key != "" || issue.Project != "OPS" {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	wantBody := `{"fields":{"description":"wiki","issuetype":{"id":"10"},"project":{"key":"OPS"},"summary":"Summary"}}`
	if body != wantBody {
		t.Fatalf("body=%s want=%s", body, wantBody)
	}
	wantAuth := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbCreate}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: "OPS"}}}
	if len(authorizer.requests) != 1 || !reflect.DeepEqual(authorizer.requests[0], wantAuth) {
		t.Fatalf("authorization=%+v want=%+v", authorizer.requests, wantAuth)
	}
}

func TestGuardedCreateWriterAuthorizesExactProjectWithoutLookup(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"11"}`)
	}))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	prepared, err := adapter.PrepareGuardedCreate(domain.JiraGuardedCreatePreparationRequest{ProjectKey: "OPS", IssueTypeID: "10", Summary: "Summary"})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := adapter.WriteGuardedCreate(t.Context(), domain.JiraGuardedCreateWrite{Payload: prepared.Payload, ProjectID: "7", ProjectKey: "OPS"})
	want := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbCreate}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: "OPS"}}}
	if err != nil || ack.ID != "11" || len(paths) != 1 || paths[0] != "/rest/api/2/issue" || len(authorizer.requests) != 1 || !reflect.DeepEqual(authorizer.requests[0], want) {
		t.Fatalf("ack=%+v err=%v paths=%v authorization=%+v", ack, err, paths, authorizer.requests)
	}
}

func TestGuardedCreateAcknowledgementRefusalsPreserveOnlySafeID(t *testing.T) {
	responses := map[string]string{
		"key only":      `{"key":"OPS-1"}`,
		"missing":       `{}`,
		"malformed id":  `{"id":"01","key":"OPS-1"}`,
		"malformed key": `{"id":"10","key":"OTHER-1"}`,
		"trailing":      `{"id":"10"}{}`,
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			adapter := New(server.URL, "token", "test")
			prepared, _ := adapter.PrepareGuardedCreate(domain.JiraGuardedCreatePreparationRequest{ProjectKey: "OPS", IssueTypeID: "10", Summary: "S"})
			ack, err := adapter.WriteGuardedCreate(t.Context(), domain.JiraGuardedCreateWrite{Payload: prepared.Payload, ProjectID: "7", ProjectKey: "OPS"})
			if err == nil {
				t.Fatal("unqualified acknowledgement accepted")
			}
			if name == "malformed key" && ack.ID != "10" || name != "malformed key" && ack.ID != "" {
				t.Fatalf("ack=%+v err=%v", ack, err)
			}
		})
	}
}

func TestGuardedCreateStrictReadbackPreservesTypedEvidence(t *testing.T) {
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"id":"7","key":"OPS"},"issuetype":{"id":"3"},"summary":"S","description":null,"created":"2026-08-22T10:00:00.000+0000","updated":"2026-08-22T10:00:01.000+0000","customfield_1":{"value":9007199254740993,"extra":true}}}`)
	}))
	defer server.Close()
	readback, err := New(server.URL, "token", "test").ReadGuardedCreate(t.Context(), domain.JiraGuardedCreateReadRequest{ID: "10", Fields: []string{"customfield_1", "summary", "customfield_1"}})
	if err != nil || readback.ID != "10" || readback.ProjectID != "7" || !readback.Description.Present || readback.Description.Value != nil || !readback.Fields["customfield_1"].Present {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
	if !strings.Contains(rawQuery, "fields=") || strings.Count(rawQuery, "customfield_1") != 1 {
		t.Fatalf("query=%q", rawQuery)
	}
}

func TestGuardedCreateWriteIsSingleAttemptNoRedirectAndChargesErrorBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, "bad")
	}))
	defer server.Close()
	budget, _ := domain.NewReadBudget(2, 3)
	ctx := domain.WithSingleAttempt(domain.WithReadBudget(context.Background(), budget))
	adapter := New(server.URL, "token", "test")
	prepared, _ := adapter.PrepareGuardedCreate(domain.JiraGuardedCreatePreparationRequest{ProjectKey: "OPS", IssueTypeID: "10", Summary: "S"})
	_, err := adapter.WriteGuardedCreate(ctx, domain.JiraGuardedCreateWrite{Payload: prepared.Payload, ProjectID: "7", ProjectKey: "OPS"})
	if err == nil || requests.Load() != 1 || budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 3}) {
		t.Fatalf("err=%v requests=%d budget=%+v", err, requests.Load(), budget.Usage())
	}
	if errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
		t.Fatalf("write retried: %v", err)
	}
}

func TestGuardedCreateRejectsUnqualifiedEvidenceBeforeDispatch(t *testing.T) {
	t.Run("write preflight", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
		defer server.Close()

		adapter := New(server.URL, "token", "test")
		_, err := adapter.WriteGuardedCreate(t.Context(), domain.JiraGuardedCreateWrite{
			Payload: []byte(`{"fields":{"summary":"S"}}`), ProjectID: "7", ProjectKey: "OPS",
		})
		var diagnostic interface{ DiagnosticWriteAttempted() bool }
		if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &diagnostic) || diagnostic.DiagnosticWriteAttempted() || err.Error() != "guarded Jira create was denied before dispatch" || requests.Load() != 0 {
			t.Fatalf("err=%v diagnostic=%T requests=%d", err, diagnostic, requests.Load())
		}

		authorizer := &recordingWriteAuthorizer{err: domain.ErrForbidden}
		adapter = New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
		prepared, prepareErr := adapter.PrepareGuardedCreate(domain.JiraGuardedCreatePreparationRequest{ProjectKey: "OPS", IssueTypeID: "10", Summary: "S"})
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		_, err = adapter.WriteGuardedCreate(t.Context(), domain.JiraGuardedCreateWrite{Payload: prepared.Payload, ProjectID: "7", ProjectKey: "OPS"})
		if !errors.Is(err, domain.ErrForbidden) || !errors.As(err, &diagnostic) || diagnostic.DiagnosticWriteAttempted() || requests.Load() != 0 || len(authorizer.requests) != 1 {
			t.Fatalf("err=%v diagnostic=%T requests=%d authorization=%d", err, diagnostic, requests.Load(), len(authorizer.requests))
		}
	})

	tooManyFields := make([]string, jiraGuardedCreateMaxFields)
	for index := range tooManyFields {
		tooManyFields[index] = fmt.Sprintf("customfield_%d", index)
	}
	oversizedQuery := make([]string, 65)
	for index := range oversizedQuery {
		oversizedQuery[index] = strings.Repeat("x", 1020) + fmt.Sprintf("%04d", index)
	}
	readCases := []struct {
		name     string
		request  domain.JiraGuardedCreateReadRequest
		status   int
		response string
		contact  bool
	}{
		{name: "invalid id", request: domain.JiraGuardedCreateReadRequest{ID: "01"}},
		{name: "invalid field", request: domain.JiraGuardedCreateReadRequest{ID: "10", Fields: []string{"bad\nfield"}}},
		{name: "too many fields", request: domain.JiraGuardedCreateReadRequest{ID: "10", Fields: tooManyFields}},
		{name: "oversized query", request: domain.JiraGuardedCreateReadRequest{ID: "10", Fields: oversizedQuery}},
		{name: "http failure", request: domain.JiraGuardedCreateReadRequest{ID: "10"}, status: http.StatusInternalServerError, response: `{}`, contact: true},
		{name: "missing envelope", request: domain.JiraGuardedCreateReadRequest{ID: "10"}, response: `{}`, contact: true},
		{name: "malformed project", request: domain.JiraGuardedCreateReadRequest{ID: "10"}, response: `{"id":"10","key":"OPS-1","fields":{"project":{"id":"01","key":"OPS"},"issuetype":{"id":"3"},"summary":"S"}}`, contact: true},
		{name: "malformed summary", request: domain.JiraGuardedCreateReadRequest{ID: "10"}, response: `{"id":"10","key":"OPS-1","fields":{"project":{"id":"7","key":"OPS"},"issuetype":{"id":"3"},"summary":{}}}`, contact: true},
	}
	for _, test := range readCases {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if test.status != 0 {
					w.WriteHeader(test.status)
				}
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			ctx := domain.WithSingleAttempt(t.Context())
			_, err := New(server.URL, "token", "test").ReadGuardedCreate(ctx, test.request)
			wantRequests := int32(0)
			if test.contact {
				wantRequests = 1
			}
			if err == nil || requests.Load() != wantRequests || test.name != "http failure" && !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v requests=%d contact=%v", err, requests.Load(), test.contact)
			}
		})
	}
}
