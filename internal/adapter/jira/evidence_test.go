package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestListJiraCommentsQualifiedPaginatesAndMapsStableEvidence(t *testing.T) {
	var starts []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/rest/api/2/issue/10001/comment" {
			t.Fatalf("path=%q", request.URL.Path)
		}
		starts = append(starts, request.URL.Query().Get("startAt"))
		writer.Header().Set("Content-Type", "application/json")
		if len(starts) == 1 {
			_, _ = writer.Write([]byte(`{"startAt":0,"total":2,"comments":[{"id":"1","author":{"name":"user","key":"stable","displayName":"Fixture"},"created":"2026-01-01","updated":"2026-01-02","parentId":"0","body":"one"}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"startAt":1,"total":2,"comments":[{"id":"2","body":"two"}]}`))
	}))
	t.Cleanup(server.Close)

	inventory, err := newTestJira(server).ListJiraCommentsQualified(t.Context(), "10001", domain.JiraCommentReadOptions{MaxPages: 2, MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Complete || inventory.Total != 2 || !inventory.TotalKnown || inventory.PageCount != 2 ||
		len(inventory.Comments) != 2 || inventory.Comments[0].AuthorKey != "stable" ||
		inventory.Comments[0].Updated != "2026-01-02" || inventory.Comments[0].ParentID != "0" {
		t.Fatalf("inventory=%+v", inventory)
	}
	if !reflect.DeepEqual(starts, []string{"0", "1"}) {
		t.Fatalf("starts=%v", starts)
	}
}

func TestListJiraCommentsQualifiedReportsClosedBounds(t *testing.T) {
	tests := []struct {
		name       string
		options    domain.JiraCommentReadOptions
		response   string
		wantReason string
	}{
		{"page limit", domain.JiraCommentReadOptions{MaxPages: 1, MaxItems: 2}, `{"startAt":0,"total":2,"comments":[{"id":"1","body":"one"}]}`, domain.JiraCommentPartialPageLimit},
		{"item limit", domain.JiraCommentReadOptions{MaxPages: 2, MaxItems: 1}, `{"startAt":0,"total":2,"comments":[{"id":"1","body":"one"}]}`, domain.JiraCommentPartialItemLimit},
		{"stalled", domain.JiraCommentReadOptions{MaxPages: 2, MaxItems: 2}, `{"startAt":0,"total":1,"comments":[]}`, domain.JiraCommentPartialPaginationStalled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.response))
			}))
			t.Cleanup(server.Close)
			inventory, err := newTestJira(server).ListJiraCommentsQualified(t.Context(), "10001", test.options)
			if err != nil || inventory.Complete || inventory.PartialReason != test.wantReason {
				t.Fatalf("inventory=%+v error=%v", inventory, err)
			}
		})
	}
}

func TestListJiraCommentsQualifiedRejectsMalformedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"startAt":0,"total":2,"comments":[{"id":"1","body":"one"},{"id":"1","body":"two"}]}`))
	}))
	t.Cleanup(server.Close)
	inventory, err := newTestJira(server).ListJiraCommentsQualified(t.Context(), "10001", domain.JiraCommentReadOptions{MaxPages: 1, MaxItems: 2})
	if !errors.Is(err, domain.ErrCheckFailed) || inventory.Comments != nil {
		t.Fatalf("inventory=%+v error=%v", inventory, err)
	}
}

func TestListJiraAttachmentsQualifiedMapsExactField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/rest/api/2/issue/10001" || request.URL.Query().Get("fields") != "attachment" {
			t.Fatalf("request=%s?%s", request.URL.Path, request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"10001","fields":{"attachment":[{"id":"7","filename":"a.bin","mimeType":"application/octet-stream","size":3,"created":"2026-01-01","content":"/secure/attachment/7/a.bin","author":{"name":"user","key":"stable","displayName":"Fixture"}}]}}`))
	}))
	t.Cleanup(server.Close)
	inventory, err := newTestJira(server).ListJiraAttachmentsQualified(t.Context(), "10001")
	if err != nil || !inventory.Complete || len(inventory.Attachments) != 1 {
		t.Fatalf("inventory=%+v error=%v", inventory, err)
	}
	attachment := inventory.Attachments[0]
	if attachment.ID != "7" || attachment.AuthorKey != "stable" || attachment.Created != "2026-01-01" || attachment.DownPath == "" {
		t.Fatalf("attachment=%+v", attachment)
	}
}

func TestListJiraAttachmentsQualifiedDistinguishesUnavailableField(t *testing.T) {
	for _, fields := range []string{`{}`, `{"attachment":null}`} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"10001","fields":` + fields + `}`))
		}))
		inventory, err := newTestJira(server).ListJiraAttachmentsQualified(context.Background(), "10001")
		server.Close()
		if err != nil || inventory.Complete || inventory.Attachments == nil || inventory.PartialReason != domain.JiraAttachmentPartialFieldUnavailable {
			t.Fatalf("fields=%s inventory=%+v error=%v", fields, inventory, err)
		}
	}
}

func TestListJiraAttachmentsQualifiedRejectsParentAndDuplicateIdentity(t *testing.T) {
	for name, response := range map[string]string{
		"parent":    `{"id":"10002","fields":{"attachment":[]}}`,
		"duplicate": `{"id":"10001","fields":{"attachment":[{"id":"7","filename":"a","size":0},{"id":"7","filename":"b","size":0}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(response))
			}))
			t.Cleanup(server.Close)
			inventory, err := newTestJira(server).ListJiraAttachmentsQualified(t.Context(), "10001")
			if !errors.Is(err, domain.ErrCheckFailed) || inventory.Attachments != nil {
				t.Fatalf("inventory=%+v error=%v", inventory, err)
			}
		})
	}
}
