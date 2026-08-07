package confluence

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

type recordingConfluenceAuthorizer struct {
	requests []domain.WriteAuthorizationRequest
	err      error
	required domain.WriteScopeRequirements
}

func (authorizer *recordingConfluenceAuthorizer) Authorize(ctx context.Context, request domain.WriteAuthorizationRequest) (context.Context, error) {
	authorizer.requests = append(authorizer.requests, request)
	if authorizer.err != nil {
		return ctx, authorizer.err
	}
	return domain.WithWriteClearance(ctx), nil
}

func (authorizer *recordingConfluenceAuthorizer) RequiredWriteScope(string) domain.WriteScopeRequirements {
	return authorizer.required
}

func writeConfluenceMetadata(writer http.ResponseWriter, id, kind, space string, ancestors ...string) {
	writer.Header().Set("Content-Type", "application/json")
	parts := make([]string, len(ancestors))
	for index, ancestor := range ancestors {
		parts[index] = `{"id":"` + ancestor + `","title":"ancestor"}`
	}
	_, _ = io.WriteString(writer, `{"id":"`+id+`","type":"`+kind+`","title":"target","space":{"key":"`+space+`"},"version":{"number":2},"ancestors":[`+strings.Join(parts, ",")+`]}`)
}

func TestEveryConfluenceMutatingTransportSiteInvokesAuthorizer(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writes++
			return
		}
		if strings.HasSuffix(request.URL.Path, "/rest/api/server-information") {
			writeTestExactMetadata(writer)
			return
		}
		id := "10"
		if parts := strings.Split(request.URL.Path, "/"); len(parts) > 0 && domain.ValidConfluenceContentID(parts[len(parts)-1]) {
			id = parts[len(parts)-1]
		}
		writeConfluenceMetadata(writer, id, "page", "DOC")
	}))
	defer server.Close()

	blocked := errors.New("blocked by test authorizer")
	want := map[string]struct {
		verbs domain.WriteVerbSet
		kind  string
		id    string
	}{
		"UpdatePage":         {domain.WriteVerbSet{domain.WriteVerbUpdate}, "page", "10"},
		"CreatePage":         {domain.WriteVerbSet{domain.WriteVerbCreate}, "page", ""},
		"MovePage":           {domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove}, "page", "10"},
		"DeletePage":         {domain.WriteVerbSet{domain.WriteVerbDelete}, "page", "10"},
		"CreateBlogPost":     {domain.WriteVerbSet{domain.WriteVerbCreate}, "blogpost", ""},
		"AddComment":         {domain.WriteVerbSet{domain.WriteVerbComment}, "comment", ""},
		"UploadAttachment":   {domain.WriteVerbSet{domain.WriteVerbCreate}, "attachment", ""},
		"DeleteAttachment":   {domain.WriteVerbSet{domain.WriteVerbDelete}, "attachment", "30"},
		"AddContentLabels":   {domain.WriteVerbSet{domain.WriteVerbUpdate}, "page", "10"},
		"RemoveContentLabel": {domain.WriteVerbSet{domain.WriteVerbUpdate}, "page", "10"},
		"InlineComment":      {domain.WriteVerbSet{domain.WriteVerbComment}, "comment", ""},
		"ReplyComment":       {domain.WriteVerbSet{domain.WriteVerbComment}, "comment", "20"},
		"ResolveComment":     {domain.WriteVerbSet{domain.WriteVerbComment}, "comment", "20"},
	}
	tests := []struct {
		name string
		run  func(*Confluence) error
	}{
		{"UpdatePage", func(cf *Confluence) error {
			_, err := cf.UpdatePage(context.Background(), "10", 1, "T", []byte("x"), false)
			return err
		}},
		{"CreatePage", func(cf *Confluence) error {
			_, err := cf.CreatePage(context.Background(), "DOC", "", "T", []byte("x"))
			return err
		}},
		{"MovePage", func(cf *Confluence) error {
			_, err := cf.MovePage(context.Background(), "10", "20", 1, "T", []byte("x"))
			return err
		}},
		{"DeletePage", func(cf *Confluence) error { return cf.DeletePage(context.Background(), "10") }},
		{"CreateBlogPost", func(cf *Confluence) error {
			_, err := cf.CreateBlogPost(context.Background(), "DOC", "T", []byte("x"))
			return err
		}},
		{"AddComment", func(cf *Confluence) error {
			_, err := cf.AddComment(context.Background(), "10", []byte("x"))
			return err
		}},
		{"UploadAttachment", func(cf *Confluence) error {
			_, err := cf.UploadAttachment(context.Background(), "10", "x.txt", io.NopCloser(strings.NewReader("x")), 1, "")
			return err
		}},
		{"DeleteAttachment", func(cf *Confluence) error { return cf.DeleteAttachment(context.Background(), "10", "30") }},
		{"AddContentLabels", func(cf *Confluence) error {
			return cf.AddContentLabels(context.Background(), "10", []domain.ContentLabel{{Prefix: "global", Name: "x"}})
		}},
		{"RemoveContentLabel", func(cf *Confluence) error { return cf.RemoveContentLabel(context.Background(), "10", "x") }},
		{"InlineComment", func(cf *Confluence) error {
			provider, err := NewCommentMutationProvider(cf, testCommentMutationActivation())
			if err != nil {
				return err
			}
			_, err = provider.MutateConfluenceComment(context.Background(), testInlineCreateMutationRequest())
			return err
		}},
		{"ReplyComment", func(cf *Confluence) error {
			provider, err := NewCommentMutationProvider(cf, testCommentMutationActivation())
			if err != nil {
				return err
			}
			ctx := domain.WithConfluenceCommentContainment(context.Background(), "10", "20")
			_, err = provider.MutateConfluenceComment(ctx, domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationReply, PageID: "10", ThreadID: "20", BodyStorage: []byte("x")})
			return err
		}},
		{"ResolveComment", func(cf *Confluence) error {
			provider, err := NewCommentMutationProvider(cf, testCommentMutationActivation())
			if err != nil {
				return err
			}
			ctx := domain.WithConfluenceCommentContainment(context.Background(), "10", "20")
			_, err = provider.MutateConfluenceComment(ctx, domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationResolve, PageID: "10", ThreadID: "20"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := &recordingConfluenceAuthorizer{err: blocked}
			adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
			before := writes
			err := test.run(adapter)
			if !errors.Is(err, blocked) || len(authorizer.requests) != 1 || writes != before {
				t.Fatalf("error=%v authorizations=%d writes=%d→%d", err, len(authorizer.requests), before, writes)
			}
			got := authorizer.requests[0]
			expected := want[test.name]
			if len(got.Targets) != 1 || got.Targets[0].Kind != expected.kind || got.Targets[0].ID != expected.id || !equalConfluenceWriteVerbs(got.Verbs, expected.verbs) {
				t.Fatalf("authorization=%+v, want verbs=%v kind=%s id=%s", got, expected.verbs, expected.kind, expected.id)
			}
		})
	}
}

func equalConfluenceWriteVerbs(left, right domain.WriteVerbSet) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestConfluenceIdentityResolutionIsConditionalCachedAndSticky(t *testing.T) {
	var reads, writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			reads++
			if strings.Contains(request.URL.Path, "404") {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			if strings.Contains(request.URL.Path, "503") {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			id := strings.TrimPrefix(request.URL.Path, "/rest/api/content/")
			writeConfluenceMetadata(writer, id, "page", "DOC", "1")
			return
		}
		writes++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"version":{"number":2}}`)
	}))
	defer server.Close()

	noMetadata := &recordingConfluenceAuthorizer{}
	plain := New(server.URL, "token", "test", WithWriteAuthorizer(noMetadata))
	if _, err := plain.UpdatePage(context.Background(), "10", 1, "T", []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || writes != 1 {
		t.Fatalf("no-scope reads=%d writes=%d, want 0/1", reads, writes)
	}

	metadata := &recordingConfluenceAuthorizer{required: domain.WriteScopeRequirements{Space: true, Ancestors: true}}
	guarded := New(server.URL, "token", "test", WithWriteAuthorizer(metadata))
	for range 2 {
		if _, err := guarded.UpdatePage(context.Background(), "20", 1, "T", []byte("x"), false); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 || writes != 3 {
		t.Fatalf("cached reads=%d writes=%d, want 1/3", reads, writes)
	}

	allow := contentpolicy.NewAuthorizer(&contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "managed", Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
			ID: "allow", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
			Resource: contentpolicy.Selector{Services: []string{"confluence"}, Spaces: []string{"DOC"}},
		}}},
	}}})
	missing := New(server.URL, "token", "test", WithWriteAuthorizer(allow))
	for range 2 {
		err := func() error {
			_, err := missing.UpdatePage(context.Background(), "404", 1, "T", []byte("x"), false)
			return err
		}()
		var denial *contentpolicy.DenialError
		if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved {
			t.Fatalf("error=%v denial=%+v", err, denial)
		}
	}
	if reads != 2 {
		t.Fatalf("sticky failure reads=%d, want 2 total", reads)
	}
	_, err := missing.UpdatePage(context.Background(), "503", 1, "T", []byte("x"), false)
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnavailable || !denial.RetrySafe || denial.Advice != contentpolicy.AdviceWaitThenRetry {
		t.Fatalf("transient error=%v denial=%+v", err, denial)
	}
	readsAfterTransient := reads
	_, err = missing.UpdatePage(context.Background(), "503", 1, "T", []byte("x"), false)
	denial = nil
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnavailable || reads != readsAfterTransient || writes != 3 {
		t.Fatalf("sticky transient error=%v denial=%+v reads=%d→%d writes=%d", err, denial, readsAfterTransient, reads, writes)
	}
}

func TestConfluenceUnprimedBulkWritesResolveEachDistinctPageOnce(t *testing.T) {
	var reads, writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			reads++
			id := strings.TrimPrefix(request.URL.Path, "/rest/api/content/")
			writeConfluenceMetadata(writer, id, "page", "DOC")
			return
		}
		writes++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"version":{"number":2}}`)
	}))
	defer server.Close()
	allow := policyAuthorizer(contentpolicy.Rule{
		ID: "allow-doc", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Spaces: []string{"DOC"}},
	})
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(allow))
	for _, id := range []string{"10", "20", "30"} {
		if _, err := adapter.UpdatePage(context.Background(), id, 1, "T", []byte("x"), false); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 3 || writes != 3 {
		t.Fatalf("reads=%d writes=%d, want one resolution and write per distinct page", reads, writes)
	}
}

func TestConfluenceIdentityCacheBoundAndSubtreeEviction(t *testing.T) {
	cache := newConfluenceIdentityCache()
	for index := 1; index <= confluenceIdentityCacheLimit; index++ {
		id := strconv.Itoa(index)
		cache.put(confluenceIdentity{id: id, kind: "page", space: "DOC", ancestorIDs: []string{"1"}})
	}
	if _, ok := cache.get("2"); !ok {
		t.Fatal("recent entry missing")
	}
	cache.put(confluenceIdentity{id: "4097", kind: "page", space: "DOC", ancestorIDs: []string{"2"}})
	if _, ok := cache.get("1"); ok {
		t.Fatal("least-recently-used entry retained")
	}
	cache.evictSubtree("2")
	if _, ok := cache.get("2"); ok {
		t.Fatal("moved target retained")
	}
	if _, ok := cache.get("4097"); ok {
		t.Fatal("cached descendant retained")
	}
}

func policyAuthorizer(rules ...contentpolicy.Rule) *contentpolicy.Authorizer {
	return contentpolicy.NewAuthorizer(&contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "managed", Policy: contentpolicy.Policy{Rules: rules},
	}}})
}

func TestConfluenceReferenceProvenanceCannotGroundAllow(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			return
		}
		writeConfluenceMetadata(writer, "10", "page", "DOC")
	}))
	defer server.Close()
	allow := policyAuthorizer(contentpolicy.Rule{
		ID: "allow", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, IDs: []string{"10"}},
	})
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(allow))
	ctx := domain.WithUntrustedConfluenceReference(context.Background())
	_, err := adapter.UpdatePage(ctx, "10", 1, "T", []byte("x"), false)
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved || denial.Attribute != "id" || writes != 0 {
		t.Fatalf("error=%v denial=%+v writes=%d", err, denial, writes)
	}
}

func TestConfluenceCommentThreadRequiresExactContainmentProof(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			return
		}
		writeTestExactMetadata(writer)
	}))
	defer server.Close()
	allow := policyAuthorizer(contentpolicy.Rule{
		ID: "allow-comments", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbComment},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"comment"}},
	})
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(allow))
	provider, err := NewCommentMutationProvider(adapter, testCommentMutationActivation())
	if err != nil {
		t.Fatal(err)
	}
	request := domain.ConfluenceCommentMutationRequest{Operation: domain.ConfluenceCommentMutationResolve, PageID: "10", ThreadID: "20"}
	_, err = provider.MutateConfluenceComment(context.Background(), request)
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved || denial.Attribute != "containment" || writes != 0 {
		t.Fatalf("error=%v denial=%+v writes=%d", err, denial, writes)
	}

	wrong := domain.WithConfluenceCommentContainment(context.Background(), "10", "21")
	_, err = provider.MutateConfluenceComment(wrong, request)
	denial = nil
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved || writes != 0 {
		t.Fatalf("wrong proof error=%v denial=%+v writes=%d", err, denial, writes)
	}
}

func TestConfluenceHierarchyAndContainedContentPolicy(t *testing.T) {
	var reads, writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{}`)
			return
		}
		reads++
		id := strings.TrimPrefix(request.URL.Path, "/rest/api/content/")
		switch id {
		case "30":
			writeConfluenceMetadata(writer, id, "page", "DOC", "10", "20")
		case "40":
			writeConfluenceMetadata(writer, id, "page", "DOC")
		default:
			writeConfluenceMetadata(writer, id, "page", "DOC")
		}
	}))
	defer server.Close()

	allowPageDelete := contentpolicy.Rule{
		ID: "allow-pages", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbDelete},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, Spaces: []string{"DOC"}},
	}
	denyProtected := contentpolicy.Rule{
		ID: "deny-protected", Effect: contentpolicy.EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbDelete},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, Under: []string{"30"}},
	}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(policyAuthorizer(allowPageDelete, denyProtected)))
	err := adapter.DeletePage(context.Background(), "10")
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonProtectedSubtree || denial.RuleID != "deny-protected" || writes != 0 {
		t.Fatalf("protected error=%v denial=%+v writes=%d", err, denial, writes)
	}
	readsBefore := reads
	if err := adapter.DeletePage(context.Background(), "40"); err != nil {
		t.Fatal(err)
	}
	if reads != readsBefore+1 || writes != 1 {
		t.Fatalf("unrelated reads=%d→%d writes=%d, want one target read and one write", readsBefore, reads, writes)
	}

	denyContained := contentpolicy.Rule{
		ID: "deny-attachments", Effect: contentpolicy.EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbDelete},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"attachment"}, Spaces: []string{"DOC"}},
	}
	contained := New(server.URL, "token", "test", WithWriteAuthorizer(policyAuthorizer(allowPageDelete, denyContained)))
	err = contained.DeletePage(context.Background(), "40")
	denial = nil
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonContainedContentDenied || denial.RuleID != "deny-attachments" || writes != 1 {
		t.Fatalf("contained error=%v denial=%+v writes=%d", err, denial, writes)
	}

	denyChildID := denyContained
	denyChildID.ID = "deny-one-attachment"
	denyChildID.Resource.IDs = []string{"99"}
	childID := New(server.URL, "token", "test", WithWriteAuthorizer(policyAuthorizer(allowPageDelete, denyChildID)))
	err = childID.DeletePage(context.Background(), "40")
	denial = nil
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved || denial.Attribute != "id" || denial.RuleID != "deny-one-attachment" || writes != 1 {
		t.Fatalf("child-id error=%v denial=%+v writes=%d", err, denial, writes)
	}

	unrelatedDeny := denyContained
	unrelatedDeny.Resource.Spaces = []string{"ENG"}
	unrelated := New(server.URL, "token", "test", WithWriteAuthorizer(policyAuthorizer(allowPageDelete, unrelatedDeny)))
	if err := unrelated.DeletePage(context.Background(), "40"); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("unrelated contained deny writes=%d, want 2", writes)
	}
}

func TestConfluenceMoveCannotEscapeDenyUnderScope(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			return
		}
		id := strings.TrimPrefix(request.URL.Path, "/rest/api/content/")
		switch id {
		case "30":
			writeConfluenceMetadata(writer, id, "page", "DOC", "10")
		case "40":
			writeConfluenceMetadata(writer, id, "page", "DOC", "10", "30")
		case "60":
			writeConfluenceMetadata(writer, id, "page", "DOC", "10", "30")
		default:
			writeConfluenceMetadata(writer, id, "page", "DOC")
		}
	}))
	defer server.Close()
	rules := []contentpolicy.Rule{
		{ID: "allow-move", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove}, Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, Spaces: []string{"DOC"}}},
		{ID: "deny-delete-under", Effect: contentpolicy.EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbDelete}, Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, Under: []string{"30"}}},
	}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(policyAuthorizer(rules...)))
	_, err := adapter.MovePage(context.Background(), "40", "50", 1, "T", []byte("x"))
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonProtectedSubtree || denial.RuleID != "deny-delete-under" || writes != 0 {
		t.Fatalf("error=%v denial=%+v writes=%d", err, denial, writes)
	}
	if _, err := adapter.MovePage(context.Background(), "40", "60", 1, "T", []byte("x")); err != nil || writes != 1 {
		t.Fatalf("within-scope move error=%v writes=%d", err, writes)
	}
}

func TestConfluenceExistingContentKindMustMatchOperation(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			return
		}
		writeConfluenceMetadata(writer, "40", "blogpost", "DOC")
	}))
	defer server.Close()
	allowPages := contentpolicy.Rule{
		ID: "allow-pages", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, Spaces: []string{"DOC"}},
	}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(policyAuthorizer(allowPages)))
	_, err := adapter.UpdatePage(context.Background(), "40", 1, "T", []byte("x"), false)
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeUnresolved || denial.Attribute != "id" || writes != 0 {
		t.Fatalf("error=%v denial=%+v writes=%d", err, denial, writes)
	}
}

func TestConfluenceTopLevelAndBlogCreateUseResolvedEmptyHierarchy(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writes++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"50","type":"page","space":{"key":"DOC"},"version":{"number":1},"ancestors":[],"body":{"storage":{"value":"x"}}}`)
	}))
	defer server.Close()
	rules := []contentpolicy.Rule{
		{ID: "allow-create", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbCreate}, Resource: contentpolicy.Selector{Services: []string{"confluence"}, Spaces: []string{"DOC"}}},
		{ID: "deny-other-tree", Effect: contentpolicy.EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbCreate}, Resource: contentpolicy.Selector{Services: []string{"confluence"}, Under: []string{"99"}}},
	}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(policyAuthorizer(rules...)))
	if _, err := adapter.CreatePage(context.Background(), "DOC", "", "Top", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CreateBlogPost(context.Background(), "DOC", "Blog", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("writes=%d, want 2", writes)
	}
}

func TestConfluenceMoveAndNestedCreateAuthorizeCanonicalDestinations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			id := strings.TrimPrefix(request.URL.Path, "/rest/api/content/")
			switch id {
			case "10":
				writeConfluenceMetadata(writer, id, "page", "DOC", "1")
			case "20":
				writeConfluenceMetadata(writer, id, "page", "DOC", "1", "2")
			}
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"version":{"number":2},"id":"30","type":"page","space":{"key":"DOC"},"ancestors":[]}`)
	}))
	defer server.Close()
	authorizer := &recordingConfluenceAuthorizer{required: domain.WriteScopeRequirements{Space: true, Ancestors: true}}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	if _, err := adapter.MovePage(context.Background(), "10", "20", 1, "T", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(authorizer.requests) != 2 || !equalConfluenceWriteVerbs(authorizer.requests[0].Verbs, domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove}) ||
		!equalConfluenceWriteVerbs(authorizer.requests[1].Verbs, domain.WriteVerbSet{domain.WriteVerbMove}) || authorizer.requests[1].Targets[0].ID != "20" {
		t.Fatalf("move authorizations=%+v", authorizer.requests)
	}
	authorizer.requests = nil
	if _, err := adapter.CreatePage(context.Background(), "DOC", "20", "T", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(authorizer.requests) != 1 {
		t.Fatalf("create authorizations=%+v", authorizer.requests)
	}
	target := authorizer.requests[0].Targets[0]
	if target.Space != "DOC" || target.ID != "" || !equalStringIDs(target.AncestorIDs, []string{"1", "2", "20"}) {
		t.Fatalf("nested create target=%+v", target)
	}
}

func TestConfluenceSuccessfulMoveEvictsCachedDescendantHierarchy(t *testing.T) {
	reads := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			id := strings.TrimPrefix(request.URL.Path, "/rest/api/content/")
			reads[id]++
			switch id {
			case "10":
				writeConfluenceMetadata(writer, id, "page", "DOC", "1")
			case "20":
				writeConfluenceMetadata(writer, id, "page", "DOC", "1", "2")
			case "30":
				writeConfluenceMetadata(writer, id, "page", "DOC", "1", "10")
			}
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"version":{"number":2}}`)
	}))
	defer server.Close()
	authorizer := &recordingConfluenceAuthorizer{required: domain.WriteScopeRequirements{Space: true, Ancestors: true}}
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(authorizer))
	if _, err := adapter.GetMeta(context.Background(), "30"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.MovePage(context.Background(), "10", "20", 1, "T", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.UpdatePage(context.Background(), "30", 1, "T", []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	if reads["30"] != 2 {
		t.Fatalf("descendant metadata reads=%d, want read before and after move", reads["30"])
	}
}

func TestConfluenceCreateRejectsDeclaredParentSpaceContradiction(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes++
			return
		}
		writeConfluenceMetadata(writer, "20", "page", "ENG")
	}))
	defer server.Close()
	allow := policyAuthorizer(contentpolicy.Rule{
		ID: "allow-doc", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbCreate},
		Resource: contentpolicy.Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, Spaces: []string{"DOC"}},
	})
	adapter := New(server.URL, "token", "test", WithWriteAuthorizer(allow))
	_, err := adapter.CreatePage(context.Background(), "DOC", "20", "T", []byte("x"))
	var denial *contentpolicy.DenialError
	if !errors.As(err, &denial) || denial.Reason != contentpolicy.ReasonScopeContradiction || denial.Attribute != "space" || writes != 0 {
		t.Fatalf("error=%v denial=%+v writes=%d", err, denial, writes)
	}
}

func equalStringIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
