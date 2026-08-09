package jira

import (
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

func TestReadIssueSnapshotUsesOneExactIssueExpansion(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/2/issue/PROJ-7" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("fields"); got != "*all" {
			t.Fatalf("fields = %q", got)
		}
		if got := r.URL.Query().Get("properties"); got != "*all" {
			t.Fatalf("properties = %q", got)
		}
		if got := r.URL.Query().Get("expand"); got != "names,schema" {
			t.Fatalf("expand = %q", got)
		}
		_, _ = w.Write([]byte(`{
			"id":"10007","key":"PROJ-7",
			"fields":{"summary":"Graph seed","customfield_10":"PROJ-8"},
			"names":{"summary":"Summary","customfield_10":"Related work"},
			"schema":{"summary":{"type":"string","system":"summary"},"customfield_10":{"type":"string","custom":"example:key"}},
			"properties":{"example":{"pageId":123}}
		}`))
	}))
	defer server.Close()

	snapshot, err := New(server.URL, "token", "test").ReadIssueSnapshot(context.Background(), " PROJ-7 ")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if snapshot.RequestedKey != "PROJ-7" || snapshot.Key != "PROJ-7" || snapshot.ID != "10007" {
		t.Fatalf("identity = %#v", snapshot)
	}
	if snapshot.Issue.Summary != "Graph seed" || snapshot.Names["customfield_10"] != "Related work" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Schema["customfield_10"].Custom != "example:key" {
		t.Fatalf("schema = %#v", snapshot.Schema)
	}
	property, ok := snapshot.Properties["example"].(map[string]any)
	pageID, numberOK := property["pageId"].(json.Number)
	if !ok || !numberOK || pageID.String() != "123" {
		t.Fatalf("properties = %#v", snapshot.Properties)
	}
}

func TestReadIssueRemoteLinksMapsContentMinimizedProjection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/2/issue/PROJ-7/remotelink" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{
			"id":"9","globalId":"system=example&id=4","relationship":"documents",
			"application":{"type":"com.example.docs","name":"Docs"},
			"object":{"url":"https://docs.example.test/pages/4","title":"Design","summary":"Bounded",
				"status":{"resolved":true,"icon":{"title":"Done"}}},
			"self":"https://jira.example.test/rest/api/2/issue/PROJ-7/remotelink/9"
		}]`))
	}))
	defer server.Close()

	inventory, err := New(server.URL, "token", "test").ReadIssueRemoteLinks(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatal(err)
	}
	links := inventory.Links
	if len(links) != 1 {
		t.Fatalf("links = %#v", links)
	}
	link := links[0]
	if link.ID != "9" || link.Relationship != "documents" || link.ObjectURL != "https://docs.example.test/pages/4" ||
		link.GlobalID != "system=example&id=4" || link.ApplicationType != "com.example.docs" {
		t.Fatalf("link = %#v", link)
	}
}

func TestReadIssueSnapshotRejectsMissingRequestedSections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"1","key":"PROJ-1"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "token", "test").ReadIssueSnapshot(context.Background(), "PROJ-1")
	if err == nil {
		t.Fatal("expected missing-section failure")
	}
}

func TestReadIssueSnapshotIsSingleAttempt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "temporary backend prose", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := New(server.URL, "token", "test").ReadIssueSnapshot(context.Background(), "PROJ-1")
	if err == nil || requests != 1 {
		t.Fatalf("error = %v, requests = %d", err, requests)
	}
}

func TestReadIssueRemoteLinksRejectsUnsafeAndDuplicateRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"1","object":{"url":"https://docs.example.test/one"}},
			{"id":"1","object":{"url":"https://docs.example.test/duplicate"}},
			{"id":"2","object":{"url":"https://user:secret@docs.example.test/two"}},
			{"id":"bad","object":{"url":"https://docs.example.test/three"}}
		]`))
	}))
	defer server.Close()

	inventory, err := New(server.URL, "token", "test").ReadIssueRemoteLinks(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Total != 4 || inventory.Unsupported != 3 || len(inventory.Links) != 1 {
		t.Fatalf("inventory = %#v", inventory)
	}
}

func TestReadIssueRemoteLinksRejectsUnboundedOrControlStructuredMetadata(t *testing.T) {
	longGlobalID := strings.Repeat("g", jiraRemoteLinkMaxGlobalIDBytes+1)
	longApplicationType := strings.Repeat("a", jiraRemoteLinkMaxApplicationTypeBytes+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[
			{"id":"1","object":{"url":"https://docs.example.test/one"}},
			{"id":"2","globalId":%q,"object":{"url":"https://docs.example.test/two"}},
			{"id":"3","application":{"type":%q},"object":{"url":"https://docs.example.test/three"}},
			{"id":"4","application":{"type":"example\u0001type"},"object":{"url":"https://docs.example.test/four"}},
			{"id":"5","application":"not-an-object","object":{"url":"https://docs.example.test/five"}}
		]`, longGlobalID, longApplicationType)
	}))
	defer server.Close()

	inventory, err := New(server.URL, "token", "test").ReadIssueRemoteLinks(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Total != 5 || inventory.Unsupported != 4 || len(inventory.Links) != 1 {
		t.Fatalf("inventory = %#v", inventory)
	}
	if inventory.Links[0].GlobalID != "" || inventory.Links[0].ApplicationType != "" {
		t.Fatalf("empty metadata should remain allowed: %#v", inventory.Links[0])
	}
}

func TestReadIssueRemoteLinksMarksInvalidUTF8MetadataUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"5","globalId":"`))
		_, _ = w.Write([]byte{0xff})
		_, _ = w.Write([]byte(`","object":{"url":"https://docs.example.test/five"}}]`))
	}))
	defer server.Close()

	inventory, err := New(server.URL, "token", "test").ReadIssueRemoteLinks(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Total != 1 || inventory.Unsupported != 1 || len(inventory.Links) != 0 {
		t.Fatalf("inventory = %#v", inventory)
	}
}

func TestReadIssueRemoteLinksRejectsNullCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}))
	defer server.Close()

	_, err := New(server.URL, "token", "test").ReadIssueRemoteLinks(context.Background(), "PROJ-1")
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v", err)
	}
}
