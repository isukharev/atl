package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// recordingStore embeds DocStore so only the methods a test needs are
// implemented; it records the arguments of each thin-wrapper call-through and
// returns canned values/errors so we can pin the service→port contract.
type recordingStore struct {
	domain.DocStore

	// recorded args
	searchQuery     string
	searchLimit     int
	searchCursor    string
	getID           string
	getFormat       string
	getRestrictions bool
	metaID          string
	historyID       string
	treeSpace       string
	treeDepth       int
	commentsID      string
	addCommentID    string
	addBody         []byte
	createSpace     string
	createParent    string
	createTitle     string
	createBody      []byte
	moveID          string
	moveParent      string
	deleteID        string

	// canned returns
	pageRefs          []domain.PageRef
	treeTruncated     bool
	cursor            string
	page              *domain.Resource
	meta              *domain.PageMeta
	versions          []domain.Version
	comments          []domain.Comment
	commentsTruncated bool
	comment           *domain.Comment
	err               error
	omitBody          bool
}

func (s *recordingStore) Search(_ context.Context, q string, limit int, cursor string) ([]domain.PageRef, string, error) {
	s.searchQuery, s.searchLimit, s.searchCursor = q, limit, cursor
	return s.pageRefs, s.cursor, s.err
}

func (s *recordingStore) GetPage(_ context.Context, id string, o domain.PullOpts) (*domain.Resource, error) {
	s.getID, s.getFormat, s.getRestrictions = id, o.Format, o.IncludeRestrictions
	if s.page == nil || s.err != nil {
		return s.page, s.err
	}
	page := *s.page
	if !s.omitBody {
		page.BodyPresent = true
	}
	return &page, nil
}

func (s *recordingStore) GetMeta(_ context.Context, id string) (*domain.PageMeta, error) {
	s.metaID = id
	return s.meta, s.err
}

func (s *recordingStore) History(_ context.Context, id string) ([]domain.Version, error) {
	s.historyID = id
	return s.versions, s.err
}

func (s *recordingStore) Tree(_ context.Context, space string, depth int) ([]domain.PageRef, bool, error) {
	s.treeSpace, s.treeDepth = space, depth
	return s.pageRefs, s.treeTruncated, s.err
}

func (s *recordingStore) ListComments(_ context.Context, id string) ([]domain.Comment, bool, error) {
	s.commentsID = id
	return s.comments, s.commentsTruncated, s.err
}

func (s *recordingStore) AddComment(_ context.Context, id string, body []byte) (*domain.Comment, error) {
	s.addCommentID, s.addBody = id, body
	return s.comment, s.err
}

func (s *recordingStore) CreatePage(_ context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	s.createSpace, s.createParent, s.createTitle, s.createBody = space, parent, title, body
	return s.page, s.err
}

func (s *recordingStore) MovePage(_ context.Context, id, parent string, _ int, _ string, _ []byte) (int, error) {
	s.moveID, s.moveParent = id, parent
	return 0, s.err
}

func (s *recordingStore) DeletePage(_ context.Context, id string) error {
	s.deleteID = id
	return s.err
}

// ---- thin wrapper call-through ----

func TestConfluenceWrappersPassThrough(t *testing.T) {
	ctx := context.Background()

	t.Run("Search", func(t *testing.T) {
		st := &recordingStore{pageRefs: []domain.PageRef{{ID: "1"}}, cursor: "c2"}
		svc := &ConfluenceService{store: st}
		refs, cur, err := svc.Search(ctx, "type=page", 25, "c1")
		if err != nil {
			t.Fatal(err)
		}
		if st.searchQuery != "type=page" || st.searchLimit != 25 || st.searchCursor != "c1" {
			t.Errorf("args not forwarded: %q %d %q", st.searchQuery, st.searchLimit, st.searchCursor)
		}
		if len(refs) != 1 || cur != "c2" {
			t.Errorf("return not propagated: %v %q", refs, cur)
		}
	})

	t.Run("Get", func(t *testing.T) {
		st := &recordingStore{page: &domain.Resource{ID: "42"}}
		svc := &ConfluenceService{store: st}
		got, err := svc.Get(ctx, "42", "view")
		if err != nil {
			t.Fatal(err)
		}
		if st.getID != "42" || st.getFormat != "view" {
			t.Errorf("args not forwarded: %q %q", st.getID, st.getFormat)
		}
		if got.ID != "42" {
			t.Errorf("return not propagated: %+v", got)
		}
	})

	t.Run("Get rejects partial body projection", func(t *testing.T) {
		for _, tc := range []struct{ format, projection string }{{"csf", "body.storage.value"}, {"view", "body.view.value"}} {
			st := &recordingStore{page: &domain.Resource{ID: "42"}, omitBody: true}
			svc := &ConfluenceService{store: st}
			if _, err := svc.Get(ctx, "42", tc.format); !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), tc.projection) {
				t.Fatalf("partial %s page get error = %v", tc.format, err)
			}
		}
	})

	t.Run("Get accepts explicit empty body projection", func(t *testing.T) {
		st := &recordingStore{page: &domain.Resource{ID: "42", Body: []byte{}}}
		svc := &ConfluenceService{store: st}
		if got, err := svc.Get(ctx, "42", "csf"); err != nil || !got.BodyPresent || len(got.Body) != 0 {
			t.Fatalf("explicit empty page get = %+v, %v", got, err)
		}
	})

	t.Run("Meta", func(t *testing.T) {
		st := &recordingStore{meta: &domain.PageMeta{ID: "7", Version: 3}}
		svc := &ConfluenceService{store: st}
		got, err := svc.Meta(ctx, "7")
		if err != nil {
			t.Fatal(err)
		}
		if st.metaID != "7" || got.Version != 3 {
			t.Errorf("meta mismatch: id=%q ret=%+v", st.metaID, got)
		}
	})

	t.Run("History", func(t *testing.T) {
		st := &recordingStore{versions: []domain.Version{{Number: 2}}}
		svc := &ConfluenceService{store: st}
		got, err := svc.History(ctx, "9")
		if err != nil {
			t.Fatal(err)
		}
		if st.historyID != "9" || len(got) != 1 || got[0].Number != 2 {
			t.Errorf("history mismatch: id=%q ret=%+v", st.historyID, got)
		}
	})

	t.Run("Tree", func(t *testing.T) {
		st := &recordingStore{pageRefs: []domain.PageRef{{ID: "a"}, {ID: "b"}}}
		svc := &ConfluenceService{store: st}
		got, _, err := svc.Tree(ctx, "SPACE", 4)
		if err != nil {
			t.Fatal(err)
		}
		if st.treeSpace != "SPACE" || st.treeDepth != 4 || len(got) != 2 {
			t.Errorf("tree mismatch: space=%q depth=%d ret=%+v", st.treeSpace, st.treeDepth, got)
		}
	})

	t.Run("Comments", func(t *testing.T) {
		st := &recordingStore{comments: []domain.Comment{{ID: "c1"}}}
		svc := &ConfluenceService{store: st}
		got, _, err := svc.Comments(ctx, "p1")
		if err != nil {
			t.Fatal(err)
		}
		if st.commentsID != "p1" || len(got) != 1 {
			t.Errorf("comments mismatch: id=%q ret=%+v", st.commentsID, got)
		}
	})

	t.Run("AddComment", func(t *testing.T) {
		st := &recordingStore{comment: &domain.Comment{ID: "new"}}
		svc := &ConfluenceService{store: st}
		got, err := svc.AddComment(ctx, "p1", []byte("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if st.addCommentID != "p1" || string(st.addBody) != "hi" || got.ID != "new" {
			t.Errorf("addcomment mismatch: id=%q body=%q ret=%+v", st.addCommentID, st.addBody, got)
		}
	})

	t.Run("Create", func(t *testing.T) {
		st := &recordingStore{page: &domain.Resource{ID: "created"}}
		svc := &ConfluenceService{store: st}
		got, err := svc.Create(ctx, "SP", "parent1", "Title", []byte("<p/>"))
		if err != nil {
			t.Fatal(err)
		}
		if st.createSpace != "SP" || st.createParent != "parent1" || st.createTitle != "Title" || string(st.createBody) != "<p/>" {
			t.Errorf("create args not forwarded: %+v", st)
		}
		if got.ID != "created" {
			t.Errorf("create return not propagated: %+v", got)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		st := &recordingStore{}
		svc := &ConfluenceService{store: st}
		if err := svc.Delete(ctx, "id9"); err != nil {
			t.Fatal(err)
		}
		if st.deleteID != "id9" {
			t.Errorf("delete id not forwarded: %q", st.deleteID)
		}
	})
}

// A sentinel error from the port must propagate unchanged through the thin
// wrappers so the CLI's codeFor can map it to the right exit code.
func TestConfluenceWrappersPropagateSentinel(t *testing.T) {
	ctx := context.Background()
	st := &recordingStore{err: domain.ErrNotFound}
	svc := &ConfluenceService{store: st}

	if _, err := svc.Get(ctx, "x", "csf"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get did not propagate sentinel: %v", err)
	}
	if _, err := svc.Meta(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Meta did not propagate sentinel: %v", err)
	}
	if _, err := svc.History(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("History did not propagate sentinel: %v", err)
	}
	if _, _, err := svc.Tree(ctx, "x", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Tree did not propagate sentinel: %v", err)
	}
	if _, _, err := svc.Search(ctx, "x", 1, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Search did not propagate sentinel: %v", err)
	}
	if _, _, err := svc.Comments(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Comments did not propagate sentinel: %v", err)
	}
	if _, err := svc.AddComment(ctx, "x", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("AddComment did not propagate sentinel: %v", err)
	}
	if _, err := svc.Create(ctx, "x", "", "t", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Create did not propagate sentinel: %v", err)
	}
	if err := svc.Delete(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete did not propagate sentinel: %v", err)
	}
}

// Validate delegates to csf.Validate; malformed CSF must surface error-severity
// problems.
func TestConfluenceValidate(t *testing.T) {
	svc := &ConfluenceService{}
	clean := svc.Validate([]byte("<p>ok</p>"))
	if csf.HasErrors(clean) {
		t.Errorf("clean body reported errors: %+v", clean)
	}
	bad := svc.Validate([]byte("<p>unterminated"))
	if !csf.HasErrors(bad) {
		t.Errorf("malformed body should report an error-severity problem, got %+v", bad)
	}
}

// ---- attachment download ----

// brokenStream yields a prefix then fails — a mid-download transport error.
type brokenStream struct{ n int }

func (r *brokenStream) Read(p []byte) (int, error) {
	if r.n == 0 {
		r.n++
		copy(p, "part")
		return 4, nil
	}
	return 0, errors.New("connection reset mid-body")
}
func (r *brokenStream) Close() error { return nil }

type downloadStore struct {
	domain.DocStore
	rc io.ReadCloser
}

func (s *downloadStore) DownloadAttachment(context.Context, string, string, int) (io.ReadCloser, error) {
	return s.rc, nil
}

// TestDownloadAttachmentPartialFailureLeavesNoFile: an interrupted stream must
// not plant a truncated file at the destination (atomic write path).
func TestDownloadAttachmentPartialFailureLeavesNoFile(t *testing.T) {
	outDir := t.TempDir()
	svc := &ConfluenceService{store: &downloadStore{rc: &brokenStream{}}}
	_, err := svc.DownloadAttachment(context.Background(), "55", "big.bin", 0, outDir)
	if err == nil {
		t.Fatal("mid-stream failure must propagate")
	}
	ents, _ := os.ReadDir(outDir)
	for _, e := range ents {
		t.Errorf("unexpected file after failed download: %s", e.Name())
	}
}

// ---- resolveIDs branches ----

func TestResolveIDsBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("byID", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{}}
		ids, _, err := svc.resolveIDs(ctx, PullOpts{ID: "555"})
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != "555" {
			t.Errorf("byID resolve = %v, want [555]", ids)
		}
	})

	t.Run("bySpace", func(t *testing.T) {
		st := &recordingStore{pageRefs: []domain.PageRef{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
		svc := &ConfluenceService{store: st}
		ids, _, err := svc.resolveIDs(ctx, PullOpts{Space: "ENG", Depth: 2})
		if err != nil {
			t.Fatal(err)
		}
		if st.treeSpace != "ENG" || st.treeDepth != 2 {
			t.Errorf("tree args not forwarded: %q %d", st.treeSpace, st.treeDepth)
		}
		if strings.Join(ids, ",") != "a,b,c" {
			t.Errorf("bySpace resolve = %v", ids)
		}
	})

	t.Run("byCQL", func(t *testing.T) {
		st := &recordingStore{pageRefs: []domain.PageRef{{ID: "x"}, {ID: ""}, {ID: "y"}}}
		svc := &ConfluenceService{store: st}
		ids, truncated, err := svc.resolveIDs(ctx, PullOpts{CQL: "label = foo"})
		if err != nil {
			t.Fatal(err)
		}
		if truncated {
			t.Errorf("a single page below the cap must not be truncated")
		}
		// empty IDs are skipped; single page with no cursor terminates.
		if strings.Join(ids, ",") != "x,y" {
			t.Errorf("byCQL resolve = %v, want [x y]", ids)
		}
		if st.searchQuery != "label = foo" || st.searchLimit != 100 {
			t.Errorf("search args not forwarded: %q limit=%d", st.searchQuery, st.searchLimit)
		}
	})

	t.Run("noSelector", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{}}
		_, _, err := svc.resolveIDs(ctx, PullOpts{})
		if !errors.Is(err, domain.ErrUsage) {
			t.Errorf("missing selector should be ErrUsage, got %v", err)
		}
	})

	t.Run("treeErrPropagates", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{err: domain.ErrForbidden}}
		_, _, err := svc.resolveIDs(ctx, PullOpts{Space: "X"})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("tree error should propagate, got %v", err)
		}
	})

	t.Run("searchErrPropagates", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{err: domain.ErrAuth}}
		_, _, err := svc.resolveIDs(ctx, PullOpts{CQL: "x"})
		if !errors.Is(err, domain.ErrAuth) {
			t.Errorf("search error should propagate, got %v", err)
		}
	})
}

// ---- THE SILENT CQL 1000-CAP ----

// infiniteSearchStore always returns a full page of fresh ids plus a non-empty
// next cursor, so collectSearch would loop forever if it did not enforce a cap.
type infiniteSearchStore struct {
	domain.DocStore
	calls int
}

func (s *infiniteSearchStore) Search(_ context.Context, _ string, limit int, _ string) ([]domain.PageRef, string, error) {
	s.calls++
	refs := make([]domain.PageRef, 0, limit)
	for i := 0; i < limit; i++ {
		// unique id per hit so dedup (if any) does not shrink the count
		refs = append(refs, domain.PageRef{ID: idFor(s.calls, i)})
	}
	return refs, "more", nil // always a next cursor -> never terminates naturally
}

func idFor(call, i int) string {
	var b strings.Builder
	b.WriteString("p")
	writeInt(&b, call)
	b.WriteString("-")
	writeInt(&b, i)
	return b.String()
}

func writeInt(b *strings.Builder, n int) {
	if n == 0 {
		b.WriteByte('0')
		return
	}
	var digits []byte
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		b.WriteByte(digits[i])
	}
}

// collectSearch caps at exactly 1000 ids without erroring, but now reports the
// truncation via its boolean so the overflow is not silently dropped. This is
// the documented CQL pull cap.
func TestCollectSearchSilent1000Cap(t *testing.T) {
	st := &infiniteSearchStore{}
	svc := &ConfluenceService{store: st}
	ids, truncated, err := svc.collectSearch(context.Background(), "type = page")
	if err != nil {
		t.Fatalf("collectSearch returned an error; the cap must not error: %v", err)
	}
	if len(ids) != 1000 {
		t.Fatalf("CQL cap = %d ids, want exactly 1000 (truncation)", len(ids))
	}
	if !truncated {
		t.Errorf("collectSearch hit the cap with more results pending; truncated must be true")
	}
	// 100-per-page * 10 pages == 1000 to reach the cap, then one extra probe call
	// confirms more results exist before flagging truncation.
	if st.calls != 11 {
		t.Errorf("expected 10 search calls to reach 1000 + 1 truncation probe, got %d", st.calls)
	}
}

// At exactly the cap with no further page, collectSearch must NOT flag
// truncation: the one-row probe past the cap comes back empty.
func TestCollectSearchExactCapNotTruncated(t *testing.T) {
	st := &exactlyCapStore{}
	svc := &ConfluenceService{store: st}
	ids, truncated, err := svc.collectSearch(context.Background(), "type = page")
	if err != nil {
		t.Fatalf("collectSearch: %v", err)
	}
	if len(ids) != 1000 {
		t.Fatalf("got %d ids, want exactly 1000", len(ids))
	}
	if truncated {
		t.Errorf("results ended exactly at the cap; the probe is empty so truncated must be false")
	}
	if st.calls != 11 {
		t.Errorf("expected 10 fill calls + 1 (empty) probe, got %d", st.calls)
	}
}

// exactlyCapStore serves 10 full pages (1000 ids) each advertising a next
// cursor, but the 11th call (the truncation probe) returns no results — modeling
// a query whose matches end precisely at the cap.
type exactlyCapStore struct {
	domain.DocStore
	calls int
}

func (s *exactlyCapStore) Search(_ context.Context, _ string, limit int, _ string) ([]domain.PageRef, string, error) {
	s.calls++
	if s.calls > 10 {
		return nil, "", nil // probe past the cap: nothing more
	}
	refs := make([]domain.PageRef, 0, limit)
	for i := 0; i < limit; i++ {
		refs = append(refs, domain.PageRef{ID: idFor(s.calls, i)})
	}
	return refs, "more", nil
}

// collectSearch must NOT flag truncation when the backend runs out of results at
// or before the cap — a natural end is not a truncation.
func TestCollectSearchNotTruncatedWhenExhausted(t *testing.T) {
	st := &finiteSearchStore{pages: 3} // 300 ids then an empty next cursor
	svc := &ConfluenceService{store: st}
	ids, truncated, err := svc.collectSearch(context.Background(), "type = page")
	if err != nil {
		t.Fatalf("collectSearch: %v", err)
	}
	if len(ids) != 300 {
		t.Fatalf("got %d ids, want 300", len(ids))
	}
	if truncated {
		t.Errorf("query was exhausted naturally; truncated must be false")
	}
}

// Pull surfaces the cap and its truncation flag through the public entrypoint.
func TestPullCQLSilent1000Cap(t *testing.T) {
	st := &pullCapStore{}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{CQL: "type = page", Into: t.TempDir()})
	if err != nil {
		t.Fatalf("Pull with capped CQL must not error: %v", err)
	}
	if len(res.Pages) != 1000 {
		t.Fatalf("Pull mirrored %d pages, want exactly 1000 (CQL cap)", len(res.Pages))
	}
	if !res.Truncated || res.TruncatedAt != 1000 {
		t.Errorf("Pull result truncated=%v at=%d, want true at 1000", res.Truncated, res.TruncatedAt)
	}
}

// finiteSearchStore serves `pages` full pages of fresh ids, then signals the end
// with an empty next cursor — exercising the natural-termination path.
type finiteSearchStore struct {
	domain.DocStore
	pages int
	calls int
}

func (s *finiteSearchStore) Search(_ context.Context, _ string, limit int, _ string) ([]domain.PageRef, string, error) {
	s.calls++
	refs := make([]domain.PageRef, 0, limit)
	for i := 0; i < limit; i++ {
		refs = append(refs, domain.PageRef{ID: idFor(s.calls, i)})
	}
	if s.calls >= s.pages {
		return refs, "", nil // last page: no further cursor
	}
	return refs, "more", nil
}

// pullCapStore behaves like infiniteSearchStore for Search but also serves a
// minimal page for every GetPage so Pull can mirror each capped id.
type pullCapStore struct {
	domain.DocStore
	calls int
}

func (s *pullCapStore) Search(_ context.Context, _ string, limit int, _ string) ([]domain.PageRef, string, error) {
	s.calls++
	refs := make([]domain.PageRef, 0, limit)
	for i := 0; i < limit; i++ {
		refs = append(refs, domain.PageRef{ID: idFor(s.calls, i)})
	}
	return refs, "more", nil
}

func (s *pullCapStore) GetPage(_ context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	return &domain.Resource{ID: id, Title: id, SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>"), BodyPresent: true}, nil
}

// ---- Pull orchestration ----

// pullStore serves a fixed set of pages by id for Pull orchestration tests.
type pullStore struct {
	domain.DocStore
	pages        map[string]*domain.Resource
	refs         []domain.PageRef // served by Tree for --space pulls
	getErr       error
	getErrs      map[string]error
	lastPullOpts domain.PullOpts

	// comment plumbing for `pull --comments` tests.
	comments          map[string][]domain.Comment // per-id comments to serve
	commentsTruncated map[string]bool             // per-id truncation flag
	commentsErr       error                       // forces a ListComments failure
	listCommentsCalls int                         // how many times ListComments ran
	omitBody          bool                        // simulates a successful partial body projection
}

func (s *pullStore) Tree(_ context.Context, _ string, _ int) ([]domain.PageRef, bool, error) {
	return s.refs, false, nil
}

func (s *pullStore) ListComments(_ context.Context, id string) ([]domain.Comment, bool, error) {
	s.listCommentsCalls++
	if s.commentsErr != nil {
		return nil, false, s.commentsErr
	}
	return s.comments[id], s.commentsTruncated[id], nil
}

func (s *pullStore) GetPage(_ context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	s.lastPullOpts = opts
	if s.getErrs != nil {
		if err, ok := s.getErrs[id]; ok {
			return nil, err
		}
	}
	if s.getErr != nil {
		return nil, s.getErr
	}
	if p, ok := s.pages[id]; ok {
		copy := *p
		if !s.omitBody {
			copy.BodyPresent = true
		}
		if !opts.IncludeRestrictions {
			copy.Restricted = nil
		}
		return &copy, nil
	}
	return nil, domain.ErrNotFound
}

func TestPullProjectsAndPersistsConfiguredPageFields(t *testing.T) {
	into := t.TempDir()
	restricted := true
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {
			ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Parent: "9",
			Ancestors: []string{"Home", "Docs"}, Labels: []string{"one"},
			Updated: "2026-07-10T12:00:00Z", Restricted: &restricted, Body: []byte("<p>alpha</p>"),
		},
	}}
	svc := &ConfluenceService{store: st}
	view := config.RenderService{
		Profile: "minimal", Include: []string{SecPageFields},
		PageFields: []config.ConfluenceFieldView{{ID: "restricted"}, {ID: "ancestors"}, {ID: "updated"}},
	}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into, Render: view})
	if err != nil {
		t.Fatal(err)
	}
	if !st.lastPullOpts.IncludeRestrictions {
		t.Fatal("pull did not request configured restriction metadata")
	}
	metaPath := strings.TrimSuffix(filepath.Join(into, res.Pages[0].Path), ".csf") + ".meta.json"
	var meta mirror.Meta
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Restricted == nil || !*meta.Restricted || !reflect.DeepEqual(meta.Ancestors, []string{"Home", "Docs"}) || meta.Updated == "" {
		t.Fatalf("typed metadata not persisted: %+v", meta)
	}

	// A narrower re-pull must clear the previously stored restriction fact.
	if _, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into}); err != nil {
		t.Fatal(err)
	}
	metaBytes, err = os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	meta = mirror.Meta{}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Restricted != nil {
		t.Fatalf("narrow re-pull retained stale restriction state: %+v", meta.Restricted)
	}
}

func TestPullMirrorsPages(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Body: []byte("<p>alpha</p>")},
	}}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("expected 1 mirrored page, got %d", len(res.Pages))
	}
	p := res.Pages[0]
	if p.ID != "100" || p.Title != "Alpha" || p.Version != 2 {
		t.Errorf("mirrored page meta wrong: %+v", p)
	}
	// the .csf file must exist on disk under the mirror root
	csfPath := filepath.Join(into, p.Path)
	body, rerr := os.ReadFile(csfPath)
	if rerr != nil {
		t.Fatalf("expected csf at %s: %v", csfPath, rerr)
	}
	// Byte-for-byte: the .csf must be the exact server body, never re-encoded
	// (the no-Markdown-round-trip invariant).
	if string(body) != "<p>alpha</p>" {
		t.Errorf("csf body not written verbatim: got %q, want %q", body, "<p>alpha</p>")
	}
}

func TestPullRelocatesChangedPagePathWithoutDeletingDescendants(t *testing.T) {
	into := t.TempDir()
	page := &domain.Resource{ID: "100", Title: "Old", SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>")}
	st := &pullStore{pages: map[string]*domain.Resource{"100": page}}
	svc := &ConfluenceService{store: st}
	first, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatal(err)
	}
	oldCSF := filepath.Join(into, first.Pages[0].Path)
	oldDir := filepath.Dir(oldCSF)
	child := filepath.Join(oldDir, "child", "child.csf")
	if err := os.MkdirAll(filepath.Dir(child), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("descendant"), 0o644); err != nil {
		t.Fatal(err)
	}
	page.Title, page.Version = "New", 2
	second, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatal(err)
	}
	if second.Pages[0].Path == first.Pages[0].Path {
		t.Fatal("title change did not produce a relocation control case")
	}
	for _, path := range []string{oldCSF, strings.TrimSuffix(oldCSF, ".csf") + ".md", strings.TrimSuffix(oldCSF, ".csf") + ".meta.json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old primary artifact survived relocation: %s (%v)", path, err)
		}
	}
	if got, err := os.ReadFile(child); err != nil || string(got) != "descendant" {
		t.Fatalf("descendant was not preserved: %q, %v", got, err)
	}
	lc, _, err := mirror.New(into).LoadCSF(filepath.Join(into, second.Pages[0].Path))
	if err != nil || lc.Synced == nil || lc.Synced.Path != second.Pages[0].Path {
		t.Fatalf("new path is not canonical in sidecar: lc=%+v err=%v", lc, err)
	}
}

func TestPullRelocationRefusesUnappliedMarkdownEdit(t *testing.T) {
	into := t.TempDir()
	page := &domain.Resource{ID: "100", Title: "Old", SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>")}
	st := &pullStore{pages: map[string]*domain.Resource{"100": page}}
	svc := &ConfluenceService{store: st}
	first, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatal(err)
	}
	oldCSF := filepath.Join(into, first.Pages[0].Path)
	oldMD := strings.TrimSuffix(oldCSF, ".csf") + ".md"
	f, err := os.OpenFile(oldMD, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nlocal edit\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	page.Title, page.Version = "New", 2
	_, err = svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "unapplied Markdown edits") {
		t.Fatalf("local Markdown relocation error = %v", err)
	}
	if _, err := os.Stat(oldCSF); err != nil {
		t.Fatalf("refused relocation removed old native artifact: %v", err)
	}
}

func TestPullRelocationRecoversWhenOldPrimaryArtifactsWereRemoved(t *testing.T) {
	into := t.TempDir()
	page := &domain.Resource{ID: "100", Title: "Old", SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>")}
	st := &pullStore{pages: map[string]*domain.Resource{"100": page}}
	svc := &ConfluenceService{store: st}
	first, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatal(err)
	}
	oldCSF := filepath.Join(into, first.Pages[0].Path)
	oldBase := strings.TrimSuffix(oldCSF, ".csf")
	retained := filepath.Join(filepath.Dir(oldCSF), "local-notes.txt")
	if err := os.WriteFile(retained, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".csf", ".md", ".meta.json"} {
		if err := os.Remove(oldBase + suffix); err != nil {
			t.Fatal(err)
		}
	}
	page.Title, page.Version = "New", 2
	second, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatalf("re-pull after deliberate old-path cleanup: %v", err)
	}
	if second.Pages[0].Path == first.Pages[0].Path {
		t.Fatal("replacement path did not change")
	}
	lc, _, err := mirror.New(into).LoadCSF(filepath.Join(into, second.Pages[0].Path))
	if err != nil || lc.Synced == nil || lc.Synced.Path != second.Pages[0].Path {
		t.Fatalf("replacement did not repair canonical state: lc=%+v err=%v", lc, err)
	}
	if got, err := os.ReadFile(retained); err != nil || string(got) != "preserve" {
		t.Fatalf("retained old-directory bytes changed: %q, %v", got, err)
	}
	oldSlug := strings.TrimSuffix(filepath.Base(oldCSF), ".csf")
	if _, err := os.Stat(filepath.Join(filepath.Dir(oldCSF), oldSlug+".relocated.json")); err != nil {
		t.Fatalf("recovery did not reserve retained old directory: %v", err)
	}
	otherDir, _, err := mirror.New(into).ClaimPageDir("SP", nil, "Old", "200")
	if err != nil {
		t.Fatal(err)
	}
	if otherDir == filepath.Dir(oldCSF) {
		t.Fatal("another page inherited the recovered page's retained directory")
	}
}

func TestPullRelocationRecoversWhenWholeOldLeafDirectoryWasRemoved(t *testing.T) {
	into := t.TempDir()
	page := &domain.Resource{ID: "100", Title: "Old", SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>")}
	st := &pullStore{pages: map[string]*domain.Resource{"100": page}}
	svc := &ConfluenceService{store: st}
	first, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatal(err)
	}
	oldCSF := filepath.Join(into, first.Pages[0].Path)
	oldBase := strings.TrimSuffix(oldCSF, ".csf")
	for _, suffix := range []string{".csf", ".md", ".meta.json"} {
		if err := os.Remove(oldBase + suffix); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Dir(oldCSF)); err != nil {
		t.Fatal(err)
	}
	page.Title, page.Version = "New", 2
	second, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatalf("re-pull after whole-directory cleanup: %v", err)
	}
	if second.Pages[0].Path == first.Pages[0].Path {
		t.Fatal("replacement path did not change")
	}
}

func TestPullRelocationRejectsPartiallyRemovedOldPrimaryArtifacts(t *testing.T) {
	for _, suffix := range []string{".csf", ".md", ".meta.json"} {
		t.Run(suffix, func(t *testing.T) {
			into := t.TempDir()
			page := &domain.Resource{ID: "100", Title: "Old", SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>")}
			st := &pullStore{pages: map[string]*domain.Resource{"100": page}}
			svc := &ConfluenceService{store: st}
			first, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
			if err != nil {
				t.Fatal(err)
			}
			oldBase := strings.TrimSuffix(filepath.Join(into, first.Pages[0].Path), ".csf")
			if err := os.Remove(oldBase + suffix); err != nil {
				t.Fatal(err)
			}
			page.Title, page.Version = "New", 2
			_, err = svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
			if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "partially present") {
				t.Fatalf("partial old source error = %v", err)
			}
		})
	}
}

func TestPullRelocationMigratesByteCleanLegacyView(t *testing.T) {
	for _, marker := range []string{"<!-- atl:document confluence-page v3 -->", "<!-- atl:document confluence-page v2 -->", "<!-- atl:document confluence-page v1 -->", "<!-- atl:document confluence-page -->"} {
		t.Run(marker, func(t *testing.T) {
			into := t.TempDir()
			page := &domain.Resource{ID: "100", Title: "Old", SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>")}
			st := &pullStore{pages: map[string]*domain.Resource{"100": page}}
			svc := &ConfluenceService{store: st}
			first, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
			if err != nil {
				t.Fatal(err)
			}
			mdPath := strings.TrimSuffix(filepath.Join(into, first.Pages[0].Path), ".csf") + ".md"
			md, err := os.ReadFile(mdPath)
			if err != nil {
				t.Fatal(err)
			}
			legacy := strings.Replace(string(md), mirror.ConfluenceDocumentMarker, marker, 1)
			if err := os.WriteFile(mdPath, []byte(legacy), 0o644); err != nil {
				t.Fatal(err)
			}
			page.Title, page.Version = "New", 2
			res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
			if err != nil || len(res.Pages) != 1 {
				t.Fatalf("legacy relocation result=%+v error=%v", res, err)
			}
			newMD := strings.TrimSuffix(filepath.Join(into, res.Pages[0].Path), ".csf") + ".md"
			migrated, err := os.ReadFile(newMD)
			if err != nil || mirror.ConfluenceDocumentMarkerLine(string(migrated)) != mirror.ConfluenceDocumentMarker {
				t.Fatalf("migrated view marker=%q error=%v", mirror.ConfluenceDocumentMarkerLine(string(migrated)), err)
			}
			if _, err := os.Stat(mdPath); !os.IsNotExist(err) {
				t.Fatalf("legacy path was not retired: %v", err)
			}
		})
	}
}

func TestPullRejectsMissingNativeBodyBeforeWritingArtifacts(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{
		omitBody: true,
		pages: map[string]*domain.Resource{
			"100": {ID: "100", Title: "Partial", SpaceKey: "SP", Version: 2},
		},
	}
	svc := &ConfluenceService{store: st}
	_, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "native body") {
		t.Fatalf("partial projection error = %v, want check failure", err)
	}
	var artifacts []string
	if walkErr := filepath.WalkDir(into, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && (strings.HasSuffix(path, ".csf") || strings.HasSuffix(path, ".meta.json")) {
			artifacts = append(artifacts, path)
		}
		return nil
	}); walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(artifacts) != 0 {
		t.Fatalf("partial projection wrote page artifacts: %v", artifacts)
	}
}

func TestPullAcceptsExplicitlyEmptyNativeBody(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Empty", SpaceKey: "SP", Version: 2, Body: []byte{}},
	}}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatalf("explicit empty body: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(into, res.Pages[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("mirrored body = %q, want explicitly empty", body)
	}
}

// A page body that fails CSF parsing must still be mirrored: the render/fragment
// path is best-effort and a parse failure is swallowed, never failing the pull.
func TestPullSwallowsParseFailure(t *testing.T) {
	into := t.TempDir()
	// Unterminated tag -> csf.Parse errors -> fragment extraction is skipped, but
	// Pull must continue and still write the page.
	st := &pullStore{pages: map[string]*domain.Resource{
		"200": {ID: "200", Title: "Broken", SpaceKey: "SP", Version: 1, Body: []byte("<p>unterminated")},
	}}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "200", Into: into})
	if err != nil {
		t.Fatalf("a parse/render failure must not fail the pull, got %v", err)
	}
	if len(res.Pages) != 1 || res.Pages[0].Assets != 0 {
		t.Fatalf("expected 1 page with 0 assets, got %+v", res.Pages)
	}
	if _, err := os.Stat(filepath.Join(into, res.Pages[0].Path)); err != nil {
		t.Errorf("page with unparseable body was not mirrored: %v", err)
	}
	// The .md view of the unparseable revision is an explicit stub, and a
	// stale render from an earlier good revision never survives (issue #76).
	mdPath := strings.TrimSuffix(filepath.Join(into, res.Pages[0].Path), ".csf") + ".md"
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("no .md next to the unparseable revision: %v", err)
	}
	if !strings.Contains(string(md), "markdown view unavailable") {
		t.Errorf(".md = %q, want the render-unavailable stub", md)
	}

	// Now the reverse order: good revision first, broken one after — the v1
	// render must be replaced by the stub, not left stale.
	st.pages["200"].Body = []byte("<p>good v1</p>")
	if _, err := svc.Pull(context.Background(), PullOpts{ID: "200", Into: into}); err != nil {
		t.Fatal(err)
	}
	if md, _ := os.ReadFile(mdPath); !strings.Contains(string(md), "good v1") {
		t.Fatalf("v1 render missing: %q", md)
	}
	st.pages["200"].Body = []byte("<p>broken v2")
	if _, err := svc.Pull(context.Background(), PullOpts{ID: "200", Into: into}); err != nil {
		t.Fatal(err)
	}
	md, _ = os.ReadFile(mdPath)
	if strings.Contains(string(md), "good v1") {
		t.Errorf("stale v1 .md survived the broken v2 pull: %q", md)
	}
	if !strings.Contains(string(md), "markdown view unavailable") {
		t.Errorf(".md after broken v2 = %q, want the stub", md)
	}
}

// A failure mid-pull still persists sidecar entries for the pages already
// written (deferred batch flush), so a partial pull is not reported as
// never-synced — and the error still propagates for the exit code.
func TestPullMidFailureFlushesSidecar(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{
		refs: []domain.PageRef{{ID: "1"}, {ID: "2"}},
		pages: map[string]*domain.Resource{
			"1": {ID: "1", Title: "One", SpaceKey: "SP", Version: 3, Body: []byte("<p>1</p>")},
		},
		getErrs: map[string]error{"2": domain.ErrForbidden},
	}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{Space: "SP", Into: into})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected the page-2 failure to propagate, got %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("expected page 1 mirrored before the failure, got %+v", res.Pages)
	}
	lc, _, err := mirror.New(into).LoadCSF(filepath.Join(into, res.Pages[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	if lc.Synced == nil || lc.Synced.Version != 3 {
		t.Errorf("page 1 sidecar entry not flushed on mid-pull failure: %+v", lc.Synced)
	}
	if lc.Dirty {
		t.Error("page 1 should read clean after the partial pull")
	}
}

// A corrupt sidecar aborts a pull loudly before any network write — never a
// silent state reset.
func TestPullCorruptSidecarFailsLoudly(t *testing.T) {
	into := t.TempDir()
	if err := os.MkdirAll(filepath.Join(into, ".atl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(into, ".atl", "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &pullStore{pages: map[string]*domain.Resource{
		"1": {ID: "1", Title: "One", SpaceKey: "SP", Version: 1, Body: []byte("<p>1</p>")},
	}}
	svc := &ConfluenceService{store: st}
	_, err := svc.Pull(context.Background(), PullOpts{ID: "1", Into: into})
	if err == nil || !strings.Contains(err.Error(), "corrupt mirror sidecar") {
		t.Fatalf("corrupt sidecar must fail the pull loudly, got %v", err)
	}
}

// Two pages whose titles slugify identically must never overwrite each other:
// the second pull is diverted to an id-suffixed dir and both bodies survive.
func TestPullCollidingTitlesDoNotOverwrite(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Foo Bar", SpaceKey: "SP", Version: 1, Body: []byte("<p>A</p>")},
		"200": {ID: "200", Title: "Foo-Bar?", SpaceKey: "SP", Version: 1, Body: []byte("<p>B</p>")},
	}}
	svc := &ConfluenceService{store: st}
	res1, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatalf("pull 100: %v", err)
	}
	res2, err := svc.Pull(context.Background(), PullOpts{ID: "200", Into: into})
	if err != nil {
		t.Fatalf("pull 200: %v", err)
	}
	pathA, pathB := res1.Pages[0].Path, res2.Pages[0].Path
	if pathA == pathB {
		t.Fatalf("colliding titles mirrored to the same path %q", pathA)
	}
	for path, want := range map[string]string{pathA: "<p>A</p>", pathB: "<p>B</p>"} {
		got, rerr := os.ReadFile(filepath.Join(into, path))
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	// Re-pulling the diverted page stays in its dir (no path churn).
	res3, err := svc.Pull(context.Background(), PullOpts{ID: "200", Into: into})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Pages[0].Path != pathB {
		t.Errorf("re-pull moved page 200: %q -> %q", pathB, res3.Pages[0].Path)
	}
}

// A GetPage failure mid-pull aborts and returns the wrapped error (so the CLI can
// map the sentinel to an exit code); pages mirrored so far are still reported.
func TestPullGetPageErrorAborts(t *testing.T) {
	st := &pullStore{getErr: domain.ErrForbidden}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "1", Into: t.TempDir()})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("GetPage error should propagate as ErrForbidden, got %v", err)
	}
	if res == nil {
		t.Fatal("Pull should return a partial result alongside the error")
	}
}

// ---- failReason classification ----

func TestFailReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{domain.ErrForbidden, "forbidden"},
		{domain.ErrNotFound, "not-found"},
		{domain.ErrAuth, "auth"},
		{domain.ErrUsage, "usage"},
		{errors.New("boom"), "error"},
		// wrapped sentinels must still classify via errors.Is
		{fmtErrorf(domain.ErrForbidden), "forbidden"},
		{fmtErrorf(domain.ErrVersionConflict), "error"}, // not in failReason's switch -> default
	}
	for _, c := range cases {
		if got := failReason(c.err); got != c.want {
			t.Errorf("failReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func fmtErrorf(sentinel error) error {
	return errors.Join(sentinel, errors.New("wrapped detail"))
}

// ---- Feature 5: CopyPage ----

func TestCopyPagePassesBodyUnchanged(t *testing.T) {
	ctx := context.Background()
	const body = "<p>native CSF bytes</p>"
	src := &domain.Resource{
		ID: "100", SpaceKey: "SRC", Version: 3,
		Body: []byte(body), Parent: "par0",
	}
	st := &recordingStore{page: src}
	svc := &ConfluenceService{store: st}
	_, err := svc.CopyPage(ctx, "100", "New Title", "SP2", "par1")
	if err != nil {
		t.Fatal(err)
	}
	if st.getID != "100" {
		t.Errorf("GetPage not called with src id: %q", st.getID)
	}
	if string(st.createBody) != body {
		t.Errorf("body not passed through: got %q want %q", st.createBody, body)
	}
	if st.createTitle != "New Title" {
		t.Errorf("title not forwarded: %q", st.createTitle)
	}
	if st.createSpace != "SP2" {
		t.Errorf("space not forwarded: %q", st.createSpace)
	}
	if st.createParent != "par1" {
		t.Errorf("parent not forwarded: %q", st.createParent)
	}
}

func TestCopyPageDefaultsSpaceAndParent(t *testing.T) {
	ctx := context.Background()
	src := &domain.Resource{
		ID: "100", SpaceKey: "ORIG", Version: 1,
		Body: []byte("<p/>"), Parent: "origpar",
	}
	st := &recordingStore{page: src}
	svc := &ConfluenceService{store: st}
	// Pass empty space and parent → should use source page's SpaceKey and Parent
	_, err := svc.CopyPage(ctx, "100", "Copy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.createSpace != "ORIG" {
		t.Errorf("space should default to source space, got %q", st.createSpace)
	}
	if st.createParent != "origpar" {
		t.Errorf("parent should default to source parent, got %q", st.createParent)
	}
}

func TestCopyPageRejectsProjectionWithoutNativeBody(t *testing.T) {
	st := &recordingStore{
		page:     &domain.Resource{ID: "100", SpaceKey: "SRC", Version: 1},
		omitBody: true,
	}
	svc := &ConfluenceService{store: st}
	_, err := svc.CopyPage(context.Background(), "100", "Copy", "", "")
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("copy error = %v, want check failure", err)
	}
	if st.createTitle != "" || st.createBody != nil {
		t.Fatalf("CreatePage called after partial projection: title=%q body=%q", st.createTitle, st.createBody)
	}
}

// ---- Feature 6: Attachment upload/delete at service layer ----

type attachmentStore struct {
	// recordingStore embeds domain.DocStore; attach upload/delete on top.
	*recordingStore
	uploadPageID  string
	uploadName    string
	uploadData    []byte
	uploadComment string
	uploadSize    int64
	uploadReturn  *domain.Attachment
	uploadErr     error
	deleteID      string
	deleteErr     error
}

func (s *attachmentStore) UploadAttachment(_ context.Context, pageID, filename string, data io.ReadCloser, size int64, comment string) (*domain.Attachment, error) {
	defer data.Close()
	payload, err := io.ReadAll(data)
	if err != nil {
		return nil, err
	}
	s.uploadPageID, s.uploadName, s.uploadData, s.uploadSize, s.uploadComment = pageID, filename, payload, size, comment
	return s.uploadReturn, s.uploadErr
}

func (s *attachmentStore) DeleteAttachment(_ context.Context, attachmentID string) error {
	s.deleteID = attachmentID
	return s.deleteErr
}

func TestUploadAttachmentServiceReadFile(t *testing.T) {
	// Write a temp file to upload.
	dir := t.TempDir()
	p := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	att := &domain.Attachment{ID: "a1", Title: "test.txt"}
	st := &attachmentStore{recordingStore: &recordingStore{}, uploadReturn: att}
	svc := &ConfluenceService{store: st}

	got, err := svc.UploadAttachment(context.Background(), "pg1", p, "my comment")
	if err != nil {
		t.Fatal(err)
	}
	if st.uploadPageID != "pg1" {
		t.Errorf("pageID = %q, want pg1", st.uploadPageID)
	}
	if st.uploadName != "test.txt" {
		t.Errorf("filename = %q, want test.txt", st.uploadName)
	}
	if string(st.uploadData) != "hello" {
		t.Errorf("data = %q, want hello", st.uploadData)
	}
	if st.uploadSize != int64(len("hello")) {
		t.Errorf("size = %d, want %d", st.uploadSize, len("hello"))
	}
	if st.uploadComment != "my comment" {
		t.Errorf("comment = %q, want my comment", st.uploadComment)
	}
	if got.ID != "a1" {
		t.Errorf("returned att.ID = %q, want a1", got.ID)
	}
}

func TestDeleteAttachmentServicePassThrough(t *testing.T) {
	st := &attachmentStore{recordingStore: &recordingStore{}}
	svc := &ConfluenceService{store: st}
	if err := svc.DeleteAttachment(context.Background(), "att99"); err != nil {
		t.Fatal(err)
	}
	if st.deleteID != "att99" {
		t.Errorf("deleteID = %q, want att99", st.deleteID)
	}
}

// ---- Feature 7: Whoami ----

type fakeVerifier struct {
	name string
	err  error
}

func (f *fakeVerifier) Whoami(_ context.Context) (string, error) { return f.name, f.err }

func TestConfluenceWhoami(t *testing.T) {
	ctx := context.Background()

	t.Run("returns display name", func(t *testing.T) {
		svc := &ConfluenceService{
			store:    &recordingStore{},
			verifier: &fakeVerifier{name: "Ada Lovelace"},
		}
		name, err := svc.Whoami(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if name != "Ada Lovelace" {
			t.Errorf("Whoami = %q, want Ada Lovelace", name)
		}
	})

	t.Run("nil verifier returns error", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{}}
		_, err := svc.Whoami(ctx)
		if err == nil {
			t.Error("nil verifier should return error")
		}
	})
}

// A truncated space tree must propagate through resolveIDs so a --space pull
// reports the cap instead of silently mirroring a partial space.
func TestResolveIDsPropagatesSpaceTruncation(t *testing.T) {
	st := &recordingStore{pageRefs: []domain.PageRef{{ID: "1"}, {ID: "2"}}, treeTruncated: true}
	svc := &ConfluenceService{store: st}
	ids, truncated, err := svc.resolveIDs(context.Background(), PullOpts{Space: "DOC"})
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("space truncation flag was dropped")
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
}
