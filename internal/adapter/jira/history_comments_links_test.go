package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// Older DC servers fall back from the paginated sub-resource to the embedded
// expansion and retain its explicit completeness metadata.
func TestChangelogFallsBackToExpansionAndMaps(t *testing.T) {
	var gotPath, gotExpand, gotFields string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/changelog") {
			http.NotFound(w, r)
			return
		}
		gotPath, gotExpand, gotFields = r.URL.Path, r.URL.Query().Get("expand"), r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"key":"PROJ-1",
			"changelog":{"startAt":0,"maxResults":20,"total":1,"histories":[
				{"id":"10100","author":{"displayName":"Alice","name":"alice"},"created":"2026-01-01T10:00:00.000+0000",
				 "items":[{"field":"Status","fieldId":"status","fromString":"To Do","toString":"In Progress"},
				          {"field":"assignee","fromString":"","toString":"alice"}]}
			]}}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	snapshot, err := j.CompleteChangelog(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("CompleteChangelog: %v", err)
	}
	if gotPath != "/rest/api/2/issue/PROJ-1" {
		t.Errorf("path = %q, want /rest/api/2/issue/PROJ-1", gotPath)
	}
	if gotExpand != "changelog" {
		t.Errorf("expand = %q, want changelog", gotExpand)
	}
	if gotFields != "summary" {
		t.Errorf("fields = %q, want summary (payload-size optimization)", gotFields)
	}
	if !snapshot.Complete || snapshot.Source != "embedded" || snapshot.Total != 1 || len(snapshot.Entries) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	e := snapshot.Entries[0]
	if e.ID != "10100" || e.Author != "Alice" || len(e.Items) != 2 {
		t.Fatalf("entry mismatch: %+v", e)
	}
	if e.Items[0].Field != "Status" || e.Items[0].FieldID != "status" || e.Items[0].From != "To Do" || e.Items[0].To != "In Progress" {
		t.Errorf("item[0] mismatch: %+v", e.Items[0])
	}
}

func TestCompleteChangelogPaginates(t *testing.T) {
	var starts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		starts = append(starts, r.URL.Query().Get("startAt"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("startAt") == "0" {
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":1,"total":2,"values":[{"id":"1","created":"2026-01-01","items":[]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"startAt":1,"maxResults":1,"total":2,"values":[{"id":"2","created":"2026-01-02","items":[]}]}`))
	}))
	defer srv.Close()

	snapshot, err := newTestJira(srv).CompleteChangelog(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || snapshot.Source != "paginated" || len(snapshot.Entries) != 2 || strings.Join(starts, ",") != "0,1" {
		t.Fatalf("snapshot=%+v starts=%v", snapshot, starts)
	}
}

func TestCompleteChangelogMarksNoProgressPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"startAt":0,"maxResults":100,"total":2,"values":[]}`))
	}))
	defer srv.Close()

	snapshot, err := newTestJira(srv).CompleteChangelog(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete || snapshot.PartialReason == "" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestCompleteChangelogEmbeddedWithoutPagingMetadataIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/changelog") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"changelog":{"histories":[{"id":"1","created":"2026-01-01","items":[]}]}}`))
	}))
	defer srv.Close()

	snapshot, err := newTestJira(srv).CompleteChangelog(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete || snapshot.Total != 1 || snapshot.PartialReason == "" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestListCommentsMapsFromCommentEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comments":[
			{"id":"1","author":{"displayName":"Bob"},"created":"2026-01-02","body":"hello"},
			{"id":"2","author":{"displayName":"Carol"},"created":"2026-01-03","body":"world"}
		],"total":2}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	cs, err := j.ListComments(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if gotPath != "/rest/api/2/issue/PROJ-1/comment" {
		t.Errorf("path = %q", gotPath)
	}
	if len(cs) != 2 || cs[0].ID != "1" || cs[0].Author != "Bob" || cs[0].Body != "hello" || cs[1].ID != "2" {
		t.Fatalf("comments mismatch: %+v", cs)
	}
}

func TestDeleteCommentHitsRightPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.DeleteComment(context.Background(), "PROJ-1", "42"); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/rest/api/2/issue/PROJ-1/comment/42" {
		t.Errorf("got %s %s, want DELETE /rest/api/2/issue/PROJ-1/comment/42", gotMethod, gotPath)
	}
}

func TestDeleteLinkHitsRightPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.DeleteLink(context.Background(), "10005"); err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/rest/api/2/issueLink/10005" {
		t.Errorf("got %s %s, want DELETE /rest/api/2/issueLink/10005", gotMethod, gotPath)
	}
}

// A link's backend id must be captured so `jira issue link delete <id>` has
// something to target.
func TestGetIssueCapturesLinkIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-1","fields":{"issuelinks":[
			{"id":"10005","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},
			 "outwardIssue":{"key":"PROJ-2"}}
		]}}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	is, err := j.GetIssue(context.Background(), "PROJ-1", nil)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(is.Links) != 1 {
		t.Fatalf("got %d links, want 1", len(is.Links))
	}
	if is.Links[0].ID != "10005" || !strings.EqualFold(is.Links[0].Key, "PROJ-2") {
		t.Errorf("link mismatch: %+v", is.Links[0])
	}
}

// The canonical link-type name must survive mapping alongside the directional
// phrase: identity checks (plan apply / link suggest) compare against the name,
// and for most types ("Duplicate"/"duplicates") the two differ.
func TestGetIssueCapturesLinkTypeName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-1","fields":{"issuelinks":[
			{"id":"10006","type":{"name":"Duplicate","inward":"is duplicated by","outward":"duplicates"},
			 "outwardIssue":{"key":"PROJ-2"}},
			{"id":"10006","type":{"name":"Duplicate","inward":"is duplicated by","outward":"duplicates"},
			 "inwardIssue":{"key":"PROJ-3"}}
		]}}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	is, err := j.GetIssue(context.Background(), "PROJ-1", nil)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(is.Links) != 2 {
		t.Fatalf("got %d links, want 2", len(is.Links))
	}
	out, in := is.Links[0], is.Links[1]
	if out.Type != "duplicates" || out.TypeName != "Duplicate" {
		t.Errorf("outward link: Type=%q TypeName=%q, want phrase+name", out.Type, out.TypeName)
	}
	if in.Type != "is duplicated by" || in.TypeName != "Duplicate" {
		t.Errorf("inward link: Type=%q TypeName=%q, want phrase+name", in.Type, in.TypeName)
	}
}

// A comment listing larger than one server page must be fetched completely:
// the port has no cursor, so the adapter pages internally until total.
func TestListCommentsPaginatesAllPages(t *testing.T) {
	var starts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := r.URL.Query().Get("startAt")
		starts = append(starts, start)
		w.Header().Set("Content-Type", "application/json")
		if start == "0" {
			_, _ = w.Write([]byte(`{"startAt":0,"total":3,"comments":[
				{"id":"1","body":"a"},{"id":"2","body":"b"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"startAt":2,"total":3,"comments":[{"id":"3","body":"c"}]}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	cs, err := j.ListComments(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(cs) != 3 || cs[2].ID != "3" {
		t.Fatalf("expected all 3 comments across pages, got %+v", cs)
	}
	if len(starts) != 2 || starts[0] != "0" || starts[1] != "2" {
		t.Fatalf("expected two paged requests (startAt 0, 2), got %v", starts)
	}
}

func TestListCommentsFailsClosedAtPageGuard(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"startAt":` + r.URL.Query().Get("startAt") + `,"total":101,"comments":[{"id":"one","body":"still paging"}]}`))
	}))
	defer srv.Close()

	comments, err := newTestJira(srv).ListComments(context.Background(), "PROJ-1")
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("ListComments error = %v, want ErrCheckFailed", err)
	}
	if comments != nil {
		t.Fatalf("partial comments escaped with truncation: %+v", comments)
	}
	if requests != commentPageGuard {
		t.Fatalf("requests = %d, want guard %d", requests, commentPageGuard)
	}
}

func TestListCommentsFailsClosedOnEmptyIncompletePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"startAt":0,"total":1,"comments":[]}`))
	}))
	defer srv.Close()

	comments, err := newTestJira(srv).ListComments(context.Background(), "PROJ-1")
	if !errors.Is(err, domain.ErrCheckFailed) || comments != nil {
		t.Fatalf("comments=%+v error=%v, want nil and ErrCheckFailed", comments, err)
	}
}

func TestListCommentsFailsClosedOnUnexpectedOffset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"startAt":1,"total":2,"comments":[{"id":"one"}]}`))
	}))
	defer srv.Close()

	comments, err := newTestJira(srv).ListComments(context.Background(), "PROJ-1")
	if !errors.Is(err, domain.ErrCheckFailed) || comments != nil {
		t.Fatalf("comments=%+v error=%v, want nil and ErrCheckFailed", comments, err)
	}
}

func TestListCommentsFailsClosedWhenTotalChanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("startAt") == "0" {
			_, _ = w.Write([]byte(`{"startAt":0,"total":2,"comments":[{"id":"one"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"startAt":1,"total":1,"comments":[]}`))
	}))
	defer srv.Close()

	comments, err := newTestJira(srv).ListComments(context.Background(), "PROJ-1")
	if !errors.Is(err, domain.ErrCheckFailed) || comments != nil {
		t.Fatalf("comments=%+v error=%v, want nil and ErrCheckFailed", comments, err)
	}
}

func TestListCommentsAllowsCompletionAtExactPageGuard(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		start := r.URL.Query().Get("startAt")
		_, _ = w.Write([]byte(`{"startAt":` + start + `,"total":100,"comments":[{"id":"` + start + `"}]}`))
	}))
	defer srv.Close()

	comments, err := newTestJira(srv).ListComments(context.Background(), "PROJ-1")
	if err != nil || len(comments) != commentPageGuard {
		t.Fatalf("comments=%d error=%v, want %d and nil", len(comments), err, commentPageGuard)
	}
	if requests != commentPageGuard {
		t.Fatalf("requests=%d, want %d", requests, commentPageGuard)
	}
}

// A corrupt --cursor must fail usage (exit 2), not silently restart from 0.
func TestSearchRejectsBadCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request must be issued for a bad cursor")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, _, err := j.Search(context.Background(), "project=P", nil, 10, "abc"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("bad cursor: want ErrUsage, got %v", err)
	}
	if _, _, err := j.Boards(context.Background(), "", 10, "-5"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("negative cursor: want ErrUsage, got %v", err)
	}
}
