package jira

import (
	"bytes"
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

func TestGuardedFieldCatalogIsStrictCompleteAndSelected(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		selected []string
		ok       bool
	}{
		{name: "selected plugin id", body: []byte(`[{"id":"summary","custom":false},{"id":"plugin.vendor","custom":true}]`), selected: []string{"plugin.vendor"}, ok: true},
		{name: "missing custom", body: []byte(`[{"id":"plugin.vendor"}]`), selected: []string{"plugin.vendor"}},
		{name: "duplicate id", body: []byte(`[{"id":"plugin.vendor","custom":true},{"id":"plugin.vendor","custom":true}]`), selected: []string{"plugin.vendor"}},
		{name: "duplicate member", body: []byte(`[{"id":"plugin.vendor","custom":true,"custom":true}]`), selected: []string{"plugin.vendor"}},
		{name: "unpaired surrogate", body: []byte(`[{"id":"plugin\uD800","custom":true}]`), selected: []string{"plugin.vendor"}},
		{name: "trailing", body: []byte(`[{"id":"plugin.vendor","custom":true}] {}`), selected: []string{"plugin.vendor"}},
		{name: "hostile reserved", body: []byte(`[{"id":"project","custom":true}]`), selected: []string{"project"}},
		{name: "selected not custom", body: []byte(`[{"id":"plugin.vendor","custom":false}]`), selected: []string{"plugin.vendor"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/rest/api/2/field" {
					t.Fatalf("request=%s %s", r.Method, r.URL.RequestURI())
				}
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			catalog, err := New(server.URL, "token", "test").ReadGuardedFieldCatalog(t.Context(), test.selected)
			if (err == nil) != test.ok {
				t.Fatalf("catalog=%+v err=%v", catalog, err)
			}
			wantRequests := int32(1)
			if test.name == "hostile reserved" {
				wantRequests = 0
			}
			if requests.Load() != wantRequests {
				t.Fatalf("requests=%d want=%d", requests.Load(), wantRequests)
			}
		})
	}
}

func TestGuardedFieldCatalogCardinalityBounds(t *testing.T) {
	for _, count := range []int{domain.JiraGuardedFieldMaxCatalogEntries, domain.JiraGuardedFieldMaxCatalogEntries + 1} {
		rows := make([]map[string]any, count)
		for index := range rows {
			rows[index] = map[string]any{"id": fmt.Sprintf("vendor.%d", index), "custom": true}
		}
		body, _ := json.Marshal(rows)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		_, err := New(server.URL, "token", "test").ReadGuardedFieldCatalog(t.Context(), []string{"vendor.0"})
		server.Close()
		if (err == nil) != (count == domain.JiraGuardedFieldMaxCatalogEntries) {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
}

func TestGuardedFieldIssueRequiresPresenceAndExactSortedQuery(t *testing.T) {
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.URL.RequestURI()
		_, _ = io.WriteString(w, `{"id":"10001","key":"PROJ-1","fields":{"project":{"key":"PROJ"},"updated":"2026-08-23T10:00:00.000+0000","plugin.vendor":{"n":9007199254740993},"customfield_1":null}}`)
	}))
	defer server.Close()
	issue, err := New(server.URL, "token", "test").ReadGuardedFieldIssue(t.Context(), "PROJ-1", []string{"plugin.vendor", "customfield_1"})
	if err != nil || !issue.Complete || !issue.Fields["customfield_1"].Present || issue.Fields["customfield_1"].Value != nil {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	number := issue.Fields["plugin.vendor"].Value.(map[string]any)["n"]
	if number != json.Number("9007199254740993") {
		t.Fatalf("number=%#v", number)
	}
	if requestURI != "/rest/api/2/issue/PROJ-1?fields=customfield_1%2Cplugin.vendor%2Cproject%2Cupdated" {
		t.Fatalf("request URI=%q", requestURI)
	}

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"10001","key":"PROJ-1","fields":{"project":{"key":"PROJ"},"updated":"2026-08-23T10:00:00.000+0000"}}`)
	}))
	defer missing.Close()
	if _, err := New(missing.URL, "token", "test").ReadGuardedFieldIssue(t.Context(), "PROJ-1", []string{"customfield_1"}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing field err=%v", err)
	}
}

func TestGuardedFieldPreparationIsDeterministicAndWriterUsesExactNumericPUT(t *testing.T) {
	qualified := []domain.JiraGuardedFieldCatalogEntry{{ID: "customfield_1", Custom: true}, {ID: "plugin.vendor", Custom: true}}
	valuesA := map[string]any{"plugin.vendor": map[string]any{"id": "2", "large": json.Number("9007199254740993")}, "customfield_1": "<>&\u2028\u2029"}
	valuesB := map[string]any{"customfield_1": "<>&\u2028\u2029", "plugin.vendor": map[string]any{"large": json.Number("9007199254740993"), "id": "2"}}
	adapter := New("https://jira.example.test", "token", "test")
	preparedA, err := adapter.PrepareGuardedFields(domain.JiraGuardedFieldPreparationRequest{Values: valuesA, Qualified: qualified})
	if err != nil {
		t.Fatal(err)
	}
	preparedB, err := adapter.PrepareGuardedFields(domain.JiraGuardedFieldPreparationRequest{Values: valuesB, Qualified: qualified})
	if err != nil || !bytes.Equal(preparedA.Payload, preparedB.Payload) || !reflect.DeepEqual(preparedA.Fields, preparedB.Fields) {
		t.Fatalf("preparations differ: A=%+v B=%+v err=%v", preparedA, preparedB, err)
	}
	if !bytes.Contains(preparedA.Payload, []byte(`\u003c\u003e\u0026\u2028\u2029`)) {
		t.Fatalf("HTML/line-separator escaping drifted: %s", preparedA.Payload)
	}

	var requests atomic.Int32
	var method, path string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		method, path = r.Method, r.URL.Path
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter = New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	budget, _ := domain.NewReadBudget(1, domain.JiraGuardedFieldMaxWriteResponseBytes)
	err = adapter.WriteGuardedFields(domain.WithReadBudget(context.Background(), budget), domain.JiraGuardedFieldWrite{
		ID: "10001", Key: "PROJ-1", Project: "PROJ", Qualified: qualified, Prepared: preparedA,
	})
	if err != nil || requests.Load() != 1 || len(authorizer.requests) != 1 || method != http.MethodPut || path != "/rest/api/2/issue/10001" || !bytes.Equal(body, preparedA.Payload) {
		t.Fatalf("err=%v requests=%d auth=%d method/path=%s %s body=%s", err, requests.Load(), len(authorizer.requests), method, path, body)
	}
}

func TestGuardedFieldPreparationAndWriterEnforceReleasedValueEnvelopeDepth(t *testing.T) {
	nested := func(depth int) any {
		var value any = json.Number("0")
		for range depth {
			value = []any{value}
		}
		return value
	}
	qualified := []domain.JiraGuardedFieldCatalogEntry{{ID: "plugin.vendor", Custom: true}}
	adapter := New("https://jira.example.test", "token", "test")
	prepared, err := adapter.PrepareGuardedFields(domain.JiraGuardedFieldPreparationRequest{
		Values: map[string]any{"plugin.vendor": nested(domain.JiraGuardedFieldMaxValueNestingDepth)}, Qualified: qualified,
	})
	if err != nil || len(prepared.Payload) == 0 || len(prepared.Fields) != 1 {
		t.Fatalf("exact-depth preparation=%+v err=%v", prepared, err)
	}
	if err := validateGuardedFieldWrite(domain.JiraGuardedFieldWrite{ID: "10001", Key: "PROJ-1", Project: "PROJ", Qualified: qualified, Prepared: prepared}); err != nil {
		t.Fatalf("exact-depth writer validation: %v", err)
	}
	if _, err := adapter.PrepareGuardedFields(domain.JiraGuardedFieldPreparationRequest{
		Values: map[string]any{"plugin.vendor": nested(domain.JiraGuardedFieldMaxValueNestingDepth + 1)}, Qualified: qualified,
	}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("over-depth preparation err=%v", err)
	}
}

func TestGuardedFieldWriterRejectsReservedAndDriftBeforeAuthorization(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	for _, write := range []domain.JiraGuardedFieldWrite{
		{ID: "10001", Key: "PROJ-1", Project: "PROJ", Qualified: []domain.JiraGuardedFieldCatalogEntry{{ID: "project", Custom: true}}, Prepared: domain.JiraGuardedFieldPreparation{Payload: []byte(`{"fields":{"project":{"key":"OTHER"}}}`), Fields: []domain.JiraGuardedFieldPreparedProjection{{FieldID: "project", Kind: "object", Bytes: 15, SHA256: strings.Repeat("0", 64)}}}},
		{ID: "10001", Key: "PROJ-1", Project: "PROJ", Qualified: []domain.JiraGuardedFieldCatalogEntry{{ID: "customfield_1", Custom: true}}, Prepared: domain.JiraGuardedFieldPreparation{Payload: []byte(`{"fields":{"customfield_1":"x"}}`), Fields: []domain.JiraGuardedFieldPreparedProjection{{FieldID: "customfield_1", Kind: "string", Bytes: 1, SHA256: strings.Repeat("0", 64)}}}},
	} {
		err := adapter.WriteGuardedFields(t.Context(), write)
		var diagnostic interface{ DiagnosticWriteAttempted() bool }
		if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &diagnostic) || diagnostic.DiagnosticWriteAttempted() || len(authorizer.requests) != 0 || requests.Load() != 0 {
			t.Fatalf("err=%v auth=%d requests=%d", err, len(authorizer.requests), requests.Load())
		}
	}
}

func TestGuardedFieldIdentifierAndCardinalityBounds(t *testing.T) {
	accepted := strings.Repeat("x", domain.JiraGuardedFieldMaxIDBytes)
	if _, err := guardedFieldIDs([]string{accepted}, 1); err != nil {
		t.Fatalf("1024-byte id: %v", err)
	}
	if _, err := guardedFieldIDs([]string{accepted + "x"}, 1); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("1025-byte id: %v", err)
	}
	maximum := make([]string, domain.JiraGuardedFieldMaxSelected)
	for index := range maximum {
		maximum[index] = fmt.Sprintf("vendor.%d", index)
	}
	if _, err := guardedFieldIDs(maximum, domain.JiraGuardedFieldMaxSelected); err != nil {
		t.Fatalf("1024 fields: %v", err)
	}
	if _, err := guardedFieldIDs(append(maximum, "vendor.extra"), domain.JiraGuardedFieldMaxSelected); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("1025 fields: %v", err)
	}
}
