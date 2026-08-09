package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestSelectInverseReferencePageUsesQualifiedJQLAndRawCoordinates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/rest/api/2/search" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		query := request.URL.Query()
		if got, want := query.Get("jql"), "project = INV ORDER BY key ASC"; got != want {
			t.Fatalf("jql = %q, want %q", got, want)
		}
		if got, want := query.Get("startAt"), "4"; got != want {
			t.Fatalf("startAt = %q, want %q", got, want)
		}
		if got, want := query.Get("maxResults"), "2"; got != want {
			t.Fatalf("maxResults = %q, want %q", got, want)
		}
		if got, want := query.Get("fields"), "key"; got != want {
			t.Fatalf("fields = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"startAt":4,"maxResults":2,"total":9,"issues":[{"id":"10004","key":"INV-4","fields":{"summary":"not retained"}},{"id":"10005","key":"INV-5"}]}`))
	}))
	defer server.Close()

	page, err := New(server.URL, "token", "test").SelectInverseReferencePage(context.Background(), domain.JiraInverseReferenceSelection{
		JQL: "project = INV ORDER BY key ASC", StartAt: 4, MaxResults: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.StartAt != 4 || page.MaxResults != 2 || page.Total != 9 {
		t.Fatalf("page coordinates = %#v", page)
	}
	if got, want := page.Issues, []domain.JiraInverseReferenceIssueIdentity{{ID: "10004", Key: "INV-4"}, {ID: "10005", Key: "INV-5"}}; !equalInverseIssueIdentities(got, want) {
		t.Fatalf("issues = %#v, want %#v", got, want)
	}
}

func TestSelectInverseReferencePageDistinguishesMissingCollectionFromEmpty(t *testing.T) {
	for _, test := range []struct {
		name      string
		response  string
		wantError bool
	}{
		{name: "omitted", response: `{"startAt":0,"maxResults":50,"total":0}`, wantError: true},
		{name: "null", response: `{"startAt":0,"maxResults":50,"total":0,"issues":null}`, wantError: true},
		{name: "empty", response: `{"startAt":0,"maxResults":50,"total":0,"issues":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			page, err := New(server.URL, "token", "test").SelectInverseReferencePage(context.Background(), domain.JiraInverseReferenceSelection{
				JQL: "project = INV ORDER BY key ASC", MaxResults: 50,
			})
			if test.wantError {
				if !errors.Is(err, domain.ErrCheckFailed) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if page.Issues == nil || len(page.Issues) != 0 {
				t.Fatalf("issues = %#v, want non-nil empty collection", page.Issues)
			}
		})
	}
}

func TestReadInverseReferenceSnapshotKeepsMissingAndNullDistinct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/rest/api/2/issue/INV-4" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		query := request.URL.Query()
		if got, want := query.Get("fields"), "customfield_1,customfield_2"; got != want {
			t.Fatalf("fields = %q, want %q", got, want)
		}
		if got, want := query.Get("properties"), "*all"; got != want {
			t.Fatalf("properties = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"id":"10004","key":"INV-4","fields":{"customfield_2":null},"properties":{"zeta":{"id":"44"},"alpha":null}}`))
	}))
	defer server.Close()

	snapshot, err := New(server.URL, "token", "test").ReadInverseReferenceSnapshot(context.Background(), domain.JiraInverseReferenceSnapshotRequest{
		Issue:    domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"},
		FieldIDs: []string{"customfield_1", "customfield_2"}, IncludeProperties: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Fields) != 2 || snapshot.Fields[0].FieldID != "customfield_1" || snapshot.Fields[0].Present {
		t.Fatalf("missing field = %#v", snapshot.Fields)
	}
	if field := snapshot.Fields[1]; field.FieldID != "customfield_2" || !field.Present || !bytes.Equal(field.Value, []byte("null")) {
		t.Fatalf("null field = %#v", field)
	}
	if got, want := snapshot.Properties, []domain.JiraInverseReferencePropertySnapshot{{Key: "alpha", Value: []byte("null")}, {Key: "zeta", Value: []byte(`{"id":"44"}`)}}; !equalInverseProperties(got, want) {
		t.Fatalf("properties = %#v, want %#v", got, want)
	}
}

func TestReadInverseReferenceSnapshotLeavesPropertiesUnrequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("fields"); got != "key" {
			t.Fatalf("fields = %q, want key", got)
		}
		if got := request.URL.Query().Get("properties"); got != "" {
			t.Fatalf("properties = %q, want absent", got)
		}
		_, _ = w.Write([]byte(`{"id":"10004","key":"INV-4","fields":{}}`))
	}))
	defer server.Close()

	snapshot, err := New(server.URL, "token", "test").ReadInverseReferenceSnapshot(context.Background(), domain.JiraInverseReferenceSnapshotRequest{
		Issue: domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"}, FieldIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Properties != nil {
		t.Fatalf("properties = %#v, want nil", snapshot.Properties)
	}
}

func TestReadInverseReferenceSnapshotAllowsExplicitFieldsPlusDescription(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if got := len(strings.Split(request.URL.Query().Get("fields"), ",")); got != inverseReferenceMaxFieldIDs {
			t.Fatalf("field count = %d, want %d", got, inverseReferenceMaxFieldIDs)
		}
		_, _ = w.Write([]byte(`{"id":"10004","key":"INV-4","fields":{}}`))
	}))
	defer server.Close()

	fieldIDs := make([]string, 0, inverseReferenceMaxFieldIDs)
	for index := range inverseReferenceMaxFieldIDs - 1 {
		fieldIDs = append(fieldIDs, fmt.Sprintf("customfield_%03d", index))
	}
	fieldIDs = append(fieldIDs, "description")
	adapter := New(server.URL, "token", "test")
	if _, err := adapter.ReadInverseReferenceSnapshot(context.Background(), domain.JiraInverseReferenceSnapshotRequest{
		Issue: domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"}, FieldIDs: fieldIDs,
	}); err != nil {
		t.Fatal(err)
	}
	fieldIDs = append(fieldIDs, "one_field_too_many")
	if _, err := adapter.ReadInverseReferenceSnapshot(context.Background(), domain.JiraInverseReferenceSnapshotRequest{
		Issue: domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"}, FieldIDs: fieldIDs,
	}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("overflow error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("backend requests = %d, want 1", requests)
	}
}

func TestInverseReferenceReadsRespectReadBudget(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"startAt":0,"maxResults":1,"total":0,"issues":[]}`))
	}))
	defer server.Close()
	budget, err := domain.NewReadBudget(0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(server.URL, "token", "test").SelectInverseReferencePage(domain.WithReadBudget(context.Background(), budget), domain.JiraInverseReferenceSelection{JQL: "project = INV ORDER BY key ASC", MaxResults: 1})
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) || requests != 0 {
		t.Fatalf("error = %v, requests = %d", err, requests)
	}
}

func TestInverseReferenceSnapshotRejectsResponseBudgetOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"10004","key":"INV-4","fields":{"customfield_1":"this value exceeds the synthetic response budget"}}`))
	}))
	defer server.Close()
	budget, err := domain.NewReadBudget(1, 32)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(server.URL, "token", "test").ReadInverseReferenceSnapshot(domain.WithReadBudget(context.Background(), budget), domain.JiraInverseReferenceSnapshotRequest{
		Issue: domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"}, FieldIDs: []string{"customfield_1"},
	})
	if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadInverseReferenceSnapshotRejectsUnboundedOrControlIdentifiers(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"id":"10004","key":"INV-4","fields":{}}`))
	}))
	defer server.Close()
	adapter := New(server.URL, "token", "test")
	for _, fieldID := range []string{strings.Repeat("f", inverseReferenceMaxFieldIDBytes+1), "customfield_1\x01", string([]byte{'f', 0xff}), "*all"} {
		_, err := adapter.ReadInverseReferenceSnapshot(context.Background(), domain.JiraInverseReferenceSnapshotRequest{
			Issue: domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"}, FieldIDs: []string{fieldID},
		})
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("field id %q error = %v", fieldID, err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid field ids reached backend %d times", requests)
	}
}

func TestReadInverseReferenceSnapshotRejectsInvalidUTF8PropertyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"10004","key":"INV-4","fields":{},"properties":{"`))
		_, _ = w.Write([]byte{0xff})
		_, _ = w.Write([]byte(`":"value"}}`))
	}))
	defer server.Close()
	_, err := New(server.URL, "token", "test").ReadInverseReferenceSnapshot(context.Background(), domain.JiraInverseReferenceSnapshotRequest{
		Issue: domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"}, IncludeProperties: true,
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadInverseReferenceSnapshotRejectsInvalidUTF8Values(t *testing.T) {
	for _, test := range []struct {
		response          []byte
		fieldIDs          []string
		includeProperties bool
	}{
		{[]byte("{\"id\":\"10004\",\"key\":\"INV-4\",\"fields\":{\"customfield_1\":\""), []string{"customfield_1"}, false},
		{[]byte("{\"id\":\"10004\",\"key\":\"INV-4\",\"fields\":{},\"properties\":{\"safe\":\""), nil, true},
	} {
		response := test.response
		response = append(response, 0xff)
		response = append(response, []byte("\"}}")...)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(response)
		}))
		request := domain.JiraInverseReferenceSnapshotRequest{
			Issue:    domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"},
			FieldIDs: test.fieldIDs, IncludeProperties: test.includeProperties,
		}
		_, err := New(server.URL, "token", "test").ReadInverseReferenceSnapshot(context.Background(), request)
		server.Close()
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestReadInverseReferenceSnapshotRejectsUnboundedOrControlPropertyKeys(t *testing.T) {
	for _, key := range []string{strings.Repeat("p", inverseReferenceMaxPropertyKeyBytes+1), "property\x01key"} {
		t.Run("adversarial key", func(t *testing.T) {
			encodedKey, err := json.Marshal(key)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"id":"10004","key":"INV-4","fields":{},"properties":{%s:"value"}}`, encodedKey)
			}))
			defer server.Close()
			_, err = New(server.URL, "token", "test").ReadInverseReferenceSnapshot(context.Background(), domain.JiraInverseReferenceSnapshotRequest{
				Issue: domain.JiraInverseReferenceIssueIdentity{ID: "10004", Key: "INV-4"}, IncludeProperties: true,
			})
			if !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(err.Error(), key) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func equalInverseIssueIdentities(got, want []domain.JiraInverseReferenceIssueIdentity) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func equalInverseProperties(got, want []domain.JiraInverseReferencePropertySnapshot) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].Key != want[index].Key || !bytes.Equal(got[index].Value, want[index].Value) {
			return false
		}
	}
	return true
}
