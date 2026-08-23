package jira

import (
	"context"
	"encoding/json"
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

func TestGuardedLabelStrictSnapshotAndNumericIDWrite(t *testing.T) {
	var requests []string
	var writeBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":["z","a"],"updated":"2026-08-22T10:00:00.000+0000"}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		writeBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	ctx := domain.WithSingleAttempt(t.Context())
	snapshot, err := adapter.ReadGuardedLabelSnapshot(ctx, "OPS-1")
	if err != nil || snapshot.ID != "10" || snapshot.Key != "OPS-1" || snapshot.Project != "OPS" || strings.Join(snapshot.Labels, ",") != "a,z" || !snapshot.Complete {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	write := domain.JiraGuardedLabelWrite{ID: "10", Key: "OPS-1", Project: "OPS", Add: []string{"z", "a"}, Remove: []string{"old"}}
	if err := adapter.WriteGuardedLabelDelta(ctx, write); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0] != "GET /rest/api/2/issue/OPS-1?fields=project%2Clabels%2Cupdated" || requests[1] != "PUT /rest/api/2/issue/10" {
		t.Fatalf("requests=%v", requests)
	}
	if writeBody != `{"update":{"labels":[{"add":"a"},{"add":"z"},{"remove":"old"}]}}` {
		t.Fatalf("body=%s", writeBody)
	}
	wantAuth := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Key: "OPS-1", Project: "OPS"}}}
	if len(authorizer.requests) != 1 || !reflect.DeepEqual(authorizer.requests[0], wantAuth) {
		t.Fatalf("authorization=%+v", authorizer.requests)
	}
}

func TestGuardedLabelStrictDecoderRejectsIncompleteMalformedAndUnboundedEvidence(t *testing.T) {
	labels4096 := make([]string, jiraGuardedLabelMaxCurrent)
	for index := range labels4096 {
		labels4096[index] = fmt.Sprintf("label-%04d", index)
	}
	bounded, _ := json.Marshal(map[string]any{"id": "10", "key": "OPS-1", "fields": map[string]any{
		"project": map[string]string{"key": "OPS"}, "labels": labels4096, "updated": "2026-08-22T10:00:00Z",
	}})
	boundedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bounded) }))
	boundedSnapshot, boundedErr := New(boundedServer.URL, "token", "test").ReadGuardedLabelSnapshot(domain.WithSingleAttempt(t.Context()), "OPS-1")
	boundedServer.Close()
	if boundedErr != nil || len(boundedSnapshot.Labels) != jiraGuardedLabelMaxCurrent {
		t.Fatalf("4096 labels snapshot=%d err=%v", len(boundedSnapshot.Labels), boundedErr)
	}

	labels4097 := make([]string, jiraGuardedLabelMaxCurrent+1)
	for index := range labels4097 {
		labels4097[index] = fmt.Sprintf("label-%04d", index)
	}
	large, _ := json.Marshal(map[string]any{"id": "10", "key": "OPS-1", "fields": map[string]any{
		"project": map[string]string{"key": "OPS"}, "labels": labels4097, "updated": "2026-08-22T10:00:00Z",
	}})
	valid := `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[],"updated":"2026-08-22T10:00:00Z"}}`
	tests := map[string]string{
		"omitted project":     `{"id":"10","key":"OPS-1","fields":{"labels":[],"updated":"2026-08-22T10:00:00Z"}}`,
		"null project":        `{"id":"10","key":"OPS-1","fields":{"project":null,"labels":[],"updated":"2026-08-22T10:00:00Z"}}`,
		"moved project":       `{"id":"10","key":"OPS-1","fields":{"project":{"key":"ALT"},"labels":[],"updated":"2026-08-22T10:00:00Z"}}`,
		"noncanonical id":     strings.Replace(valid, `"10"`, `"01"`, 1),
		"noncanonical key":    strings.Replace(valid, `"OPS-1"`, `"ops-1"`, 1),
		"omitted labels":      `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-08-22T10:00:00Z"}}`,
		"null labels":         `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":null,"updated":"2026-08-22T10:00:00Z"}}`,
		"object labels":       `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":{},"updated":"2026-08-22T10:00:00Z"}}`,
		"empty label":         `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[""],"updated":"2026-08-22T10:00:00Z"}}`,
		"invalid UTF-8 label": "{\"id\":\"10\",\"key\":\"OPS-1\",\"fields\":{\"project\":{\"key\":\"OPS\"},\"labels\":[\"\xff\"],\"updated\":\"2026-08-22T10:00:00Z\"}}",
		"lone high surrogate": strings.Replace(valid, `"labels":[]`, `"labels":["\ud800"]`, 1),
		"lone low surrogate":  strings.Replace(valid, `"labels":[]`, `"labels":["\udc00"]`, 1),
		"duplicate member":    strings.Replace(valid, `"labels":[]`, `"labels":[],"labels":[]`, 1),
		"duplicate label":     `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":["x","x"],"updated":"2026-08-22T10:00:00Z"}}`,
		"oversized label":     strings.Replace(valid, `"labels":[]`, `"labels":["`+strings.Repeat("x", 256)+`"]`, 1),
		"4097 labels":         string(large),
		"omitted updated":     `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[]}}`,
		"null updated":        `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[],"updated":null}}`,
		"numeric updated":     `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[],"updated":1}}`,
		"invalid updated":     `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[],"updated":"today"}}`,
		"trailing":            valid + `{}`,
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			_, err := New(server.URL, "token", "test").ReadGuardedLabelSnapshot(domain.WithSingleAttempt(t.Context()), "OPS-1")
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for name, test := range map[string]struct{ response, label string }{
		"valid surrogate pair": {strings.Replace(valid, `"labels":[]`, `"labels":["\ud83d\ude00"]`, 1), "😀"},
		"escaped backslash":    {strings.Replace(valid, `"labels":[]`, `"labels":["\\ud800"]`, 1), `\ud800`},
		"literal replacement":  {strings.Replace(valid, `"labels":[]`, `"labels":["�"]`, 1), "�"},
		"escaped replacement":  {strings.Replace(valid, `"labels":[]`, `"labels":["\ufffd"]`, 1), "�"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.response) }))
			defer server.Close()
			snapshot, err := New(server.URL, "token", "test").ReadGuardedLabelSnapshot(domain.WithSingleAttempt(t.Context()), "OPS-1")
			if err != nil || len(snapshot.Labels) != 1 || snapshot.Labels[0] != test.label {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}

func TestGuardedLabelWriterRejectsInvalidDeltaBeforeAuthorizationOrTransport(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	tests := []domain.JiraGuardedLabelWrite{
		{ID: "01", Key: "OPS-1", Project: "OPS", Add: []string{"x"}},
		{ID: "10", Key: "ALT-1", Project: "OPS", Add: []string{"x"}},
		{ID: "10", Key: "OPS-1", Project: "OPS"},
		{ID: "10", Key: "OPS-1", Project: "OPS", Add: []string{"x", "x"}},
		{ID: "10", Key: "OPS-1", Project: "OPS", Add: []string{"x"}, Remove: []string{"x"}},
		{ID: "10", Key: "OPS-1", Project: "OPS", Add: []string{strings.Repeat("x", 256)}},
	}
	for _, write := range tests {
		err := adapter.WriteGuardedLabelDelta(t.Context(), write)
		var diagnostic interface{ DiagnosticWriteAttempted() bool }
		if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &diagnostic) || diagnostic.DiagnosticWriteAttempted() {
			t.Fatalf("write=%+v err=%v", write, err)
		}
	}
	if requests.Load() != 0 || len(authorizer.requests) != 0 {
		t.Fatalf("requests=%d authorization=%d", requests.Load(), len(authorizer.requests))
	}
}

func TestGuardedLabelWriteIsSingleAttemptNoRedirectAndChargesErrorBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, "bad")
	}))
	defer server.Close()
	budget, _ := domain.NewReadBudget(1, 3)
	ctx := domain.WithSingleAttempt(domain.WithReadBudget(context.Background(), budget))
	err := New(server.URL, "token", "test").WriteGuardedLabelDelta(ctx, domain.JiraGuardedLabelWrite{
		ID: "10", Key: "OPS-1", Project: "OPS", Add: []string{"x"},
	})
	if err == nil || requests.Load() != 1 || budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 3}) {
		t.Fatalf("err=%v requests=%d budget=%+v", err, requests.Load(), budget.Usage())
	}
}
