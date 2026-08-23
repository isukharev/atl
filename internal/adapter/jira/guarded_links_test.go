package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestStrictGuardedLinkReadsAndImmutableWriteWire(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issueLinkType":
			_, _ = io.WriteString(w, `{"issueLinkTypes":[{"id":"7","name":"Blocks","inward":"is blocked by","outward":"blocks"}]}`)
		case "/rest/api/2/issue/APP-1":
			if r.URL.RawQuery != "fields=project,issuelinks,updated" {
				t.Errorf("endpoint query=%q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"id":"10","key":"APP-1","fields":{"project":{"key":"APP"},"issuelinks":[],"updated":"2026-08-23T10:00:00Z"}}`)
		default:
			method, path = r.Method, r.URL.Path
			data, _ := io.ReadAll(r.Body)
			body = string(data)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	ctx := domain.WithSingleAttempt(context.Background())
	catalog, err := adapter.ReadStrictLinkTypes(ctx)
	if err != nil || !catalog.Complete || len(catalog.Types) != 1 || catalog.Types[0].ID != "7" {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	endpoint, err := adapter.ReadStrictLinkEndpoint(ctx, "APP-1")
	if err != nil || endpoint.ID != "10" || endpoint.Project != "APP" || !endpoint.Complete {
		t.Fatalf("endpoint=%+v err=%v", endpoint, err)
	}
	write := domain.JiraGuardedLinkWrite{TypeID: "7", Outward: endpoint, Inward: domain.JiraStrictLinkEndpoint{ID: "20", Key: "OPS-2", Project: "OPS", Complete: true}}
	if err := adapter.AddGuardedLink(ctx, write); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/api/2/issueLink" || body != `{"type":{"id":"7"},"inwardIssue":{"id":"20"},"outwardIssue":{"id":"10"}}` {
		t.Fatalf("wire=%s %s %s", method, path, body)
	}
	wantTargets := []domain.WriteTarget{{Service: "jira", Kind: "link", Project: "APP", Key: "APP-1"}, {Service: "jira", Kind: "link", Project: "OPS", Key: "OPS-2"}}
	if len(authorizer.requests) != 1 || !reflect.DeepEqual(authorizer.requests[0].Targets, wantTargets) || !reflect.DeepEqual(authorizer.requests[0].Verbs, domain.WriteVerbSet{domain.WriteVerbUpdate}) {
		t.Fatalf("authorization=%+v", authorizer.requests)
	}
	if err := adapter.DeleteGuardedLink(ctx, domain.JiraGuardedLinkWrite{TypeID: "7", Outward: endpoint, Inward: write.Inward, LinkID: "90"}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/api/2/issueLink/90" {
		t.Fatalf("delete wire=%s %s", method, path)
	}
}

func TestStrictGuardedLinkDecoderRejectsIncompleteAndDuplicateEvidence(t *testing.T) {
	const prefix = `{"id":"10","key":"APP-1","fields":{"project":{"key":"APP"},"issuelinks":[{"id":"90","type":{"id":"7","name":"Blocks","inward":"is blocked by","outward":"blocks"},`
	const suffix = `}]}}`
	responses := map[string]string{
		"missing inventory":  `{"id":"10","key":"APP-1","fields":{"project":{"key":"APP"}}}`,
		"duplicate id":       `{"id":"10","key":"APP-1","fields":{"project":{"key":"APP"},"issuelinks":[{"id":"90","type":{"id":"7","name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":{"id":"20","key":"OPS-2"}},{"id":"90","type":{"id":"7","name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":{"id":"20","key":"OPS-2"}}]}}`,
		"false counterpart":  prefix + `"inwardIssue":{"id":"20","key":"OPS-2"},"outwardIssue":false` + suffix,
		"null counterpart":   prefix + `"inwardIssue":{"id":"20","key":"OPS-2"},"outwardIssue":null` + suffix,
		"string counterpart": prefix + `"inwardIssue":{"id":"20","key":"OPS-2"},"outwardIssue":"bad"` + suffix,
		"array counterpart":  prefix + `"inwardIssue":{"id":"20","key":"OPS-2"},"outwardIssue":[]` + suffix,
		"empty object":       prefix + `"inwardIssue":{}` + suffix,
		"dual objects":       prefix + `"inwardIssue":{"id":"20","key":"OPS-2"},"outwardIssue":{"id":"20","key":"OPS-2"}` + suffix,
		"missing direction":  prefix[:len(prefix)-1] + suffix,
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			_, err := New(server.URL, "token", "test").ReadStrictLinkEndpoint(domain.WithSingleAttempt(context.Background()), "APP-1")
			if err == nil {
				t.Fatalf("response unexpectedly qualified: %s", response)
			}
		})
	}
}

func TestStrictGuardedLinkDecoderRejectsLossyJSONEvidence(t *testing.T) {
	tests := map[string]struct {
		path     string
		response string
		read     func(*Jira) error
	}{
		"duplicate catalog member": {
			path:     "/rest/api/2/issueLinkType",
			response: `{"issueLinkTypes":[{"id":"7","id":"8","name":"Blocks","inward":"is blocked by","outward":"blocks"}]}`,
			read: func(adapter *Jira) error {
				_, err := adapter.ReadStrictLinkTypes(domain.WithSingleAttempt(t.Context()))
				return err
			},
		},
		"unpaired surrogate in endpoint inventory": {
			path:     "/rest/api/2/issue/APP-1",
			response: `{"id":"10","key":"APP-1","fields":{"project":{"key":"APP"},"issuelinks":[{"id":"90","type":{"id":"7","name":"\ud800","inward":"is blocked by","outward":"blocks"},"inwardIssue":{"id":"20","key":"OPS-2"}}],"updated":"2026-08-23T10:00:00Z"}}`,
			read: func(adapter *Jira) error {
				_, err := adapter.ReadStrictLinkEndpoint(domain.WithSingleAttempt(t.Context()), "APP-1")
				return err
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					t.Fatalf("path=%q want=%q", r.URL.Path, test.path)
				}
				_, _ = io.WriteString(w, test.response)
			}))
			t.Cleanup(server.Close)
			if err := test.read(New(server.URL, "token", "test")); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestStrictGuardedLinkEndpointRequiresQualifiedUpdated(t *testing.T) {
	for name, updated := range map[string]string{
		"missing": "", "null": `,"updated":null`, "structured": `,"updated":{}`, "malformed": `,"updated":"yesterday"`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"id":"10","key":"APP-1","fields":{"project":{"key":"APP"},"issuelinks":[]` + updated + `}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			t.Cleanup(server.Close)
			if _, err := New(server.URL, "token", "test").ReadStrictLinkEndpoint(domain.WithSingleAttempt(t.Context()), "APP-1"); err == nil {
				t.Fatal("endpoint without qualified updated unexpectedly accepted")
			}
		})
	}
}

func TestStrictGuardedLinkDecoderRejectsOversizedCatalogAndInventory(t *testing.T) {
	types := make([]strictLinkTypeDTO, jiraGuardedLinkTypeMaxItems+1)
	for i := range types {
		types[i] = strictLinkTypeDTO{ID: strconv.Itoa(i + 1), Name: "Type", Inward: "inward", Outward: "outward"}
	}
	catalogBody, _ := json.Marshal(map[string]any{"issueLinkTypes": types})
	rows := make([]strictLinkRowDTO, jiraGuardedLinkMaxItems+1)
	for i := range rows {
		rows[i] = strictLinkRowDTO{ID: strconv.Itoa(i + 1), Type: strictLinkTypeDTO{ID: "7", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"}, Inward: json.RawMessage(`{"id":"20","key":"OPS-2"}`)}
	}
	endpointBody, _ := json.Marshal(map[string]any{"id": "10", "key": "APP-1", "fields": map[string]any{"project": map[string]string{"key": "APP"}, "issuelinks": rows}})
	for name, body := range map[string][]byte{"catalog": catalogBody, "inventory": endpointBody} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
			defer server.Close()
			adapter := New(server.URL, "token", "test")
			var err error
			if name == "catalog" {
				_, err = adapter.ReadStrictLinkTypes(domain.WithSingleAttempt(t.Context()))
			} else {
				_, err = adapter.ReadStrictLinkEndpoint(domain.WithSingleAttempt(t.Context()), "APP-1")
			}
			if err == nil {
				t.Fatal("oversized response unexpectedly qualified")
			}
		})
	}
	if guardedLinkText(strings.Repeat("x", jiraGuardedLinkStringBytes+1)) || guardedLinkID("01") {
		t.Fatal("per-value caps accepted oversized text or noncanonical id")
	}
}

func TestStrictGuardedLinkWriteIsSingleAttemptNoRedirectAndChargesErrorBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, "bad")
	}))
	defer server.Close()
	budget, err := domain.NewReadBudget(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithSingleAttempt(domain.WithReadBudget(t.Context(), budget))
	endpoint := domain.JiraStrictLinkEndpoint{ID: "10", Key: "APP-1", Project: "APP", Complete: true}
	other := domain.JiraStrictLinkEndpoint{ID: "20", Key: "OPS-2", Project: "OPS", Complete: true}
	err = New(server.URL, "token", "test").AddGuardedLink(ctx, domain.JiraGuardedLinkWrite{TypeID: "7", Outward: endpoint, Inward: other})
	if err == nil || requests.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
	if got := budget.Usage(); got != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 3}) {
		t.Fatalf("budget=%+v", got)
	}
}
