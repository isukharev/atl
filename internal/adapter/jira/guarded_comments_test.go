package jira

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

func TestGuardedCommentStrictReadsAndNumericIDWrite(t *testing.T) {
	var requests []string
	var posted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/OPS-1":
			_, _ = io.WriteString(w, `{"id":"101","key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-08-22T10:00:00.123+0000"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/myself":
			_, _ = io.WriteString(w, `{"name":"writer","key":"writer-key","displayName":"Private Display","emailAddress":"private@example.test"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/101/comment":
			body, _ := io.ReadAll(r.Body)
			posted = string(body)
			_, _ = io.WriteString(w, `{"id":"202","body":"ignored"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	ctx := domain.WithSingleAttempt(t.Context())
	issue, err := adapter.ReadGuardedCommentIssue(ctx, "OPS-1")
	if err != nil || issue != (domain.JiraGuardedCommentIssue{ID: "101", Key: "OPS-1", Project: "OPS", Updated: "2026-08-22T10:00:00.123+0000", Complete: true}) {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	actor, err := adapter.ReadGuardedCommentActor(ctx)
	if err != nil || actor != (domain.JiraGuardedCommentActor{Name: "writer", Key: "writer-key", Complete: true}) {
		t.Fatalf("actor=%+v err=%v", actor, err)
	}
	ack, err := adapter.WriteGuardedComment(ctx, domain.JiraGuardedCommentWrite{ID: "101", Key: "OPS-1", Project: "OPS", Body: []byte("native *wiki*\n")})
	if err != nil || ack.ID != "202" || posted != `{"body":"native *wiki*\n"}` {
		t.Fatalf("ack=%+v err=%v body=%q", ack, err, posted)
	}
	wantAuth := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", ID: "101", Key: "OPS-1", Project: "OPS"}}}
	if len(authorizer.requests) != 1 || !reflect.DeepEqual(authorizer.requests[0], wantAuth) {
		t.Fatalf("authorization=%+v", authorizer.requests)
	}
	if len(requests) != 3 || requests[2] != "POST /rest/api/2/issue/101/comment" {
		t.Fatalf("requests=%v", requests)
	}
}

func TestGuardedCommentStrictIssueAndActorDecodersRejectRawWireAmbiguity(t *testing.T) {
	validIssue := `{"id":"101","key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-08-22T10:00:00Z"}}`
	validActor := `{"name":"writer","key":"writer-key"}`
	for name, response := range map[string]string{
		"issue omitted id":     `{"key":"OPS-1","fields":{"project":{"key":"OPS"},"updated":"2026-08-22T10:00:00Z"}}`,
		"issue numeric id":     strings.Replace(validIssue, `"101"`, `101`, 1),
		"issue duplicate id":   strings.Replace(validIssue, `"id":"101"`, `"id":"101","id":"102"`, 1),
		"issue null updated":   strings.Replace(validIssue, `"2026-08-22T10:00:00Z"`, `null`, 1),
		"issue date only":      strings.Replace(validIssue, `2026-08-22T10:00:00Z`, `2026-08-22`, 1),
		"issue comma fraction": strings.Replace(validIssue, `10:00:00Z`, `10:00:00,123Z`, 1),
		"issue moved project":  strings.Replace(validIssue, `"OPS"`, `"ALT"`, 1),
		"issue lone surrogate": strings.Replace(validIssue, `"OPS-1"`, `"OPS-1\ud800"`, 1),
		"actor omitted name":   `{"key":"writer-key"}`,
		"actor null key":       `{"name":"writer","key":null}`,
		"actor boolean name":   `{"name":true,"key":"writer-key"}`,
		"actor duplicate key":  `{"name":"writer","key":"writer-key","key":"other"}`,
		"actor untrimmed":      `{"name":" writer","key":"writer-key"}`,
		"actor invalid utf8":   "{\"name\":\"\xff\",\"key\":\"writer-key\"}",
		"actor lone surrogate": `{"name":"\ud800","key":"writer-key"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(name, "actor") || strings.HasPrefix(name, "actor ") || r.URL.Path == "/rest/api/2/myself" {
					_, _ = io.WriteString(w, response)
					return
				}
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			adapter := New(server.URL, "token", "test")
			var err error
			if strings.HasPrefix(name, "actor") {
				_, err = adapter.ReadGuardedCommentActor(domain.WithSingleAttempt(t.Context()))
			} else {
				_, err = adapter.ReadGuardedCommentIssue(domain.WithSingleAttempt(t.Context()), "OPS-1")
			}
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	for name, issueWire := range map[string]string{
		"surrogate pair": strings.Replace(validIssue, `"OPS-1"`, `"OPS-1\ud83d\ude00"`, 1),
		"escaped slash":  strings.Replace(validIssue, `"OPS-1"`, `"OPS-1\\u1234"`, 1),
		"literal U+FFFD": strings.Replace(validIssue, `"OPS-1"`, `"OPS-1�"`, 1),
	} {
		t.Run("valid raw "+name, func(t *testing.T) {
			// Raw-wire qualification accepts the JSON representation; semantic key
			// qualification still refuses these deliberately noncanonical values.
			if root, ok := guardedCommentObject([]byte(issueWire)); !ok || root == nil {
				t.Fatalf("raw valid wire rejected: %q", issueWire)
			}
		})
	}
	if root, ok := guardedCommentObject([]byte(validActor)); !ok || root == nil {
		t.Fatal("valid actor wire rejected")
	}
	for name, test := range map[string]struct {
		wire     string
		wantName string
		wantKey  string
	}{
		"surrogate pair and escaped U+FFFD":    {`{"name":"writer\ud83d\ude00","key":"writer-key\ufffd"}`, "writer😀", "writer-key�"},
		"escaped backslash and literal U+FFFD": {`{"name":"writer\\ud800","key":"writer-key�"}`, `writer\ud800`, "writer-key�"},
	} {
		t.Run("valid actor "+name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.wire) }))
			defer server.Close()
			actor, err := New(server.URL, "token", "test").ReadGuardedCommentActor(domain.WithSingleAttempt(t.Context()))
			if err != nil || actor.Name != test.wantName || actor.Key != test.wantKey {
				t.Fatalf("actor=%+v err=%v", actor, err)
			}
		})
	}
}

func TestGuardedCommentWriterRejectsBeforeAuthorizationAndMalformedAckRemainsAttempted(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	for _, write := range []domain.JiraGuardedCommentWrite{
		{ID: "01", Key: "OPS-1", Project: "OPS", Body: []byte("x")},
		{ID: "10", Key: "ALT-1", Project: "OPS", Body: []byte("x")},
		{ID: "10", Key: "OPS-1", Project: "OPS"},
		{ID: "10", Key: "OPS-1", Project: "OPS", Body: []byte{0xff}},
		{ID: "10", Key: "OPS-1", Project: "OPS", Body: make([]byte, jiraGuardedCommentBodyMaxBytes+1)},
	} {
		_, err := adapter.WriteGuardedComment(t.Context(), write)
		var diagnostic interface{ DiagnosticWriteAttempted() bool }
		if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &diagnostic) || diagnostic.DiagnosticWriteAttempted() {
			t.Fatalf("write=%+v err=%v", write, err)
		}
	}
	if requests.Load() != 0 || len(authorizer.requests) != 0 {
		t.Fatalf("requests=%d auth=%d", requests.Load(), len(authorizer.requests))
	}
	_, err := adapter.WriteGuardedComment(domain.WithSingleAttempt(t.Context()), domain.JiraGuardedCommentWrite{ID: "10", Key: "OPS-1", Project: "OPS", Body: []byte("x")})
	var noAttempt interface{ DiagnosticWriteAttempted() bool }
	if !errors.Is(err, domain.ErrCheckFailed) || errors.As(err, &noAttempt) || requests.Load() != 1 {
		t.Fatalf("malformed ack err=%v requests=%d diagnostic=%T", err, requests.Load(), noAttempt)
	}
}

func TestGuardedCommentCanonicalIDPolicyIsAuthoritativeAtLastHop(t *testing.T) {
	exact := domain.JiraGuardedCommentWrite{ID: "101", Key: "OPS-1", Project: "OPS", Body: []byte("native *wiki*")}
	policy := func(effect contentpolicy.Effect) *contentpolicy.Authorizer {
		return contentpolicy.NewAuthorizer(&contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
			Source: "managed", Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
				ID: "exact-comment", Effect: effect, Verbs: domain.WriteVerbSet{domain.WriteVerbComment},
				Resource: contentpolicy.Selector{Services: []string{"jira"}, Kinds: []string{"issue"}, IDs: []string{"101"}, Keys: []string{"OPS-1"}, Projects: []string{"OPS"}},
			}}},
		}}})
	}
	t.Run("exact allow preserves native bytes", func(t *testing.T) {
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			posts.Add(1)
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || r.URL.Path != "/rest/api/2/issue/101/comment" || string(body) != `{"body":"native *wiki*"}` {
				t.Errorf("request=%s %s body=%q", r.Method, r.URL.Path, body)
			}
			_, _ = io.WriteString(w, `{"id":"202"}`)
		}))
		defer server.Close()
		ack, err := New(server.URL, "token", "test", WithWriteAuthorizer(policy(contentpolicy.EffectAllow))).WriteGuardedComment(domain.WithSingleAttempt(t.Context()), exact)
		if err != nil || ack.ID != "202" || posts.Load() != 1 {
			t.Fatalf("ack=%+v err=%v posts=%d", ack, err, posts.Load())
		}
	})

	tests := []struct {
		name       string
		write      domain.JiraGuardedCommentWrite
		effect     contentpolicy.Effect
		wantReason contentpolicy.DenialReason
	}{
		{name: "explicit exact denial", write: exact, effect: contentpolicy.EffectDeny, wantReason: contentpolicy.ReasonExplicitDeny},
		{name: "drifted id", write: domain.JiraGuardedCommentWrite{ID: "102", Key: "OPS-1", Project: "OPS", Body: []byte("x")}, effect: contentpolicy.EffectAllow, wantReason: contentpolicy.ReasonNoMatchingAllow},
		{name: "missing id", write: domain.JiraGuardedCommentWrite{Key: "OPS-1", Project: "OPS", Body: []byte("x")}, effect: contentpolicy.EffectAllow},
		{name: "non-numeric id", write: domain.JiraGuardedCommentWrite{ID: "id", Key: "OPS-1", Project: "OPS", Body: []byte("x")}, effect: contentpolicy.EffectAllow},
		{name: "noncanonical id", write: domain.JiraGuardedCommentWrite{ID: "01", Key: "OPS-1", Project: "OPS", Body: []byte("x")}, effect: contentpolicy.EffectAllow},
		{name: "overflow id", write: domain.JiraGuardedCommentWrite{ID: "18446744073709551616", Key: "OPS-1", Project: "OPS", Body: []byte("x")}, effect: contentpolicy.EffectAllow},
		{name: "drifted key", write: domain.JiraGuardedCommentWrite{ID: "101", Key: "OPS-2", Project: "OPS", Body: []byte("x")}, effect: contentpolicy.EffectAllow, wantReason: contentpolicy.ReasonNoMatchingAllow},
		{name: "missing key", write: domain.JiraGuardedCommentWrite{ID: "101", Project: "OPS", Body: []byte("x")}, effect: contentpolicy.EffectAllow},
		{name: "drifted project", write: domain.JiraGuardedCommentWrite{ID: "101", Key: "ALT-1", Project: "ALT", Body: []byte("x")}, effect: contentpolicy.EffectAllow, wantReason: contentpolicy.ReasonNoMatchingAllow},
		{name: "missing project", write: domain.JiraGuardedCommentWrite{ID: "101", Key: "OPS-1", Body: []byte("x")}, effect: contentpolicy.EffectAllow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				posts.Add(1)
				_, _ = io.WriteString(w, `{"id":"202"}`)
			}))
			defer server.Close()
			_, err := New(server.URL, "token", "test", WithWriteAuthorizer(policy(test.effect))).WriteGuardedComment(domain.WithSingleAttempt(t.Context()), test.write)
			if !errors.Is(err, domain.ErrCheckFailed) || posts.Load() != 0 {
				t.Fatalf("err=%v posts=%d", err, posts.Load())
			}
			var denial *contentpolicy.DenialError
			if test.wantReason != "" {
				if !errors.As(err, &denial) || denial.Reason != test.wantReason || denial.Details.Target.ID != test.write.ID ||
					denial.Details.Target.Key != test.write.Key || denial.Details.Target.Project != test.write.Project {
					t.Fatalf("denial=%+v", denial)
				}
			} else if errors.As(err, &denial) {
				t.Fatalf("invalid input reached policy: %+v", denial)
			}
		})
	}
}

func TestDirectCommentJiraIssueIDSelectorRemainsUnresolved(t *testing.T) {
	for _, test := range []struct {
		effect contentpolicy.Effect
		reason contentpolicy.DenialReason
	}{
		{effect: contentpolicy.EffectAllow, reason: contentpolicy.ReasonNoMatchingAllow},
		{effect: contentpolicy.EffectDeny, reason: contentpolicy.ReasonScopeUnresolved},
	} {
		t.Run(string(test.effect), func(t *testing.T) {
			var gets, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					gets.Add(1)
					_, _ = io.WriteString(w, `{"id":"101","key":"OPS-1","fields":{"project":{"key":"OPS"}}}`)
				case http.MethodPost:
					posts.Add(1)
					_, _ = io.WriteString(w, `{"id":"202"}`)
				}
			}))
			defer server.Close()
			authorizer := contentpolicy.NewAuthorizer(&contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
				Source: "managed", Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
					ID: "id-rule", Effect: test.effect, Verbs: domain.WriteVerbSet{domain.WriteVerbComment},
					Resource: contentpolicy.Selector{Services: []string{"jira"}, Kinds: []string{"issue"}, IDs: []string{"101"}},
				}}},
			}}})
			_, err := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer)).AddComment(domain.WithSingleAttempt(t.Context()), "OPS-1", []byte("native"))
			var denial *contentpolicy.DenialError
			if !errors.As(err, &denial) || denial.Reason != test.reason || denial.Details.Target.ID != "" ||
				denial.Details.Target.Key != "OPS-1" || gets.Load() != 1 || posts.Load() != 0 {
				t.Fatalf("err=%v denial=%+v gets=%d posts=%d", err, denial, gets.Load(), posts.Load())
			}
		})
	}
}

func TestGuardedCommentWriteIsSingleAttemptAndChargesAcknowledgementBytes(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"id":"20"}`)
			}))
			defer server.Close()
			budget, err := domain.NewReadBudget(1, 1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := domain.WithSingleAttempt(domain.WithReadBudget(t.Context(), budget))
			_, err = New(server.URL, "token", "test").WriteGuardedComment(ctx, domain.JiraGuardedCommentWrite{
				ID: "10", Key: "OPS-1", Project: "OPS", Body: []byte("x"),
			})
			if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || requests.Load() != 1 || budget.Usage().Attempts != 1 || budget.Usage().ResponseBytes != 1 {
				t.Fatalf("status=%d err=%v requests=%d usage=%+v", status, err, requests.Load(), budget.Usage())
			}
		})
	}

	var requests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", server.URL+"/rest/api/2/issue/10/comment")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := New(server.URL, "token", "test").WriteGuardedComment(domain.WithSingleAttempt(t.Context()), domain.JiraGuardedCommentWrite{
		ID: "10", Key: "OPS-1", Project: "OPS", Body: []byte("x"),
	})
	var statusErr interface{ HTTPStatus() int }
	if !errors.As(err, &statusErr) || statusErr.HTTPStatus() != http.StatusTemporaryRedirect || requests.Load() != 1 {
		t.Fatalf("redirect err=%v requests=%d", err, requests.Load())
	}
}

func TestQualifiedCommentDecoderRejectsTypesUnicodeAndAcceptsExactStrings(t *testing.T) {
	valid := `{"startAt":0,"total":1,"comments":[{"id":"1","author":{"name":"writer","key":"writer-key"},"created":"2026-08-22T10:00:00Z","updated":"2026-08-22T10:00:00Z","parentId":null,"body":"body"}]}`
	for name, response := range map[string]string{
		"id null":          strings.Replace(valid, `"1"`, `null`, 1),
		"id number":        strings.Replace(valid, `"1"`, `1`, 1),
		"author array":     strings.Replace(valid, `{"name":"writer","key":"writer-key"}`, `[]`, 1),
		"author name bool": strings.Replace(valid, `"writer"`, `true`, 1),
		"created object":   strings.Replace(valid, `"2026-08-22T10:00:00Z"`, `{}`, 1),
		"parent number":    strings.Replace(valid, `"parentId":null`, `"parentId":1`, 1),
		"body null":        strings.Replace(valid, `"body":"body"`, `"body":null`, 1),
		"body duplicate":   strings.Replace(valid, `"body":"body"`, `"body":"body","body":"other"`, 1),
		"body lone escape": strings.Replace(valid, `"body":"body"`, `"body":"\ud800"`, 1),
		"raw invalid utf8": strings.Replace(valid, `"body":"body"`, "\"body\":\"\xff\"", 1),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			_, err := New(server.URL, "token", "test").ListJiraCommentsQualified(domain.WithSingleAttempt(t.Context()), "101", domain.JiraCommentReadOptions{MaxPages: 1, MaxItems: 1})
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v response=%q", err, response)
			}
		})
	}
	for name, body := range map[string]string{"surrogate pair": `\ud83d\ude00`, "escaped slash": `\\ud800`, "literal U+FFFD": `�`, "escaped U+FFFD": `\ufffd`} {
		t.Run(name, func(t *testing.T) {
			response := strings.Replace(valid, `"body":"body"`, `"body":"`+body+`"`, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			inventory, err := New(server.URL, "token", "test").ListJiraCommentsQualified(domain.WithSingleAttempt(t.Context()), "101", domain.JiraCommentReadOptions{MaxPages: 1, MaxItems: 1})
			if err != nil || len(inventory.Comments) != 1 {
				t.Fatalf("inventory=%+v err=%v", inventory, err)
			}
		})
	}
	for name, test := range map[string]struct {
		author   string
		wantName string
		wantKey  string
	}{
		"surrogate pair and escaped U+FFFD":    {`{"name":"writer\ud83d\ude00","key":"writer-key\ufffd"}`, "writer😀", "writer-key�"},
		"escaped backslash and literal U+FFFD": {`{"name":"writer\\ud800","key":"writer-key�"}`, `writer\ud800`, "writer-key�"},
	} {
		t.Run("author "+name, func(t *testing.T) {
			response := strings.Replace(valid, `{"name":"writer","key":"writer-key"}`, test.author, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			inventory, err := New(server.URL, "token", "test").ListJiraCommentsQualified(domain.WithSingleAttempt(t.Context()), "101", domain.JiraCommentReadOptions{MaxPages: 1, MaxItems: 1})
			if err != nil || len(inventory.Comments) != 1 || inventory.Comments[0].AuthorName != test.wantName || inventory.Comments[0].AuthorKey != test.wantKey {
				t.Fatalf("inventory=%+v err=%v", inventory, err)
			}
		})
	}
	encoded, _ := json.Marshal(map[string]any{"startAt": 0, "total": 0, "comments": []any{}})
	if !json.Valid(encoded) {
		t.Fatal("test fixture invalid")
	}
}
