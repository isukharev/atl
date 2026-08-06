package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

type recordingWriteAuthorizer struct {
	requests []domain.WriteAuthorizationRequest
	err      error
}

func (authorizer *recordingWriteAuthorizer) Authorize(ctx context.Context, request domain.WriteAuthorizationRequest) (context.Context, error) {
	authorizer.requests = append(authorizer.requests, request)
	if authorizer.err != nil {
		return ctx, authorizer.err
	}
	return domain.WithWriteClearance(ctx), nil
}

func writeIssueIdentity(t *testing.T, writer http.ResponseWriter, reference string) {
	t.Helper()
	project := strings.SplitN(reference, "-", 2)[0]
	writer.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(writer, `{"id":"10","key":"`+reference+`","fields":{"project":{"key":"`+project+`"}}}`)
}

func TestJiraNilAuthorizerPreservesRequestCountWithClearanceBackstop(t *testing.T) {
	var gets, puts int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			gets++
		case http.MethodPut:
			puts++
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{}`)
		}
	}))
	defer server.Close()

	adapter := New(server.URL, "token", "test")
	if err := adapter.Update(context.Background(), "ML-1", "summary", nil, nil); err != nil {
		t.Fatal(err)
	}
	if gets != 0 || puts != 1 {
		t.Fatalf("request counts GET=%d PUT=%d, want 0/1", gets, puts)
	}
}

func TestJiraAuthorizationResolvesCanonicalIdentityAndCachesIt(t *testing.T) {
	var gets, puts int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			gets++
			writeIssueIdentity(t, writer, "ML-1")
		case http.MethodPut:
			puts++
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{}`)
		}
	}))
	defer server.Close()

	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	for range 2 {
		if err := adapter.Assign(context.Background(), "ML-1", "alice"); err != nil {
			t.Fatal(err)
		}
	}
	if gets != 1 || puts != 2 || len(authorizer.requests) != 2 {
		t.Fatalf("GET=%d PUT=%d authorization=%d, want 1/2/2", gets, puts, len(authorizer.requests))
	}
	target := authorizer.requests[0].Targets[0]
	if target.Service != "jira" || target.Kind != "issue" || target.Project != "ML" || target.Key != "ML-1" {
		t.Fatalf("target = %+v", target)
	}
}

func TestJiraNumericReferenceUsesResolvedIdentityWithoutComparison(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"id":"999","key":"ML-1","fields":{"project":{"key":"ML"}}}`)
			return
		}
		writes++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer server.Close()

	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	if err := adapter.DeleteIssue(context.Background(), "10", false); err != nil {
		t.Fatal(err)
	}
	target := authorizer.requests[0].Targets[0]
	if writes != 1 || target.Key != "ML-1" || target.Project != "ML" {
		t.Fatalf("writes=%d target=%+v", writes, target)
	}
}

func TestIssueIdentityCacheIsBoundedLRU(t *testing.T) {
	cache := newIssueIdentityCache()
	for index := 1; index <= issueIdentityCacheLimit; index++ {
		key := "ML-" + strconv.Itoa(index)
		cache.put(strconv.Itoa(index), domain.WriteTarget{Service: "jira", Kind: "issue", Key: key, Project: "ML"})
	}
	if _, ok := cache.get("1"); !ok {
		t.Fatal("recently touched entry is missing")
	}
	cache.put("next", domain.WriteTarget{Service: "jira", Kind: "issue", Key: "ML-4097", Project: "ML"})
	if _, ok := cache.get("2"); ok {
		t.Fatal("least-recently-used entry was retained")
	}
	if _, ok := cache.get("1"); !ok || len(cache.entries) != issueIdentityCacheLimit {
		t.Fatalf("cache entries=%d, want %d with touched entry retained", len(cache.entries), issueIdentityCacheLimit)
	}
	for index := 1; index <= issueIdentityCacheLimit+1; index++ {
		cache.fail("missing-"+strconv.Itoa(index), errors.New("missing"))
	}
	if ok, _ := cache.failure("missing-1"); ok {
		t.Fatal("oldest cached failure was retained")
	}
	if ok, _ := cache.failure("missing-4097"); !ok || len(cache.failureEntries) != issueIdentityCacheLimit {
		t.Fatalf("failure cache entries=%d, want %d", len(cache.failureEntries), issueIdentityCacheLimit)
	}
}

func TestJiraPolicyDenialStopsBeforeMutatingRequest(t *testing.T) {
	var gets, writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			gets++
			writeIssueIdentity(t, writer, "ML-1")
			return
		}
		writes++
	}))
	defer server.Close()

	resolved := &contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "managed",
		Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
			ID: "deny-ml", Effect: contentpolicy.EffectDeny,
			Verbs:    domain.WriteVerbSet{domain.WriteVerbDelete},
			Resource: contentpolicy.Selector{Services: []string{"jira"}, Projects: []string{"ML"}},
		}}},
	}}}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(contentpolicy.NewAuthorizer(resolved)))
	err := adapter.DeleteIssue(context.Background(), "ML-1", false)
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonExplicitDeny || denial.RuleID != "deny-ml" {
		t.Fatalf("error=%v denial=%+v", err, denial)
	}
	if gets != 1 || writes != 0 {
		t.Fatalf("GET=%d writes=%d, want 1/0", gets, writes)
	}
}

func TestJiraCompoundAndRelocationAuthorizationTargets(t *testing.T) {
	var identityReads int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/rest/api/2/issue/") {
			identityReads++
			reference := strings.TrimPrefix(request.URL.Path, "/rest/api/2/issue/")
			writeIssueIdentity(t, writer, reference)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer server.Close()

	authorizer := &recordingWriteAuthorizer{}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	if err := adapter.Link(context.Background(), "ML-1", "OPS-2", "Relates"); err != nil {
		t.Fatal(err)
	}
	link := authorizer.requests[len(authorizer.requests)-1]
	if len(link.Targets) != 2 || link.Targets[0].Key != "ML-1" || link.Targets[1].Key != "OPS-2" {
		t.Fatalf("link request = %+v", link)
	}
	if err := adapter.Update(context.Background(), "ML-1", "", nil, map[string]string{"project": `{"key":"OPS"}`}); err != nil {
		t.Fatal(err)
	}
	relocation := authorizer.requests[len(authorizer.requests)-1]
	if len(relocation.Targets) != 2 || relocation.Targets[1].Project != "OPS" || !containsWriteVerb(relocation.Verbs, domain.WriteVerbMove) || !containsWriteVerb(relocation.Verbs, domain.WriteVerbUpdate) {
		t.Fatalf("relocation request = %+v", relocation)
	}
	if err := adapter.TransitionByID(context.Background(), "ML-1", domain.JiraTransitionRequest{ID: "2", Fields: map[string]any{"project": map[string]any{"key": "OPS"}}, Comment: []byte("done")}); err != nil {
		t.Fatal(err)
	}
	transition := authorizer.requests[len(authorizer.requests)-1]
	for _, verb := range (domain.WriteVerbSet{domain.WriteVerbTransition, domain.WriteVerbComment, domain.WriteVerbUpdate, domain.WriteVerbMove}) {
		if !containsWriteVerb(transition.Verbs, verb) {
			t.Fatalf("transition relocation verbs = %v, missing %q", transition.Verbs, verb)
		}
	}
	readsBefore := identityReads
	if err := adapter.Assign(context.Background(), "ML-1", "alice"); err != nil {
		t.Fatal(err)
	}
	if identityReads != readsBefore+1 {
		t.Fatalf("identity reads after relocation = %d, want cache eviction read %d", identityReads, readsBefore+1)
	}
	if err := adapter.MoveIssuesToSprint(context.Background(), 42, []string{"ML-1", "OPS-2"}); err != nil {
		t.Fatal(err)
	}
	sprint := authorizer.requests[len(authorizer.requests)-1]
	if len(sprint.Targets) != 3 || sprint.Targets[2].Kind != "sprint" || sprint.Targets[2].ID != "42" {
		t.Fatalf("sprint request = %+v", sprint)
	}
}

func TestJiraPartialTargetsRequireServiceWideAllow(t *testing.T) {
	var identityReads, writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			identityReads++
			writeIssueIdentity(t, writer, "ML-1")
			return
		}
		writes++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer server.Close()

	policy := func(selector contentpolicy.Selector) *contentpolicy.Authorizer {
		return contentpolicy.NewAuthorizer(&contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
			Source: "managed", Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
				ID: "allow-delete", Effect: contentpolicy.EffectAllow,
				Verbs: domain.WriteVerbSet{domain.WriteVerbDelete}, Resource: selector,
			}}},
		}}})
	}
	projectOnly := New(server.URL, "token", "test", WithWriteAuthorizer(policy(contentpolicy.Selector{
		Services: []string{"jira"}, Projects: []string{"ML"},
	})))
	for name, run := range map[string]func() error{
		"link":     func() error { return projectOnly.DeleteLink(context.Background(), "7") },
		"subtasks": func() error { return projectOnly.DeleteIssue(context.Background(), "ML-1", true) },
	} {
		err := run()
		var denial *contentpolicy.DenialError
		if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved {
			t.Fatalf("%s error=%v denial=%+v", name, err, denial)
		}
	}
	if identityReads != 1 || writes != 0 {
		t.Fatalf("project-only reads=%d writes=%d, want 1/0", identityReads, writes)
	}

	serviceWide := New(server.URL, "token", "test", WithWriteAuthorizer(policy(contentpolicy.Selector{Services: []string{"jira"}})))
	if err := serviceWide.DeleteLink(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	if err := serviceWide.DeleteIssue(context.Background(), "ML-1", true); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("service-wide writes=%d, want 2", writes)
	}
}

func TestJiraRelocationRejectsNoncanonicalDestinationProject(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeIssueIdentity(t, writer, "ML-1")
			return
		}
		writes++
	}))
	defer server.Close()
	allow := contentpolicy.NewAuthorizer(&contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "managed", Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
			ID: "allow", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove},
			Resource: contentpolicy.Selector{Services: []string{"jira"}},
		}}},
	}}})
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(allow))
	for name, project := range map[string]any{
		"lowercase": map[string]any{"key": "ops"},
		"ambiguous": map[string]any{"key": "OPS", "id": "100"},
	} {
		err := adapter.SetFields(context.Background(), "ML-1", map[string]any{"project": project})
		var denial *contentpolicy.DenialError
		if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved || writes != 0 {
			t.Fatalf("%s error=%v denial=%+v writes=%d", name, err, denial, writes)
		}
	}
}

func TestJiraScopeFailuresBecomeStablePolicyDenials(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		response   string
		wantReason contentpolicy.DenialReason
		wantAdvice contentpolicy.Advice
		retrySafe  bool
	}{
		{"moved key", http.StatusOK, `{"id":"10","key":"OPS-2","fields":{"project":{"key":"OPS"}}}`, contentpolicy.ReasonScopeUnresolved, contentpolicy.AdviceNoRetry, false},
		{"unavailable identity", http.StatusServiceUnavailable, `{}`, contentpolicy.ReasonScopeUnavailable, contentpolicy.AdviceWaitThenRetry, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var writes int
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet {
					writes++
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.response)
			}))
			defer server.Close()
			allow := &contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
				Source: "managed",
				Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
					ID: "allow", Effect: contentpolicy.EffectAllow,
					Verbs:    domain.WriteVerbSet{domain.WriteVerbUpdate},
					Resource: contentpolicy.Selector{Services: []string{"jira"}},
				}}},
			}}}
			adapter := New(server.URL, "token", "test", WithWriteAuthorizer(contentpolicy.NewAuthorizer(allow)))
			err := adapter.Assign(context.Background(), "ML-1", "alice")
			var denial *contentpolicy.DenialError
			if !errors.As(err, &denial) || denial.Reason != test.wantReason || denial.Advice != test.wantAdvice || denial.RetrySafe != test.retrySafe ||
				denial.Details.Advice != test.wantAdvice || denial.Details.RetrySafe != test.retrySafe || writes != 0 {
				t.Fatalf("error=%v denial=%+v writes=%d", err, denial, writes)
			}
		})
	}
}

func TestJiraIdentityResolutionFailureIsSticky(t *testing.T) {
	var reads, writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			return
		}
		reads++
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	allow := &contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "managed", Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
			ID: "allow", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
			Resource: contentpolicy.Selector{Services: []string{"jira"}},
		}}},
	}}}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(contentpolicy.NewAuthorizer(allow)))
	assertUnavailable := func() {
		t.Helper()
		err := adapter.Assign(context.Background(), "ML-1", "alice")
		var denial *contentpolicy.DenialError
		if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnavailable {
			t.Fatalf("error=%v denial=%+v", err, denial)
		}
	}
	assertUnavailable()
	readsAfterFirst := reads
	assertUnavailable()
	if readsAfterFirst == 0 || reads != readsAfterFirst || writes != 0 {
		t.Fatalf("reads=%d→%d writes=%d, want no second resolution", readsAfterFirst, reads, writes)
	}
}

func TestJiraNoncanonicalReferenceCannotGroundPolicyAllow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("noncanonical reference reached backend")
	}))
	defer server.Close()
	allow := &contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "managed", Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
			ID: "allow", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
			Resource: contentpolicy.Selector{Services: []string{"jira"}},
		}}},
	}}}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(contentpolicy.NewAuthorizer(allow)))
	err := adapter.Assign(context.Background(), "ml-1", "alice")
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved {
		t.Fatalf("error=%v denial=%+v", err, denial)
	}
}

func TestJiraCreateProjectOverrideIsScopeContradiction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("policy contradiction reached backend")
	}))
	defer server.Close()
	allow := &contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "managed",
		Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
			ID: "allow", Effect: contentpolicy.EffectAllow,
			Verbs:    domain.WriteVerbSet{domain.WriteVerbCreate},
			Resource: contentpolicy.Selector{Services: []string{"jira"}},
		}}},
	}}}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(contentpolicy.NewAuthorizer(allow)))
	_, err := adapter.Create(context.Background(), "ML", "Task", "summary", nil, map[string]string{"project": `{"key":"OPS"}`})
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeContradiction {
		t.Fatalf("error=%v denial=%+v", err, denial)
	}
}

func TestEveryJiraMutatingTransportSiteInvokesAuthorizer(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/rest/api/2/field" {
			_, _ = io.WriteString(writer, `[{"id":"customfield_1","name":"Epic Link"}]`)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/rest/api/2/issue/") {
			reference := strings.TrimPrefix(request.URL.Path, "/rest/api/2/issue/")
			if slash := strings.IndexByte(reference, '/'); slash >= 0 {
				reference = reference[:slash]
			}
			writeIssueIdentity(t, writer, reference)
			return
		}
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer server.Close()

	blocked := errors.New("blocked by test authorizer")
	tests := []struct {
		name string
		run  func(*Jira) error
	}{
		{"Create", func(j *Jira) error { _, err := j.Create(context.Background(), "ML", "Task", "s", nil, nil); return err }},
		{"Update", func(j *Jira) error { return j.Update(context.Background(), "ML-1", "s", nil, nil) }},
		{"SetFields", func(j *Jira) error { return j.SetFields(context.Background(), "ML-1", map[string]any{"summary": "s"}) }},
		{"TransitionByID", func(j *Jira) error {
			return j.TransitionByID(context.Background(), "ML-1", domain.JiraTransitionRequest{ID: "2"})
		}},
		{"DeleteIssue", func(j *Jira) error { return j.DeleteIssue(context.Background(), "ML-1", false) }},
		{"UpdateLabels", func(j *Jira) error { return j.UpdateLabels(context.Background(), "ML-1", []string{"x"}, nil) }},
		{"Assign", func(j *Jira) error { return j.Assign(context.Background(), "ML-1", "alice") }},
		{"AddComment", func(j *Jira) error { _, err := j.AddComment(context.Background(), "ML-1", []byte("x")); return err }},
		{"DeleteComment", func(j *Jira) error { return j.DeleteComment(context.Background(), "ML-1", "7") }},
		{"Link", func(j *Jira) error { return j.Link(context.Background(), "ML-1", "OPS-2", "Relates") }},
		{"DeleteLink", func(j *Jira) error { return j.DeleteLink(context.Background(), "7") }},
		{"LinkEpic", func(j *Jira) error { return j.LinkEpic(context.Background(), "ML-1", "ML-2") }},
		{"MoveIssuesToSprint", func(j *Jira) error { return j.MoveIssuesToSprint(context.Background(), 4, []string{"ML-1"}) }},
		{"MoveIssuesToBacklog", func(j *Jira) error { return j.MoveIssuesToBacklog(context.Background(), []string{"ML-1"}) }},
		{"UploadAttachment", func(j *Jira) error {
			_, err := j.UploadAttachment(context.Background(), "ML-1", "x.txt", strings.NewReader("x"), 1)
			return err
		}},
		{"AddIssueWatcher", func(j *Jira) error { return j.AddIssueWatcher(context.Background(), "ML-1", "alice") }},
		{"RemoveIssueWatcher", func(j *Jira) error { return j.RemoveIssueWatcher(context.Background(), "ML-1", "alice") }},
		{"AddIssueWorklog", func(j *Jira) error {
			_, err := j.AddIssueWorklog(context.Background(), "ML-1", domain.IssueWorklogCreate{TimeSpentSeconds: 60})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := &recordingWriteAuthorizer{err: blocked}
			adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
			before := writes
			err := test.run(adapter)
			if !errors.Is(err, blocked) || len(authorizer.requests) != 1 || writes != before {
				t.Fatalf("error=%v authorizations=%d writes=%d→%d", err, len(authorizer.requests), before, writes)
			}
		})
	}
}

func containsWriteVerb(verbs domain.WriteVerbSet, wanted domain.WriteVerb) bool {
	for _, verb := range verbs {
		if verb == wanted {
			return true
		}
	}
	return false
}
