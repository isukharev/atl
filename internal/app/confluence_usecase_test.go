package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// recordingStore embeds DocStore so only the methods a test needs are
// implemented; it records the arguments of each thin-wrapper call-through and
// returns canned values/errors so we can pin the service→port contract.
type recordingStore struct {
	domain.DocStore

	// recorded args
	searchQuery  string
	searchLimit  int
	searchCursor string
	getID        string
	getFormat    string
	metaID       string
	historyID    string
	treeSpace    string
	treeDepth    int
	commentsID   string
	addCommentID string
	addBody      []byte
	createSpace  string
	createParent string
	createTitle  string
	createBody   []byte
	moveID       string
	moveParent   string
	deleteID     string

	// canned returns
	pageRefs []domain.PageRef
	cursor   string
	page     *domain.Resource
	meta     *domain.PageMeta
	versions []domain.Version
	comments []domain.Comment
	comment  *domain.Comment
	err      error
}

func (s *recordingStore) Search(_ context.Context, q string, limit int, cursor string) ([]domain.PageRef, string, error) {
	s.searchQuery, s.searchLimit, s.searchCursor = q, limit, cursor
	return s.pageRefs, s.cursor, s.err
}

func (s *recordingStore) GetPage(_ context.Context, id string, o domain.PullOpts) (*domain.Resource, error) {
	s.getID, s.getFormat = id, o.Format
	return s.page, s.err
}

func (s *recordingStore) GetMeta(_ context.Context, id string) (*domain.PageMeta, error) {
	s.metaID = id
	return s.meta, s.err
}

func (s *recordingStore) History(_ context.Context, id string) ([]domain.Version, error) {
	s.historyID = id
	return s.versions, s.err
}

func (s *recordingStore) Tree(_ context.Context, space string, depth int) ([]domain.PageRef, error) {
	s.treeSpace, s.treeDepth = space, depth
	return s.pageRefs, s.err
}

func (s *recordingStore) ListComments(_ context.Context, id string) ([]domain.Comment, error) {
	s.commentsID = id
	return s.comments, s.err
}

func (s *recordingStore) AddComment(_ context.Context, id string, body []byte) (*domain.Comment, error) {
	s.addCommentID, s.addBody = id, body
	return s.comment, s.err
}

func (s *recordingStore) CreatePage(_ context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	s.createSpace, s.createParent, s.createTitle, s.createBody = space, parent, title, body
	return s.page, s.err
}

func (s *recordingStore) MovePage(_ context.Context, id, parent string) error {
	s.moveID, s.moveParent = id, parent
	return s.err
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
		got, err := svc.Tree(ctx, "SPACE", 4)
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
		got, err := svc.Comments(ctx, "p1")
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

	t.Run("Move", func(t *testing.T) {
		st := &recordingStore{}
		svc := &ConfluenceService{store: st}
		if err := svc.Move(ctx, "id1", "par1"); err != nil {
			t.Fatal(err)
		}
		if st.moveID != "id1" || st.moveParent != "par1" {
			t.Errorf("move args not forwarded: %q %q", st.moveID, st.moveParent)
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
	if _, err := svc.Tree(ctx, "x", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Tree did not propagate sentinel: %v", err)
	}
	if _, _, err := svc.Search(ctx, "x", 1, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Search did not propagate sentinel: %v", err)
	}
	if _, err := svc.Comments(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Comments did not propagate sentinel: %v", err)
	}
	if _, err := svc.AddComment(ctx, "x", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("AddComment did not propagate sentinel: %v", err)
	}
	if _, err := svc.Create(ctx, "x", "", "t", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Create did not propagate sentinel: %v", err)
	}
	if err := svc.Move(ctx, "x", "y"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Move did not propagate sentinel: %v", err)
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

// ---- resolveIDs branches ----

func TestResolveIDsBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("byID", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{}}
		ids, err := svc.resolveIDs(ctx, PullOpts{ID: "555"})
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
		ids, err := svc.resolveIDs(ctx, PullOpts{Space: "ENG", Depth: 2})
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
		ids, err := svc.resolveIDs(ctx, PullOpts{CQL: "label = foo"})
		if err != nil {
			t.Fatal(err)
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
		_, err := svc.resolveIDs(ctx, PullOpts{})
		if !errors.Is(err, domain.ErrUsage) {
			t.Errorf("missing selector should be ErrUsage, got %v", err)
		}
	})

	t.Run("treeErrPropagates", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{err: domain.ErrForbidden}}
		_, err := svc.resolveIDs(ctx, PullOpts{Space: "X"})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("tree error should propagate, got %v", err)
		}
	})

	t.Run("searchErrPropagates", func(t *testing.T) {
		svc := &ConfluenceService{store: &recordingStore{err: domain.ErrAuth}}
		_, err := svc.resolveIDs(ctx, PullOpts{CQL: "x"})
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

// collectSearch silently truncates at exactly 1000 ids: it returns no error even
// though the backend signals more results. This is the documented CQL pull cap.
func TestCollectSearchSilent1000Cap(t *testing.T) {
	st := &infiniteSearchStore{}
	svc := &ConfluenceService{store: st}
	ids, err := svc.collectSearch(context.Background(), "type = page")
	if err != nil {
		t.Fatalf("collectSearch returned an error; the cap must be silent: %v", err)
	}
	if len(ids) != 1000 {
		t.Fatalf("CQL cap = %d ids, want exactly 1000 (silent truncation)", len(ids))
	}
	// 100-per-page * 10 pages == 1000; the loop condition `len(ids) < 1000` admits
	// one final page that overshoots to exactly 1000, then stops.
	if st.calls != 10 {
		t.Errorf("expected 10 search calls to reach 1000, got %d", st.calls)
	}
}

// Pull surfaces the same silent cap through the public entrypoint.
func TestPullCQLSilent1000Cap(t *testing.T) {
	st := &pullCapStore{}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{CQL: "type = page", Into: t.TempDir()})
	if err != nil {
		t.Fatalf("Pull with capped CQL must not error (silent truncation): %v", err)
	}
	if len(res.Pages) != 1000 {
		t.Fatalf("Pull mirrored %d pages, want exactly 1000 (silent CQL cap)", len(res.Pages))
	}
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
	return &domain.Resource{ID: id, Title: id, SpaceKey: "SP", Version: 1, Body: []byte("<p>body</p>")}, nil
}

// ---- Pull orchestration ----

// pullStore serves a fixed set of pages by id for Pull orchestration tests.
type pullStore struct {
	domain.DocStore
	pages   map[string]*domain.Resource
	getErr  error
	getErrs map[string]error
}

func (s *pullStore) GetPage(_ context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	if s.getErrs != nil {
		if err, ok := s.getErrs[id]; ok {
			return nil, err
		}
	}
	if s.getErr != nil {
		return nil, s.getErr
	}
	if p, ok := s.pages[id]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
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
