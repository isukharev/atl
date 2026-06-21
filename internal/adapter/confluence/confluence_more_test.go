package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// --- pure helpers ---------------------------------------------------------

// TestCQLQuote pins the escaping of CQL string literals: backslashes and
// double-quotes must be escaped (backslash first, then quote) and the whole
// value wrapped in double quotes, so a crafted space key cannot break out of
// the literal and alter the query.
func TestCQLQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{`DOC`, `"DOC"`},
		{``, `""`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		// A naive injection: a closing quote followed by an OR clause. After
		// escaping the quote is neutralized, so it stays inside the literal.
		{`x" or type=page`, `"x\" or type=page"`},
		// Backslash-then-quote must escape the backslash first (\\) then the
		// quote (\"), never producing \" from the raw backslash.
		{`\"`, `"\\\""`},
		{`сд`, `"сд"`}, // unicode passes through unchanged
	}
	for _, c := range cases {
		if got := cqlQuote(c.in); got != c.want {
			t.Errorf("cqlQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStripHTML covers tag removal, nested tags, entity passthrough, empty
// input, and surrounding-whitespace trimming. Note: stripHTML does NOT decode
// HTML entities — it only strips < … > spans — so &amp; survives verbatim.
func TestStripHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{``, ``},
		{`plain`, `plain`},
		{`<b>bold</b>`, `bold`},
		{`a <b>b</b> c`, `a b c`}, // tags removed; the literal spaces around them survive
		{`<a><b>nested</b></a>`, `nested`},
		{`  <b>trim</b>  `, `trim`},
		{`a &amp; b`, `a &amp; b`}, // entities are NOT decoded
		{`<unclosed`, ``},          // an unterminated tag swallows the rest
		{`<b>x</b>>y`, `xy`},       // stray '>' after a closed tag is dropped (inTag already false → treated as tag close)
	}
	for _, c := range cases {
		if got := stripHTML(c.in); got != c.want {
			t.Errorf("stripHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFirstNonEmpty pins the helper Search uses to prefer the structured title
// over the (HTML-stripped) search title: a whitespace-only first value falls
// through to the second.
func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("a", "b"); got != "a" {
		t.Errorf("firstNonEmpty(a,b)=%q want a", got)
	}
	if got := firstNonEmpty("", "b"); got != "b" {
		t.Errorf("firstNonEmpty('',b)=%q want b", got)
	}
	if got := firstNonEmpty("   ", "b"); got != "b" {
		t.Errorf("firstNonEmpty(spaces,b)=%q want b", got)
	}
}

// TestToResource verifies a content DTO maps to a domain.Resource: id/title/
// space/version/body, ancestors (titles in order), Parent = last ancestor id,
// labels, and the absolute URL built from base + webui.
func TestToResource(t *testing.T) {
	var ct content
	const raw = `{
		"id": "42",
		"title": "Hello",
		"space": {"key": "DOC"},
		"version": {"number": 7},
		"ancestors": [{"id": "1", "title": "Root"}, {"id": "2", "title": "Mid"}],
		"metadata": {"labels": {"results": [{"name": "alpha"}, {"name": "beta"}]}},
		"_links": {"webui": "/display/DOC/Hello"}
	}`
	if err := json.Unmarshal([]byte(raw), &ct); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	r := ct.toResource("https://wiki.example", "the body")
	if r.ID != "42" || r.Title != "Hello" || r.SpaceKey != "DOC" || r.Version != 7 {
		t.Errorf("scalar fields wrong: %+v", r)
	}
	if string(r.Body) != "the body" {
		t.Errorf("body = %q, want 'the body'", r.Body)
	}
	if len(r.Ancestors) != 2 || r.Ancestors[0] != "Root" || r.Ancestors[1] != "Mid" {
		t.Errorf("ancestors = %v", r.Ancestors)
	}
	if r.Parent != "2" {
		t.Errorf("parent = %q, want 2 (last ancestor id)", r.Parent)
	}
	if len(r.Labels) != 2 || r.Labels[0] != "alpha" || r.Labels[1] != "beta" {
		t.Errorf("labels = %v", r.Labels)
	}
	if r.URL != "https://wiki.example/display/DOC/Hello" {
		t.Errorf("url = %q", r.URL)
	}
}

// TestToResourceNoAncestorsNoWebUI checks the empty branches: no ancestors →
// empty Parent and Ancestors; missing webui → empty URL (not just base).
func TestToResourceNoAncestorsNoWebUI(t *testing.T) {
	var ct content
	const raw = `{"id":"9","title":"T","space":{"key":"S"},"version":{"number":1}}`
	if err := json.Unmarshal([]byte(raw), &ct); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	r := ct.toResource("https://wiki.example", "")
	if r.Parent != "" {
		t.Errorf("expected empty Parent, got %q", r.Parent)
	}
	if len(r.Ancestors) != 0 {
		t.Errorf("expected no ancestors, got %v", r.Ancestors)
	}
	if r.URL != "" {
		t.Errorf("expected empty URL when webui absent, got %q", r.URL)
	}
}

// --- GetPage --------------------------------------------------------------

// TestGetPageStorage verifies the default (csf) path requests body.storage and
// returns the storage value as the resource body, with the expand query set.
func TestGetPageStorage(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"55","title":"Doc","space":{"key":"DOC"},"version":{"number":4},"body":{"storage":{"value":"<p>native</p>"},"view":{"value":"<p>rendered</p>"}}}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	r, err := cf.GetPage(context.Background(), "55", domain.PullOpts{})
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if string(r.Body) != "<p>native</p>" {
		t.Errorf("body = %q, want storage value", r.Body)
	}
	if !strings.Contains(gotPath, "/rest/api/content/55") || !strings.Contains(gotPath, "expand=body.storage") {
		t.Errorf("unexpected request path %q", gotPath)
	}
	if strings.Contains(gotPath, "body.view") {
		t.Errorf("csf pull should not request body.view: %q", gotPath)
	}
}

// TestGetPageView verifies opts.Format=="view" switches the expand to
// body.view and returns the view value.
func TestGetPageView(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"55","title":"Doc","body":{"storage":{"value":"<p>native</p>"},"view":{"value":"<p>rendered</p>"}}}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	r, err := cf.GetPage(context.Background(), "55", domain.PullOpts{Format: "view"})
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if string(r.Body) != "<p>rendered</p>" {
		t.Errorf("body = %q, want view value", r.Body)
	}
	if !strings.Contains(gotPath, "expand=body.view") {
		t.Errorf("view pull should request body.view: %q", gotPath)
	}
}

// TestGetPageEmptyBody verifies a page with no body.storage yields an empty
// (non-nil-checked) body, not an error.
func TestGetPageEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"55","title":"Empty","space":{"key":"DOC"},"version":{"number":1}}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	r, err := cf.GetPage(context.Background(), "55", domain.PullOpts{})
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if len(r.Body) != 0 {
		t.Errorf("expected empty body, got %q", r.Body)
	}
	if r.Title != "Empty" {
		t.Errorf("title = %q", r.Title)
	}
}

// TestGetPageNotFound verifies a 404 maps to domain.ErrNotFound (exit 4).
func TestGetPageNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such content"}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.GetPage(context.Background(), "nope", domain.PullOpts{})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- History --------------------------------------------------------------

// TestHistory verifies version records are parsed newest-first as returned and
// the by/when/message fields are mapped onto domain.Version.
func TestHistory(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"number":3,"when":"2026-01-03","message":"third","by":{"displayName":"Carol"}},
			{"number":2,"when":"2026-01-02","by":{"displayName":"Bob"}},
			{"number":1,"when":"2026-01-01","by":{"displayName":"Alice"}}
		]}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	vs, err := cf.History(context.Background(), "70")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(vs))
	}
	if vs[0].Number != 3 || vs[0].By != "Carol" || vs[0].Message != "third" || vs[0].When != "2026-01-03" {
		t.Errorf("v0 = %+v", vs[0])
	}
	if vs[2].Number != 1 || vs[2].By != "Alice" {
		t.Errorf("v2 = %+v", vs[2])
	}
	if !strings.Contains(gotPath, "/rest/api/content/70/version") || !strings.Contains(gotPath, "limit=50") {
		t.Errorf("unexpected history path %q", gotPath)
	}
}

// TestHistoryEmpty verifies an empty results array yields a non-nil empty slice
// (make with len 0), not an error.
func TestHistoryEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	vs, err := cf.History(context.Background(), "70")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if vs == nil {
		t.Fatalf("expected non-nil slice")
	}
	if len(vs) != 0 {
		t.Fatalf("expected 0 versions, got %d", len(vs))
	}
}

// TestHistoryForbidden verifies a 403 maps to domain.ErrForbidden.
func TestHistoryForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.History(context.Background(), "70")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// --- Tree -----------------------------------------------------------------

// TestTreePaginates verifies Tree follows _links.next, advances start by the
// number of results returned, sets Parent from the last ancestor, and uses the
// CQL-quoted space key.
func TestTreePaginates(t *testing.T) {
	var paths []string
	page1 := `{"results":[
		{"id":"1","title":"Root","space":{"key":"DOC"},"version":{"number":1}},
		{"id":"2","title":"Child","space":{"key":"DOC"},"version":{"number":1},"ancestors":[{"id":"1","title":"Root"}]}
	],"_links":{"next":"/rest/api/content/search?start=2"}}`
	page2 := `{"results":[
		{"id":"3","title":"Grandchild","space":{"key":"DOC"},"version":{"number":1},"ancestors":[{"id":"1","title":"Root"},{"id":"2","title":"Child"}]}
	],"_links":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(page1))
			return
		}
		_, _ = w.Write([]byte(page2))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, err := cf.Tree(context.Background(), "DOC", 0)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 pages, got %d: %+v", len(got), got)
	}
	if got[0].Parent != "" {
		t.Errorf("root parent should be empty, got %q", got[0].Parent)
	}
	if got[1].Parent != "1" {
		t.Errorf("child parent = %q, want 1", got[1].Parent)
	}
	if got[2].Parent != "2" {
		t.Errorf("grandchild parent = %q, want 2", got[2].Parent)
	}
	if len(paths) < 1 || !strings.Contains(paths[0], "cql=") {
		t.Fatalf("expected cql query in path: %v", paths)
	}
	// The CQL must contain the quoted space key (space="DOC").
	if !strings.Contains(paths[0], "space%3D%22DOC%22") {
		t.Errorf("expected quoted space key in cql, got %q", paths[0])
	}
}

// TestTreeDepthFilter verifies depth>0 filters out pages whose ancestor count
// is >= depth. With depth=1, only top-level pages (0 ancestors) survive.
func TestTreeDepthFilter(t *testing.T) {
	body := `{"results":[
		{"id":"1","title":"Root","space":{"key":"DOC"},"version":{"number":1}},
		{"id":"2","title":"Child","space":{"key":"DOC"},"version":{"number":1},"ancestors":[{"id":"1","title":"Root"}]}
	],"_links":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, err := cf.Tree(context.Background(), "DOC", 1)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("depth=1 should keep only top-level page, got %+v", got)
	}
}

// TestTreeEmptyResultsStops verifies an empty results page with a populated
// _links.next does not loop (the len(results)==0 break).
func TestTreeEmptyResultsStops(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"_links":{"next":"/rest/api/content/search?start=0"}}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, err := cf.Tree(context.Background(), "DOC", 0)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 pages, got %d", len(got))
	}
	if calls != 1 {
		t.Fatalf("expected exactly one request (no infinite loop), got %d", calls)
	}
}

// TestTreeError verifies a backend error propagates with the sentinel.
func TestTreeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.Tree(context.Background(), "DOC", 0)
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

// --- UpdatePage -----------------------------------------------------------

// captureUpdate decodes the PUT payload sent to UpdatePage's PUT handler.
type updatePayload struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
	Body struct {
		Storage struct {
			Value          string `json:"value"`
			Representation string `json:"representation"`
		} `json:"storage"`
	} `json:"body"`
}

func decodePut(t *testing.T, r *http.Request) updatePayload {
	t.Helper()
	var p updatePayload
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode PUT payload: %v (raw=%s)", err, b)
	}
	return p
}

// TestUpdatePageNormal verifies the non-force path with a supplied title:
// version.number = expectVersion+1, no GET is issued, representation is
// "storage", and the returned version comes from the PUT response.
func TestUpdatePageNormal(t *testing.T) {
	var gets, puts int
	var got updatePayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			gets++
			_, _ = w.Write([]byte(`{"version":{"number":99}}`))
		case http.MethodPut:
			puts++
			got = decodePut(t, r)
			_, _ = w.Write([]byte(`{"version":{"number":6}}`))
		}
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	ver, err := cf.UpdatePage(context.Background(), "55", 5, "My Title", []byte("<p>x</p>"), false)
	if err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}
	if ver != 6 {
		t.Errorf("returned version = %d, want 6 (from PUT response)", ver)
	}
	if gets != 0 {
		t.Errorf("expected no GET when title supplied and not force, got %d", gets)
	}
	if puts != 1 {
		t.Errorf("expected exactly one PUT, got %d", puts)
	}
	if got.Version.Number != 6 {
		t.Errorf("PUT version = %d, want expectVersion+1 = 6", got.Version.Number)
	}
	if got.Title != "My Title" || got.Type != "page" {
		t.Errorf("PUT title/type wrong: %+v", got)
	}
	if got.Body.Storage.Value != "<p>x</p>" || got.Body.Storage.Representation != "storage" {
		t.Errorf("PUT body wrong: %+v", got.Body.Storage)
	}
}

// TestUpdatePageForce verifies --force re-reads the current version and targets
// current+1, ignoring expectVersion, and fills the title from the GET when the
// caller supplied none.
func TestUpdatePageForce(t *testing.T) {
	var got updatePayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"version":{"number":12},"title":"Remote Title"}`))
		case http.MethodPut:
			got = decodePut(t, r)
			_, _ = w.Write([]byte(`{"version":{"number":13}}`))
		}
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	// expectVersion is deliberately stale (3); force must ignore it.
	ver, err := cf.UpdatePage(context.Background(), "55", 3, "", []byte("<p>y</p>"), true)
	if err != nil {
		t.Fatalf("UpdatePage force: %v", err)
	}
	if ver != 13 {
		t.Errorf("version = %d, want 13", ver)
	}
	if got.Version.Number != 13 {
		t.Errorf("PUT version = %d, want current+1 = 13 (ignoring expectVersion)", got.Version.Number)
	}
	if got.Title != "Remote Title" {
		t.Errorf("title = %q, want fetched 'Remote Title'", got.Title)
	}
}

// TestUpdatePageEmptyTitleFetches verifies the title-fetch branch when title=="" and
// not force: it GETs the page, fills the title, and (since cur.Version matches
// expectVersion) proceeds to PUT at expectVersion+1.
func TestUpdatePageEmptyTitleFetches(t *testing.T) {
	var got updatePayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"version":{"number":5},"title":"Fetched"}`))
		case http.MethodPut:
			got = decodePut(t, r)
			_, _ = w.Write([]byte(`{"version":{"number":6}}`))
		}
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	ver, err := cf.UpdatePage(context.Background(), "55", 5, "", []byte("<p>z</p>"), false)
	if err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}
	if ver != 6 {
		t.Errorf("version = %d, want 6", ver)
	}
	if got.Title != "Fetched" {
		t.Errorf("title = %q, want 'Fetched'", got.Title)
	}
	if got.Version.Number != 6 {
		t.Errorf("PUT version = %d, want expectVersion+1 = 6", got.Version.Number)
	}
}

// TestUpdatePageEmptyTitleDriftConflict verifies the local drift refusal: when
// title=="" and not force and the fetched current version != expectVersion, the
// method returns ErrVersionConflict WITHOUT issuing a PUT.
func TestUpdatePageEmptyTitleDriftConflict(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"version":{"number":9},"title":"Drifted"}`))
		case http.MethodPut:
			puts++
			_, _ = w.Write([]byte(`{"version":{"number":10}}`))
		}
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.UpdatePage(context.Background(), "55", 5, "", []byte("<p>z</p>"), false)
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict from local drift check, got %v", err)
	}
	if puts != 0 {
		t.Errorf("expected no PUT when drift detected locally, got %d", puts)
	}
}

// TestUpdatePageConflictFromServer verifies a 409 on the PUT (the optimistic
// gate firing server-side) maps to ErrVersionConflict.
func TestUpdatePageConflictFromServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"version conflict"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":{"number":5},"title":"T"}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.UpdatePage(context.Background(), "55", 5, "Has Title", []byte("<p>x</p>"), false)
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict from server 409, got %v", err)
	}
}

// TestUpdatePageForceGetError verifies a failure fetching the current version
// in the force path propagates (the sentinel is preserved).
func TestUpdatePageForceGetError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.UpdatePage(context.Background(), "55", 5, "", nil, true)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound from force GET, got %v", err)
	}
}

// --- CreatePage -----------------------------------------------------------

// TestCreatePageWithParent verifies the POST payload: type/title/space.key,
// storage body, and ancestors set when parent is non-empty; the returned
// Resource is built from the response.
func TestCreatePageWithParent(t *testing.T) {
	var method, path string
	var payload struct {
		Type  string `json:"type"`
		Title string `json:"title"`
		Space struct {
			Key string `json:"key"`
		} `json:"space"`
		Body struct {
			Storage struct {
				Value          string `json:"value"`
				Representation string `json:"representation"`
			} `json:"storage"`
		} `json:"body"`
		Ancestors []struct {
			ID string `json:"id"`
		} `json:"ancestors"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"900","title":"New","space":{"key":"DOC"},"version":{"number":1},"body":{"storage":{"value":"<p>created</p>"}},"_links":{"webui":"/x"}}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	r, err := cf.CreatePage(context.Background(), "DOC", "777", "New", []byte("<p>b</p>"))
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if method != http.MethodPost || path != "/rest/api/content" {
		t.Errorf("method/path = %s %s, want POST /rest/api/content", method, path)
	}
	if payload.Type != "page" || payload.Title != "New" || payload.Space.Key != "DOC" {
		t.Errorf("payload header wrong: %+v", payload)
	}
	if payload.Body.Storage.Value != "<p>b</p>" || payload.Body.Storage.Representation != "storage" {
		t.Errorf("payload body wrong: %+v", payload.Body.Storage)
	}
	if len(payload.Ancestors) != 1 || payload.Ancestors[0].ID != "777" {
		t.Errorf("ancestors = %+v, want [{id:777}]", payload.Ancestors)
	}
	if r.ID != "900" || string(r.Body) != "<p>created</p>" {
		t.Errorf("resource = %+v", r)
	}
	if r.URL != srv.URL+"/x" {
		t.Errorf("url = %q", r.URL)
	}
}

// TestCreatePageNoParent verifies ancestors is omitted when parent is empty.
func TestCreatePageNoParent(t *testing.T) {
	var hasAncestors bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		_, hasAncestors = m["ancestors"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"901","title":"Top"}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.CreatePage(context.Background(), "DOC", "", "Top", []byte("x")); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if hasAncestors {
		t.Errorf("ancestors should be omitted when parent is empty")
	}
}

// TestCreatePageForbidden verifies a 403 maps to ErrForbidden.
func TestCreatePageForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.CreatePage(context.Background(), "DOC", "", "T", []byte("x"))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// --- MovePage -------------------------------------------------------------

// TestMovePage verifies the read-modify-write: it GETs version+body.storage,
// then PUTs with version.number = current+1, the new ancestor, and preserves
// the existing title and body bytes verbatim.
func TestMovePage(t *testing.T) {
	var got struct {
		Title   string `json:"title"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Ancestors []struct {
			ID string `json:"id"`
		} `json:"ancestors"`
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	var putMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"55","title":"Movable","version":{"number":8},"body":{"storage":{"value":"<p>keep me</p>"}}}`))
		case http.MethodPut:
			putMethod = r.Method
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if err := cf.MovePage(context.Background(), "55", "newdad"); err != nil {
		t.Fatalf("MovePage: %v", err)
	}
	if putMethod != http.MethodPut {
		t.Fatalf("expected a PUT")
	}
	if got.Version.Number != 9 {
		t.Errorf("version = %d, want current+1 = 9", got.Version.Number)
	}
	if got.Title != "Movable" {
		t.Errorf("title = %q, want preserved 'Movable'", got.Title)
	}
	if len(got.Ancestors) != 1 || got.Ancestors[0].ID != "newdad" {
		t.Errorf("ancestors = %+v, want [{id:newdad}]", got.Ancestors)
	}
	if got.Body.Storage.Value != "<p>keep me</p>" {
		t.Errorf("body = %q, want verbatim preserved", got.Body.Storage.Value)
	}
}

// TestMovePageReadNotFound verifies a 404 returned mid-move (on the read) maps
// to ErrNotFound.
func TestMovePageReadNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	err := cf.MovePage(context.Background(), "55", "newdad")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- DeletePage -----------------------------------------------------------

// TestDeletePage verifies the DELETE method and path.
func TestDeletePage(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if err := cf.DeletePage(context.Background(), "55"); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	if method != http.MethodDelete || path != "/rest/api/content/55" {
		t.Errorf("method/path = %s %s, want DELETE /rest/api/content/55", method, path)
	}
}

// TestDeletePageForbidden verifies a 403 (per-space permission) maps to
// ErrForbidden.
func TestDeletePageForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	err := cf.DeletePage(context.Background(), "55")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// --- AddComment -----------------------------------------------------------

// TestAddComment verifies the POST payload (type=comment, container id/type,
// storage body) and that the returned Comment carries the response id and the
// original body bytes.
func TestAddComment(t *testing.T) {
	var method, path string
	var payload struct {
		Type      string `json:"type"`
		Container struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"container"`
		Body struct {
			Storage struct {
				Value          string `json:"value"`
				Representation string `json:"representation"`
			} `json:"storage"`
		} `json:"body"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"comment-77"}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	c, err := cf.AddComment(context.Background(), "55", []byte("<p>hi</p>"))
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if method != http.MethodPost || path != "/rest/api/content" {
		t.Errorf("method/path = %s %s", method, path)
	}
	if payload.Type != "comment" || payload.Container.ID != "55" || payload.Container.Type != "page" {
		t.Errorf("payload container wrong: %+v", payload)
	}
	if payload.Body.Storage.Value != "<p>hi</p>" || payload.Body.Storage.Representation != "storage" {
		t.Errorf("payload body wrong: %+v", payload.Body.Storage)
	}
	if c.ID != "comment-77" || c.Body != "<p>hi</p>" {
		t.Errorf("returned comment = %+v", c)
	}
}

// TestAddCommentForbidden verifies a 403 maps to ErrForbidden.
func TestAddCommentForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.AddComment(context.Background(), "55", []byte("x"))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// --- DownloadAttachment ---------------------------------------------------

// TestDownloadAttachment verifies the download path (no version) and that the
// raw bytes are returned verbatim.
func TestDownloadAttachment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("\x89PNGbytes"))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	data, err := cf.DownloadAttachment(context.Background(), "55", "diagram.png", 0)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if string(data) != "\x89PNGbytes" {
		t.Errorf("data = %q", data)
	}
	if gotPath != "/download/attachments/55/diagram.png" {
		t.Errorf("path = %q", gotPath)
	}
}

// TestDownloadAttachmentVersion verifies version>0 appends ?version=N.
func TestDownloadAttachmentVersion(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		_, _ = w.Write([]byte("data"))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.DownloadAttachment(context.Background(), "55", "d.png", 3); err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if !strings.Contains(gotURI, "version=3") {
		t.Errorf("expected version=3 in %q", gotURI)
	}
}

// TestDownloadAttachmentTraversalEscaped is the security-relevant assertion: a
// hostile server-supplied filename containing path-traversal sequences and a
// leading slash must be URL-PATH-ESCAPED by the adapter so it lands as a single
// (encoded) path component beneath /download/attachments/<pageID>/ and cannot
// climb out of that directory in the request URL. (On-disk traversal is blocked
// separately by safepath at the mirror layer; here we pin the adapter's URL
// construction.)
func TestDownloadAttachmentTraversalEscaped(t *testing.T) {
	var gotPath, gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path         // decoded
		gotRaw = r.URL.EscapedPath() // as sent on the wire
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	const hostile = "../../../../etc/passwd"
	if _, err := cf.DownloadAttachment(context.Background(), "55", hostile, 0); err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	// The escaped path on the wire must keep the page-id prefix and must NOT
	// contain a bare "/../" that would let the request escape the attachments
	// directory: the slashes in the filename are percent-encoded (%2F).
	if !strings.HasPrefix(gotRaw, "/download/attachments/55/") {
		t.Errorf("escaped path lost its prefix: %q", gotRaw)
	}
	if strings.Contains(gotRaw, "/../") {
		t.Errorf("escaped path contains an un-escaped traversal: %q", gotRaw)
	}
	if !strings.Contains(gotRaw, "%2F") && !strings.Contains(gotRaw, "%2f") {
		t.Errorf("expected slashes in filename to be percent-encoded, got raw=%q (decoded=%q)", gotRaw, gotPath)
	}
}

// TestDownloadAttachmentNotFound verifies a 404 maps to ErrNotFound.
func TestDownloadAttachmentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.DownloadAttachment(context.Background(), "55", "missing.png", 0)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- Resolve --------------------------------------------------------------

// TestResolveDrawio verifies a drawio ref downloads "<key>.png" at the given
// revision and returns that filename.
func TestResolveDrawio(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		_, _ = w.Write([]byte("PNG"))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	page := &domain.Resource{ID: "55"}
	ref := domain.Ref{Kind: domain.RefDrawio, Key: "Arch", Params: map[string]string{"revision": "4"}}
	data, name, err := cf.Resolve(context.Background(), page, ref)
	if err != nil {
		t.Fatalf("Resolve drawio: %v", err)
	}
	if name != "Arch.png" {
		t.Errorf("name = %q, want Arch.png", name)
	}
	if string(data) != "PNG" {
		t.Errorf("data = %q", data)
	}
	if !strings.Contains(gotURI, "/download/attachments/55/Arch.png") || !strings.Contains(gotURI, "version=4") {
		t.Errorf("download URI = %q", gotURI)
	}
}

// TestResolveDrawioBadRevision verifies a non-numeric revision degrades to 0
// (latest) rather than erroring — strconv.Atoi error is ignored by design.
func TestResolveDrawioBadRevision(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		_, _ = w.Write([]byte("PNG"))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	page := &domain.Resource{ID: "55"}
	ref := domain.Ref{Kind: domain.RefDrawio, Key: "X", Params: map[string]string{"revision": "notanumber"}}
	if _, _, err := cf.Resolve(context.Background(), page, ref); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if strings.Contains(gotURI, "version=") {
		t.Errorf("bad revision should drop the version param, got %q", gotURI)
	}
}

// TestResolveImage verifies an image ref downloads ref.Key (latest) and returns
// the key as the filename.
func TestResolveImage(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("IMG"))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	page := &domain.Resource{ID: "55"}
	ref := domain.Ref{Kind: domain.RefImage, Key: "photo.jpg"}
	data, name, err := cf.Resolve(context.Background(), page, ref)
	if err != nil {
		t.Fatalf("Resolve image: %v", err)
	}
	if name != "photo.jpg" || string(data) != "IMG" {
		t.Errorf("name/data = %q/%q", name, data)
	}
	if gotPath != "/download/attachments/55/photo.jpg" {
		t.Errorf("path = %q", gotPath)
	}
}

// TestResolveUnknownKind verifies a non-asset ref kind returns ErrNotFound and
// issues no request.
func TestResolveUnknownKind(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	page := &domain.Resource{ID: "55"}
	_, _, err := cf.Resolve(context.Background(), page, domain.Ref{Kind: domain.RefUser, Key: "u"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown kind, got %v", err)
	}
	if calls != 0 {
		t.Errorf("expected no HTTP call for unresolvable kind, got %d", calls)
	}
}

// TestResolveDownloadError verifies a download error in the drawio branch
// propagates (the resolver itself returns the error; the swallow-all guarantee
// lives in the fragment package, not here).
func TestResolveDownloadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	page := &domain.Resource{ID: "55"}
	_, _, err := cf.Resolve(context.Background(), page, domain.Ref{Kind: domain.RefImage, Key: "gone.png"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound propagated, got %v", err)
	}
}

// --- ResolveUser ----------------------------------------------------------

// TestResolveUserByKey verifies a userkey resolves on the first (key=) lookup.
func TestResolveUserByKey(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Alice Admin"}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	name, err := cf.ResolveUser(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if name != "Alice Admin" {
		t.Errorf("name = %q", name)
	}
	if len(queries) != 1 || !strings.HasPrefix(queries[0], "key=") {
		t.Errorf("expected a single key= lookup, got %v", queries)
	}
}

// TestResolveUserFallsBackToAccountID verifies that when the key= lookup
// returns an empty displayName, the resolver retries with accountId= and uses
// that result.
func TestResolveUserFallsBackToAccountID(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.RawQuery, "accountId=") {
			_, _ = w.Write([]byte(`{"displayName":"Cloud User"}`))
			return
		}
		_, _ = w.Write([]byte(`{"displayName":""}`)) // empty → triggers fallback
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	name, err := cf.ResolveUser(context.Background(), "5b10:abc")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if name != "Cloud User" {
		t.Errorf("name = %q, want 'Cloud User' from accountId fallback", name)
	}
	if len(queries) != 2 {
		t.Errorf("expected key then accountId lookups, got %v", queries)
	}
}

// TestResolveUserKeyErrorAccountIDSucceeds verifies that when the key= lookup
// errors (e.g. 404) but the accountId= lookup succeeds, the resolver returns
// the accountId display name and swallows the first error.
func TestResolveUserKeyErrorAccountIDSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.RawQuery, "accountId=") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"displayName":"Recovered"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound) // key= lookup fails
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	name, err := cf.ResolveUser(context.Background(), "u")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if name != "Recovered" {
		t.Errorf("name = %q, want 'Recovered'", name)
	}
}

// TestResolveUserBothFail verifies that when both lookups fail, the original
// (key=) error is returned.
func TestResolveUserBothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.ResolveUser(context.Background(), "ghost")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestResolveUserKeyOKButEmptyAccountIDAlsoEmpty verifies that when key= returns
// empty and accountId= also returns empty (no error on either), the resolver
// returns the empty key result with no error — the "not-found but no error"
// degradation path documented for the upstream user resolver.
func TestResolveUserBothEmptyNoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":""}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	name, err := cf.ResolveUser(context.Background(), "u")
	if err != nil {
		t.Fatalf("ResolveUser unexpected error: %v", err)
	}
	if name != "" {
		t.Errorf("expected empty display name, got %q", name)
	}
}

// --- Search parsing edge cases (beyond the cursor tests already present) ---

// TestSearchTitleAndExcerptStripped verifies Search prefers the structured
// content title, falls back to the stripped search title when content title is
// empty, strips HTML from the excerpt, and builds an absolute URL from
// _links.base + result.url.
func TestSearchTitleAndExcerptStripped(t *testing.T) {
	const body = `{
		"results": [
			{"content": {"id": "1", "title": "Structured", "space": {"key": "DOC"}, "version": {"number": 2}}, "title": "<b>Search</b> Title", "excerpt": "an <b>important</b> hit", "url": "/pages/1"},
			{"content": {"id": "2"}, "title": "<b>Fallback</b>", "excerpt": "", "url": ""}
		],
		"size": 2,
		"_links": {"base": "https://wiki.example", "next": ""}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	out, next, err := cf.Search(context.Background(), "type=page", 25, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if next != "" {
		t.Errorf("expected empty next (no _links.next), got %q", next)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	if out[0].Title != "Structured" {
		t.Errorf("out[0].Title = %q, want structured content title", out[0].Title)
	}
	if out[0].Excerpt != "an important hit" {
		t.Errorf("out[0].Excerpt = %q, want HTML stripped", out[0].Excerpt)
	}
	if out[0].URL != "https://wiki.example/pages/1" {
		t.Errorf("out[0].URL = %q", out[0].URL)
	}
	if out[1].Title != "Fallback" {
		t.Errorf("out[1].Title = %q, want stripped search title fallback", out[1].Title)
	}
	if out[1].URL != "" {
		t.Errorf("out[1].URL = %q, want empty when result.url empty", out[1].URL)
	}
}

// TestSearchLimitClamped verifies the limit guard: <=0 or >100 falls back to 25
// in the outgoing request.
func TestSearchLimitClamped(t *testing.T) {
	var gotLimits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimits = append(gotLimits, r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"size":0,"_links":{}}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, _, err := cf.Search(context.Background(), "q", 0, ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, _, err := cf.Search(context.Background(), "q", 500, ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, _, err := cf.Search(context.Background(), "q", 50, ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(gotLimits) != 3 || gotLimits[0] != "25" || gotLimits[1] != "25" || gotLimits[2] != "50" {
		t.Errorf("limits = %v, want [25 25 50]", gotLimits)
	}
}

// TestSearchError verifies a backend error propagates with its sentinel.
func TestSearchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, _, err := cf.Search(context.Background(), "bad cql", 25, "")
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("expected ErrUsage for 400, got %v", err)
	}
}
