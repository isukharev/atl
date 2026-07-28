package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestTransitionByIDSendsExactPayloadWithoutMetadataLookup(t *testing.T) {
	var method, path string
	var payload map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		method, path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := newTestJira(srv).TransitionByID(context.Background(), "ABC-1", domain.JiraTransitionRequest{
		ID: "31",
		Fields: map[string]any{
			"customfield_1": "001",
			"resolution":    map[string]any{"id": "5"},
		},
		Comment: []byte("reviewed\nwiki"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || method != http.MethodPost || path != "/rest/api/2/issue/ABC-1/transitions" {
		t.Fatalf("calls=%d request=%s %s", calls, method, path)
	}
	transition := payload["transition"].(map[string]any)
	fields := payload["fields"].(map[string]any)
	updates := payload["update"].(map[string]any)["comment"].([]any)
	comment := updates[0].(map[string]any)["add"].(map[string]any)
	if transition["id"] != "31" || fields["customfield_1"] != "001" || fields["resolution"].(map[string]any)["id"] != "5" || comment["body"] != "reviewed\nwiki" {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestTransitionByIDSingleAttemptDoesNotRetryOrFollowRedirect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "ambiguous status", status: http.StatusTooManyRequests},
		{name: "redirect", status: http.StatusTemporaryRedirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if tc.status >= 300 && tc.status < 400 {
					w.Header().Set("Location", "/other")
				}
				http.Error(w, "try later", tc.status)
			}))
			defer srv.Close()

			err := newTestJira(srv).TransitionByID(domain.WithSingleAttempt(context.Background()), "ABC-1", domain.JiraTransitionRequest{ID: "31"})
			if err == nil || calls != 1 {
				t.Fatalf("err=%v calls=%d, want one failed POST", err, calls)
			}
		})
	}
}

func TestTransitionByIDRejectsNonCanonicalIDBeforeNetwork(t *testing.T) {
	for _, id := range []string{"", " 31", "31 "} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
		err := newTestJira(srv).TransitionByID(context.Background(), "ABC-1", domain.JiraTransitionRequest{ID: id})
		srv.Close()
		if !errors.Is(err, domain.ErrUsage) || calls != 0 {
			t.Fatalf("id=%q err=%v calls=%d", id, err, calls)
		}
	}
}

func TestTransitionsMapsStableTargetStatusID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"transitions":[{"id":"31","name":"Finish","to":{"id":"5","name":"Done"}}]}`)
	}))
	defer srv.Close()
	transitions, err := newTestJira(srv).Transitions(context.Background(), "ABC-1")
	if err != nil || len(transitions) != 1 || transitions[0].ToID != "5" {
		t.Fatalf("transitions=%+v err=%v", transitions, err)
	}
}
