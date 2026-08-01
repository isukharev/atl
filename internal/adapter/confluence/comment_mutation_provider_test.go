package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/domain"
)

func testCommentMutationActivation() compatibility.Activation {
	return compatibility.Activation{
		ProviderID:  compatibility.ConfluenceInlineCommentsDCProfileID,
		Version:     "9.5.2",
		BuildNumber: "12345",
	}
}

func mustCommentMutationProvider(t *testing.T, base string) *CommentMutationProvider {
	t.Helper()
	provider, err := NewCommentMutationProvider(&Confluence{c: newTestClient(base), base: base}, testCommentMutationActivation())
	if err != nil {
		t.Fatalf("NewCommentMutationProvider: %v", err)
	}
	return provider
}

func writeTestExactMetadata(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"version":"9.5.2","buildNumber":"12345"}`)
}

func testInlineCreateMutationRequest() domain.ConfluenceCommentMutationRequest {
	return domain.ConfluenceCommentMutationRequest{
		Operation: domain.ConfluenceCommentMutationInlineCreate, PageID: "10", PageVersion: 7,
		BodyStorage: []byte("<p>comment</p>"), SearchSelection: "beta gamma", OriginalSelection: "beta gamma",
		NumMatches: 2, MatchIndex: 1, LastFetchTime: 1700000000123,
		SerializedHighlights: []domain.ConfluenceInlineHighlightGeometry{
			{Text: "beta ", ChildIndexPath: []int{0, 1}, PreviousTextSiblingOffset: 6, Length: 5},
			{Text: "gamma", ChildIndexPath: []int{0, 2, 0}, PreviousTextSiblingOffset: 0, Length: 5},
		},
	}
}

func TestCommentMutationProviderInlineCreateExactContract(t *testing.T) {
	events := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events = append(events, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/wiki/rest/api/server-information":
			writeTestExactMetadata(w)
		case "/wiki/rest/inlinecomments/1.0/comments":
			if r.Method != http.MethodPost || r.URL.RawQuery != "" {
				t.Errorf("create request = %s %s", r.Method, r.URL.RequestURI())
			}
			if r.Header.Get("X-Atlassian-Token") != "no-check" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("create headers = %#v", r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"containerId": "10", "containerVersion": float64(7), "parentCommentId": float64(0),
				"body": "<p>comment</p>", "originalSelection": "beta gamma", "numMatches": float64(2),
				"matchIndex": float64(1), "lastFetchTime": float64(1700000000123),
				"serializedHighlights": `[["beta ","0:1",6,5],["gamma","0:2:0",0,5]]`,
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("create body = %#v, want %#v", body, want)
			}
			_, _ = io.WriteString(w, `{"id":40,"markerRef":"ref-40","originalSelection":"beta gamma","parentCommentId":0,"containerVersion":8,"body":"not-projected"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := mustCommentMutationProvider(t, srv.URL+"/wiki")
	result, err := provider.MutateConfluenceComment(context.Background(), testInlineCreateMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	wantResult := domain.ConfluenceCommentMutationResult{
		Operation: domain.ConfluenceCommentMutationInlineCreate, ThreadID: "40", CommentID: "40",
		MarkerRef: "ref-40", OriginalSelection: "beta gamma", PageVersion: 8,
	}
	if result != wantResult {
		t.Fatalf("result = %+v, want %+v", result, wantResult)
	}
	wantEvents := []string{"GET /wiki/rest/api/server-information", "POST /wiki/rest/inlinecomments/1.0/comments"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestCommentMutationProviderInlineCreateRejectsMalformedResponse(t *testing.T) {
	responses := map[string]string{
		"missing id":        `{"markerRef":"ref","originalSelection":"beta gamma","parentCommentId":0,"containerVersion":8}`,
		"missing marker":    `{"id":40,"originalSelection":"beta gamma","parentCommentId":0,"containerVersion":8}`,
		"wrong selection":   `{"id":40,"markerRef":"ref","originalSelection":"other","parentCommentId":0,"containerVersion":8}`,
		"non-root":          `{"id":40,"markerRef":"ref","originalSelection":"beta gamma","parentCommentId":2,"containerVersion":8}`,
		"missing version":   `{"id":40,"markerRef":"ref","originalSelection":"beta gamma","parentCommentId":0}`,
		"trailing response": `{"id":40,"markerRef":"ref","originalSelection":"beta gamma","parentCommentId":0,"containerVersion":8}{}`,
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			var writes int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rest/api/server-information" {
					writeTestExactMetadata(w)
					return
				}
				writes++
				_, _ = io.WriteString(w, response)
			}))
			defer srv.Close()
			provider := mustCommentMutationProvider(t, srv.URL)
			_, err := provider.MutateConfluenceComment(context.Background(), testInlineCreateMutationRequest())
			if !errors.Is(err, domain.ErrCheckFailed) || writes != 1 {
				t.Fatalf("error=%v writes=%d", err, writes)
			}
			var attempted interface{ DiagnosticWriteAttempted() bool }
			if !errors.As(err, &attempted) || !attempted.DiagnosticWriteAttempted() {
				t.Fatalf("attempted evidence = %T/%v", attempted, attempted != nil && attempted.DiagnosticWriteAttempted())
			}
		})
	}
}

func TestCommentMutationProviderInlineCreateDoesNotReplayRedirect(t *testing.T) {
	var writes, redirects int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/server-information":
			writeTestExactMetadata(w)
		case "/rest/inlinecomments/1.0/comments":
			writes++
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
		case "/redirected":
			redirects++
		}
	}))
	defer srv.Close()
	provider := mustCommentMutationProvider(t, srv.URL)
	_, err := provider.MutateConfluenceComment(context.Background(), testInlineCreateMutationRequest())
	if err == nil || writes != 1 || redirects != 0 {
		t.Fatalf("error=%v writes=%d redirects=%d", err, writes, redirects)
	}
	var attempted interface{ DiagnosticWriteAttempted() bool }
	if !errors.As(err, &attempted) || !attempted.DiagnosticWriteAttempted() {
		t.Fatalf("attempted evidence = %T/%v", attempted, attempted != nil && attempted.DiagnosticWriteAttempted())
	}
}

func TestCommentMutationProviderInlineCreatePreservesAttemptedHTTPStatusWithoutBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/server-information" {
			writeTestExactMetadata(w)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, "backend-conflict-canary")
	}))
	defer srv.Close()
	provider := mustCommentMutationProvider(t, srv.URL)
	_, err := provider.MutateConfluenceComment(context.Background(), testInlineCreateMutationRequest())
	if !errors.Is(err, domain.ErrVersionConflict) || strings.Contains(err.Error(), "backend-conflict-canary") {
		t.Fatalf("error = %v", err)
	}
	var attempted interface{ DiagnosticWriteAttempted() bool }
	var status interface{ HTTPStatus() int }
	hasAttempted := errors.As(err, &attempted)
	hasStatus := errors.As(err, &status)
	statusCode := 0
	if status != nil {
		statusCode = status.HTTPStatus()
	}
	if !hasAttempted || !attempted.DiagnosticWriteAttempted() || !hasStatus || statusCode != http.StatusConflict {
		t.Fatalf("attempted/status = %T/%v %T/%d", attempted, attempted != nil && attempted.DiagnosticWriteAttempted(), status, statusCode)
	}
}

func TestCommentMutationProviderFixedOperationMatrixAndRequalification(t *testing.T) {
	var mu sync.Mutex
	events := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.URL.Path == "/wiki/rest/api/server-information" {
			if r.Method != http.MethodGet {
				t.Errorf("metadata method = %s", r.Method)
			}
			writeTestExactMetadata(w)
			return
		}
		if got := r.Header.Get("X-Atlassian-Token"); got != "no-check" {
			t.Errorf("X-Atlassian-Token = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		switch r.URL.Path {
		case "/wiki/rest/inlinecomments/1.0/comments/20/replies":
			if r.Method != http.MethodPost || r.URL.Query().Get("containerId") != "10" || len(r.URL.Query()) != 1 {
				t.Errorf("reply request = %s %s", r.Method, r.URL.RequestURI())
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"commentId": "20", "containerId": "10", "body": "<p>reply</p>"}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("reply body = %#v, want %#v", body, want)
			}
			_, _ = io.WriteString(w, `{"id":30,"commentId":20,"body":"backend-content-not-projected"}`)
		case "/wiki/rest/inlinecomments/1.0/comments/20/resolve/true/dangling/false":
			if r.Method != http.MethodPut {
				t.Errorf("resolve method = %s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "{}" {
				t.Errorf("resolve body = %q", body)
			}
			_, _ = io.WriteString(w, `{"resolveProperties":{"resolved":true},"untrusted":"not-projected"}`)
		case "/wiki/rest/inlinecomments/1.0/comments/20/resolve/false/dangling/false":
			if r.Method != http.MethodPut {
				t.Errorf("reopen method = %s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "{}" {
				t.Errorf("reopen body = %q", body)
			}
			_, _ = io.WriteString(w, `{"resolveProperties":{"resolved":false}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	provider := mustCommentMutationProvider(t, srv.URL+"/wiki")
	requests := []domain.ConfluenceCommentMutationRequest{
		{Operation: domain.ConfluenceCommentMutationReply, PageID: "10", ThreadID: "20", BodyStorage: []byte("<p>reply</p>")},
		{Operation: domain.ConfluenceCommentMutationResolve, PageID: "10", ThreadID: "20"},
		{Operation: domain.ConfluenceCommentMutationReopen, PageID: "10", ThreadID: "20"},
	}
	wants := []domain.ConfluenceCommentMutationResult{
		{Operation: domain.ConfluenceCommentMutationReply, ThreadID: "20", CommentID: "30", Resolved: false},
		{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "20", CommentID: "20", Resolved: true},
		{Operation: domain.ConfluenceCommentMutationReopen, ThreadID: "20", CommentID: "20", Resolved: false},
	}
	for index, request := range requests {
		result, err := provider.MutateConfluenceComment(context.Background(), request)
		if err != nil {
			t.Fatalf("operation %s: %v", request.Operation, err)
		}
		if result != wants[index] {
			t.Errorf("operation %s result = %+v, want %+v", request.Operation, result, wants[index])
		}
	}

	mu.Lock()
	defer mu.Unlock()
	wantEvents := []string{
		"GET /wiki/rest/api/server-information",
		"POST /wiki/rest/inlinecomments/1.0/comments/20/replies?containerId=10",
		"GET /wiki/rest/api/server-information",
		"PUT /wiki/rest/inlinecomments/1.0/comments/20/resolve/true/dangling/false",
		"GET /wiki/rest/api/server-information",
		"PUT /wiki/rest/inlinecomments/1.0/comments/20/resolve/false/dangling/false",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestCommentMutationProviderMismatchPerformsZeroWrites(t *testing.T) {
	var writes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/server-information" || r.Method != http.MethodGet {
			writes++
		}
		_, _ = io.WriteString(w, `{"version":"9.5.3","buildNumber":"12345"}`)
	}))
	defer srv.Close()
	provider := mustCommentMutationProvider(t, srv.URL)
	_, err := provider.MutateConfluenceComment(context.Background(), domain.ConfluenceCommentMutationRequest{
		Operation: domain.ConfluenceCommentMutationReply, PageID: "10", ThreadID: "20", BodyStorage: []byte("reply"),
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("mismatch error = %v, want ErrCheckFailed", err)
	}
	var attempt interface{ DiagnosticWriteAttempted() bool }
	if !errors.As(err, &attempt) || attempt.DiagnosticWriteAttempted() {
		t.Fatalf("mismatch attempt evidence = %T/%v, want pre-write", attempt, attempt != nil && attempt.DiagnosticWriteAttempted())
	}
	if writes != 0 {
		t.Fatalf("mismatch performed %d writes", writes)
	}
}

func TestCommentMutationProviderWriteDoesNotRedirectOrReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"redirect", http.StatusTemporaryRedirect},
		{"transient", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writeHits, redirectedHits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/rest/api/server-information":
					writeTestExactMetadata(w)
				case "/rest/inlinecomments/1.0/comments/20/replies":
					writeHits++
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, "backend-response-canary")
				case "/redirected":
					redirectedHits++
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			provider := mustCommentMutationProvider(t, srv.URL)
			_, err := provider.MutateConfluenceComment(context.Background(), domain.ConfluenceCommentMutationRequest{
				Operation: domain.ConfluenceCommentMutationReply, PageID: "10", ThreadID: "20", BodyStorage: []byte("reply"),
			})
			if err == nil {
				t.Fatal("write unexpectedly succeeded")
			}
			if writeHits != 1 || redirectedHits != 0 {
				t.Fatalf("write hits=%d redirected hits=%d, want 1 and 0", writeHits, redirectedHits)
			}
			if strings.Contains(err.Error(), "backend-response-canary") || strings.Contains(err.Error(), "/comments/20") {
				t.Fatalf("write error was not sanitized: %v", err)
			}
		})
	}
}

func TestCommentMutationProviderRejectsUnqualifiedResponses(t *testing.T) {
	tests := []struct {
		name     string
		request  domain.ConfluenceCommentMutationRequest
		response string
	}{
		{"reply missing id", domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationReply, PageID: "10", ThreadID: "20", BodyStorage: []byte("reply")}, `{"commentId":20}`},
		{"reply wrong thread", domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationReply, PageID: "10", ThreadID: "20", BodyStorage: []byte("reply")}, `{"id":30,"commentId":21}`},
		{"reply trailing", domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationReply, PageID: "10", ThreadID: "20", BodyStorage: []byte("reply")}, `{"id":30,"commentId":20}{}`},
		{"resolve missing state", domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationResolve, PageID: "10", ThreadID: "20"}, `{}`},
		{"resolve wrong state", domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationResolve, PageID: "10", ThreadID: "20"}, `{"resolveProperties":{"resolved":false}}`},
		{"reopen wrong state", domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationReopen, PageID: "10", ThreadID: "20"}, `{"resolveProperties":{"resolved":true}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var writes int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rest/api/server-information" {
					writeTestExactMetadata(w)
					return
				}
				writes++
				_, _ = io.WriteString(w, tc.response)
			}))
			defer srv.Close()
			provider := mustCommentMutationProvider(t, srv.URL)
			_, err := provider.MutateConfluenceComment(context.Background(), tc.request)
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("response error = %v, want ErrCheckFailed", err)
			}
			var attempt interface{ DiagnosticWriteAttempted() bool }
			if !errors.As(err, &attempt) || !attempt.DiagnosticWriteAttempted() {
				t.Fatalf("response attempt evidence = %T/%v, want attempted", attempt, attempt != nil && attempt.DiagnosticWriteAttempted())
			}
			if writes != 1 {
				t.Fatalf("writes = %d, want one", writes)
			}
		})
	}
}

func TestNewCommentMutationProviderRejectsInvalidActivation(t *testing.T) {
	if _, err := NewCommentMutationProvider(nil, testCommentMutationActivation()); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("nil backend error = %v", err)
	}
	invalid := testCommentMutationActivation()
	invalid.ProviderID = "arbitrary-rest"
	if _, err := NewCommentMutationProvider(&Confluence{c: newTestClient("http://127.0.0.1")}, invalid); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("invalid activation error = %v", err)
	}
}
