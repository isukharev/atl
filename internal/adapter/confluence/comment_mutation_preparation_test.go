package confluence

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/net/html"

	"github.com/isukharev/atl/internal/domain"
)

func preparationHTML(headExtra, wiki string) string {
	return `<!doctype html><html><head>
<meta name="confluence-request-time" content="1700000000123">
<meta name="ajs-page-id" content="10">
<meta name="ajs-latest-page-id" content="10">
<meta name="ajs-page-version" content="7">` + headExtra + `</head><body>
<main id="content"><div data-b="2" class="wiki-content" data-a="1">` + wiki + `</div></main>
</body></html>`
}

func TestPrepareInlineCommentFromHTMLDerivesExactBrowserGeometry(t *testing.T) {
	html := preparationHTML("", `<p>alpha beta <strong>gamma</strong> delta</p><p><span class="inline-comment-marker" data-ref="ref-2">marked</span></p>`)
	request := domain.ConfluenceInlineCommentPreparationRequest{
		PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "beta gamma", MatchIndex: 0,
	}
	prepared, err := prepareInlineCommentFromHTML([]byte(html), request)
	if err != nil {
		t.Fatalf("prepareInlineCommentFromHTML: %v", err)
	}
	wantHighlights := []domain.ConfluenceInlineHighlightGeometry{
		{Text: "beta ", ChildIndexPath: []int{0, 1}, PreviousTextSiblingOffset: 6, Length: 5},
		{Text: "gamma", ChildIndexPath: []int{0, 2, 0}, PreviousTextSiblingOffset: 0, Length: 5},
	}
	if prepared.PageID != "10" || prepared.PageVersion != 7 || prepared.LastFetchTime != 1700000000123 ||
		prepared.SearchSelection != "beta gamma" || prepared.OriginalSelection != "beta gamma" || prepared.HighlightedSelection != "beta gamma" ||
		prepared.NumMatches != 1 || prepared.MatchIndex != 0 || len(prepared.ViewSHA256) != 64 ||
		!reflect.DeepEqual(prepared.MarkerRefs, []string{"ref-2"}) || !reflect.DeepEqual(prepared.SerializedHighlights, wantHighlights) {
		t.Fatalf("prepared = %+v, highlights=%+v", prepared, prepared.SerializedHighlights)
	}
	serialized, err := serializeInlineHighlights(prepared.SerializedHighlights)
	if err != nil {
		t.Fatal(err)
	}
	if serialized != `[["beta ","0:1",6,5],["gamma","0:2:0",0,5]]` {
		t.Fatalf("serialized highlights = %s", serialized)
	}
}

func TestPinnedRangeHelperSelectionUsesExactJQuery224Trim(t *testing.T) {
	edges := []rune{'\u0009', '\u000a', '\u000b', '\u000c', '\u000d', '\u0020', '\u00a0', '\u1680',
		'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a',
		'\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff'}
	for _, edge := range edges {
		if got := pinnedRangeHelperSelection(string(edge) + "target" + string(edge)); got != "target" {
			t.Errorf("edge U+%04X normalized to %q", edge, got)
		}
	}
	if got := pinnedRangeHelperSelection("\u0085target\u0085"); got != "\u0085target\u0085" {
		t.Fatalf("U+0085 was incorrectly trimmed: %q", got)
	}
	if got := pinnedRangeHelperSelection(" \u00a0target\u00a0 "); got != "target" {
		t.Fatalf("NBSP normalization = %q", got)
	}
}

func TestHTMLClassUsesCSSASCIIWhitespaceOnly(t *testing.T) {
	document, err := html.Parse(strings.NewReader(`<div><span id="nbsp" class="x&nbsp;jira-issue"></span><span id="ascii" class="x jira-issue"></span></div>`))
	if err != nil {
		t.Fatal(err)
	}
	nbsp := exactlyOneElement(document, func(node *html.Node) bool { return htmlAttribute(node, "id") == "nbsp" })
	ascii := exactlyOneElement(document, func(node *html.Node) bool { return htmlAttribute(node, "id") == "ascii" })
	verticalTab := &html.Node{Type: html.ElementNode, Data: "span", Attr: []html.Attribute{{Key: "class", Val: "x\vjira-issue"}}}
	if htmlClass(nbsp, "jira-issue") || !htmlClass(ascii, "jira-issue") || htmlClass(verticalTab, "jira-issue") {
		t.Fatalf("CSS class tokenization mismatch: nbsp=%t ascii=%t vt=%t",
			htmlClass(nbsp, "jira-issue"), htmlClass(ascii, "jira-issue"), htmlClass(verticalTab, "jira-issue"))
	}
}

func TestHTMLAttributeNamesUseASCIICaseFoldingOnly(t *testing.T) {
	ascii := &html.Node{Type: html.ElementNode, Data: "span", Attr: []html.Attribute{{Key: "CLASS", Val: "jira-issue"}}}
	unicodeFold := &html.Node{Type: html.ElementNode, Data: "span", Attr: []html.Attribute{{Key: "claſs", Val: "jira-issue"}}}
	if !htmlClass(ascii, "jira-issue") || htmlClass(unicodeFold, "jira-issue") || htmlAttributePresent(unicodeFold, "class") {
		t.Fatalf("HTML attribute-name folding mismatch: ascii=%t unicode=%t present=%t",
			htmlClass(ascii, "jira-issue"), htmlClass(unicodeFold, "jira-issue"), htmlAttributePresent(unicodeFold, "class"))
	}
}

func TestPrepareInlineCommentFromHTMLSeparatesSearchWireAndRawSelection(t *testing.T) {
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", `<p>alpha&nbsp;beta
line</p>`)), domain.ConfluenceInlineCommentPreparationRequest{
		PageID: "10", ExpectedPageVersion: 7, OriginalSelection: " \ufeffalpha\u00a0beta\nline\u3000", MatchIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SearchSelection != "alpha beta\nline" || prepared.OriginalSelection != "alpha betaline" ||
		prepared.HighlightedSelection != "alpha\u00a0beta\nline" || len(prepared.SerializedHighlights) != 1 ||
		prepared.SerializedHighlights[0].Text != "alpha\u00a0beta\nline" {
		t.Fatalf("normalized preparation = %+v", prepared)
	}
}

func TestPrepareInlineCommentFromHTMLSkipsWhitespaceOnlyHighlightNodes(t *testing.T) {
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", "<p>alpha</p> \n <p>beta</p>")),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "alpha \n beta", MatchIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SearchSelection != "alpha \n beta" || prepared.OriginalSelection != "alpha  beta" ||
		prepared.HighlightedSelection != "alphabeta" || len(prepared.SerializedHighlights) != 2 {
		t.Fatalf("prepared = %+v", prepared)
	}
}

func TestPrepareInlineCommentFromHTMLMasksMatchesAndShiftsOverlappingIndex(t *testing.T) {
	body := `<p>aaa </p><a href="/masked">aa</a><p> aa</p>`
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", body)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "aa", MatchIndex: 3})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.NumMatches != 3 || prepared.MatchIndex != 2 || prepared.HighlightedSelection != "aa" {
		t.Fatalf("shifted preparation = %+v", prepared)
	}
	if _, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", body)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "aa", MatchIndex: 2}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("masked selected occurrence error = %v", err)
	}
}

func TestPrepareInlineCommentFromHTMLUsesNonOverlappingMaskedReplacement(t *testing.T) {
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", `<a href="#masked">aaa</a>aa`)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "aa", MatchIndex: 3})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.NumMatches != 2 || prepared.MatchIndex != 1 || prepared.HighlightedSelection != "aa" {
		t.Fatalf("pinned global replacement preparation = %+v", prepared)
	}
}

func TestPrepareInlineCommentFromHTMLMasksOnlyTopmostNestedElement(t *testing.T) {
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", `<a href="/masked">aa<span class="user-mention">aaa</span></a>aaa`)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "aaa", MatchIndex: 5})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.NumMatches != 3 || prepared.MatchIndex != 2 || prepared.HighlightedSelection != "aaa" {
		t.Fatalf("pinned destructive mask preparation = %+v", prepared)
	}
}

func TestPrepareInlineCommentFromHTMLUsesRawTextForConditionalMaskEligibility(t *testing.T) {
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", `<span class="jira-issue">x&nbsp;y</span><p>x y</p>`)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "x y", MatchIndex: 1})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.NumMatches != 2 || prepared.MatchIndex != 1 || prepared.HighlightedSelection != "x y" {
		t.Fatalf("raw conditional mask preparation = %+v", prepared)
	}
}

func TestBrowserSelectedMaskElementsBoundsDeepConditionalDOM(t *testing.T) {
	const depth = 512
	root := &html.Node{Type: html.ElementNode, Data: "div"}
	ranges := map[*html.Node]inlinePreparationDOMRange{root: {start: 0, end: 1 << 20}}
	current := root
	for range depth {
		next := &html.Node{Type: html.ElementNode, Data: "span", Attr: []html.Attribute{{Key: "class", Val: "jira-issue"}}}
		current.AppendChild(next)
		ranges[next] = inlinePreparationDOMRange{start: 0, end: 1 << 20}
		current = next
	}
	current.AppendChild(&html.Node{Type: html.TextNode, Data: strings.Repeat("x", (1<<20)-6) + "needle"})
	rawText := utf16.Encode([]rune(current.FirstChild.Data))
	selected := browserSelectedMaskElements(root, rawText, ranges, utf16.Encode([]rune("needle")))
	if len(selected) != depth {
		t.Fatalf("selected conditional masks = %d, want %d", len(selected), depth)
	}
}

func TestBrowserMaskedPreparationTextPreservesSelectionWhitespace(t *testing.T) {
	document, err := html.Parse(strings.NewReader(preparationHTML("", "<a href=\"#masked\">x\ty\n</a><p>x\ty\n</p>")))
	if err != nil {
		t.Fatal(err)
	}
	content := exactlyOneElement(document, func(node *html.Node) bool { return htmlAttribute(node, "id") == "content" })
	wiki := exactlyOneElementBelow(content, func(node *html.Node) bool { return htmlClass(node, "wiki-content") })
	text, rawText, _, ranges := preparationText(wiki)
	selection := utf16.Encode([]rune("x\ty\n"))
	_, selectedStart := countUTF16Matches(text, selection, 1)
	masked, _, err := browserMaskedPreparationText(wiki, text, rawText, ranges, selection, selectedStart, selectedStart+len(selection))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(utf16.Decode(masked)), " \t \nx\ty\n"; got != want {
		t.Fatalf("masked text = %q, want %q", got, want)
	}
}

func TestPrepareInlineCommentFromHTMLRejectsPinnedMaskedAndFooterFallbackClasses(t *testing.T) {
	tests := map[string]string{
		"internal link":         `<a href="/internal">target</a>`,
		"fragment link":         `<a href="#fragment">target</a>`,
		"mention":               `<a class="user-mention" href="/profile">target</a>`,
		"panel header":          `<div data-macro-name="panel"><div class="panelHeader">target</div></div>`,
		"macro render":          `<div data-macro-name="sample"><span class="conf-macro-render">target</span></div>`,
		"bodyless macro":        `<span class="conf-macro" data-hasbody="false">target</span>`,
		"code macro":            `<div class="conf-macro" data-macro-name="code">target</div>`,
		"include macro":         `<div class="conf-macro" data-macro-name="include">target</div>`,
		"jira issue":            `<span class="jira-issue">target</span>`,
		"jira issues":           `<span class="jira-issues">target</span>`,
		"date lozenge":          `<time class="date-past">target</time>`,
		"floating table header": `<table><thead class="tableFloatingHeader"><tr><th>copy</th></tr></thead></table><p>target</p>`,
	}
	for name, wiki := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", wiki)),
				domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 0})
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", `<a href="https://example.invalid/path">target</a>`)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 0})
	if err != nil || prepared.HighlightedSelection != "target" {
		t.Fatalf("external link preparation = %+v, err=%v", prepared, err)
	}
}

func TestPrepareInlineCommentFromHTMLRejectsHighlighterSkippedTags(t *testing.T) {
	for _, tag := range []string{"script", "style", "select", "button", "object", "applet"} {
		t.Run(tag, func(t *testing.T) {
			wiki := "<" + tag + ">target</" + tag + ">"
			_, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", wiki)),
				domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 0})
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
}

func TestPrepareInlineCommentFromHTMLRejectsRangeCrossingBodylessMacro(t *testing.T) {
	wiki := `<p>left <span class="conf-macro" data-hasbody="false"></span>target</p>`
	_, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", wiki)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "left target", MatchIndex: 0})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want ErrCheckFailed", err)
	}
}

func TestPrepareInlineCommentFromHTMLSelectsZeroBasedOccurrence(t *testing.T) {
	prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", `<p>target xx target</p>`)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ConfluenceInlineHighlightGeometry{{Text: "target", ChildIndexPath: []int{0, 1}, PreviousTextSiblingOffset: 10, Length: 6}}
	if prepared.NumMatches != 2 || !reflect.DeepEqual(prepared.SerializedHighlights, want) {
		t.Fatalf("prepared = %+v", prepared)
	}
}

func TestPreparationFingerprintUsesCanonicalWikiContentOnly(t *testing.T) {
	first := preparationHTML(`<script>head-one()</script>`, `<p z="2" a="1">stable other</p><script>dynamic-one()</script><!-- one -->`)
	second := strings.ReplaceAll(preparationHTML(`<script>head-two()</script>`, `<p a="1" z="2">stable other</p><script>dynamic-two()</script><!-- two -->`),
		`content="1700000000123"`, `content="1700000000999"`)
	request := domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "stable", MatchIndex: 0}
	a, err := prepareInlineCommentFromHTML([]byte(first), request)
	if err != nil {
		t.Fatal(err)
	}
	b, err := prepareInlineCommentFromHTML([]byte(second), request)
	if err != nil {
		t.Fatal(err)
	}
	if a.ViewSHA256 != b.ViewSHA256 {
		t.Fatalf("canonical view hashes differ: %s != %s", a.ViewSHA256, b.ViewSHA256)
	}
	otherSelection, err := prepareInlineCommentFromHTML([]byte(first),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "other", MatchIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	if otherSelection.ViewSHA256 != a.ViewSHA256 {
		t.Fatal("view hash depends on transient highlight wrappers")
	}
	if reflect.DeepEqual(otherSelection.SerializedHighlights, a.SerializedHighlights) {
		t.Fatal("different selections unexpectedly produced identical geometry")
	}
	changed, err := prepareInlineCommentFromHTML([]byte(strings.Replace(first, "stable", "changed", 1)),
		domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "changed", MatchIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ViewSHA256 == a.ViewSHA256 {
		t.Fatal("content change did not change canonical view hash")
	}
}

func TestPrepareInlineCommentFromHTMLFailsClosed(t *testing.T) {
	base := preparationHTML("", `<p>target</p>`)
	request := domain.ConfluenceInlineCommentPreparationRequest{PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 0}
	tests := map[string]string{
		"missing request time":   strings.Replace(base, `<meta name="confluence-request-time" content="1700000000123">`, "", 1),
		"duplicate request time": strings.Replace(base, `</head>`, `<meta name="confluence-request-time" content="1700000000123"></head>`, 1),
		"wrong page id":          strings.Replace(base, `name="ajs-page-id" content="10"`, `name="ajs-page-id" content="11"`, 1),
		"wrong version":          strings.Replace(base, `name="ajs-page-version" content="7"`, `name="ajs-page-version" content="8"`, 1),
		"duplicate content":      strings.Replace(base, `<main id="content">`, `<main id="content"></main><main id="content">`, 1),
		"duplicate wiki":         strings.Replace(base, `</main>`, `<div class="wiki-content">target</div></main>`, 1),
		"malformed marker":       strings.Replace(base, `<p>target</p>`, `<p><span class="inline-comment-marker">target</span></p>`, 1),
		"marker overlap":         strings.Replace(base, `<p>target</p>`, `<p><span class="inline-comment-marker" data-ref="ref">target</span></p>`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareInlineCommentFromHTML([]byte(body), request); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
	if _, err := prepareInlineCommentFromHTML([]byte(base), domain.ConfluenceInlineCommentPreparationRequest{
		PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "absent", MatchIndex: 0,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing selection error = %v", err)
	}
	if _, err := prepareInlineCommentFromHTML([]byte(base), domain.ConfluenceInlineCommentPreparationRequest{
		PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 1,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing occurrence error = %v", err)
	}
}

func TestCommentMutationProviderPreparationUsesFixedBoundedRoute(t *testing.T) {
	events := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events = append(events, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/wiki/rest/api/server-information":
			writeTestExactMetadata(w)
		case "/wiki/pages/viewpage.action":
			if r.Method != http.MethodGet || r.URL.Query().Get("pageId") != "10" || len(r.URL.Query()) != 1 {
				t.Errorf("preparation request = %s %s", r.Method, r.URL.RequestURI())
			}
			if got := r.Header.Get("Accept"); got != "text/html" {
				t.Errorf("Accept = %q", got)
			}
			_, _ = io.WriteString(w, preparationHTML("", `<p>target</p>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := mustCommentMutationProvider(t, srv.URL+"/wiki")
	prepared, err := provider.PrepareConfluenceInlineComment(context.Background(), domain.ConfluenceInlineCommentPreparationRequest{
		PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 0,
	})
	if err != nil || prepared.LastFetchTime != 1700000000123 {
		t.Fatalf("preparation = %+v, err=%v", prepared, err)
	}
	wantEvents := []string{"GET /wiki/rest/api/server-information", "GET /wiki/pages/viewpage.action?pageId=10"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestCommentMutationProviderPreparationDoesNotFollowRedirect(t *testing.T) {
	var viewHits, redirectedHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/server-information":
			writeTestExactMetadata(w)
		case "/pages/viewpage.action":
			viewHits++
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
		case "/redirected":
			redirectedHits++
		}
	}))
	defer srv.Close()
	provider := mustCommentMutationProvider(t, srv.URL)
	_, err := provider.PrepareConfluenceInlineComment(context.Background(), domain.ConfluenceInlineCommentPreparationRequest{
		PageID: "10", ExpectedPageVersion: 7, OriginalSelection: "target", MatchIndex: 0,
	})
	if err == nil || viewHits != 1 || redirectedHits != 0 || strings.Contains(err.Error(), "pageId") {
		t.Fatalf("error=%v view_hits=%d redirected_hits=%d", err, viewHits, redirectedHits)
	}
}

func FuzzPrepareInlineCommentFromRenderedHTML(f *testing.F) {
	for _, seed := range []string{
		`<p>target</p>`,
		`<p>tar<strong>get</strong></p>`,
		`<a href="/internal">target</a>`,
		`<div data-macro-name="panel"><div class="panelHeader">target</div></div>`,
		`<p>target&nbsp;target</p>`,
		`<select><option>target</option></select>`,
	} {
		f.Add(seed, "target", 0)
	}
	f.Fuzz(func(t *testing.T, wiki, selection string, occurrence int) {
		if len(wiki) > 1<<20 || len(selection) > 1<<16 || occurrence < 0 || occurrence > 32 || selection == "" {
			t.Skip()
		}
		prepared, err := prepareInlineCommentFromHTML([]byte(preparationHTML("", wiki)),
			domain.ConfluenceInlineCommentPreparationRequest{
				PageID: "10", ExpectedPageVersion: 7, OriginalSelection: selection, MatchIndex: occurrence,
			})
		if err != nil {
			return
		}
		if prepared.SearchSelection != pinnedRangeHelperSelection(selection) ||
			prepared.OriginalSelection != strings.ReplaceAll(prepared.SearchSelection, "\n", "") ||
			prepared.NumMatches <= 0 || prepared.MatchIndex < 0 || prepared.MatchIndex >= prepared.NumMatches ||
			len(prepared.SerializedHighlights) == 0 {
			t.Fatalf("invalid successful preparation: %+v", prepared)
		}
		var highlighted strings.Builder
		for _, descriptor := range prepared.SerializedHighlights {
			highlighted.WriteString(descriptor.Text)
			if descriptor.Text == "" || descriptor.Length != len(utf16.Encode([]rune(descriptor.Text))) ||
				len(descriptor.ChildIndexPath) == 0 || descriptor.PreviousTextSiblingOffset < 0 {
				t.Fatalf("invalid descriptor: %+v", descriptor)
			}
		}
		if highlighted.String() != prepared.HighlightedSelection {
			t.Fatalf("highlighted selection mismatch: %q != %q", highlighted.String(), prepared.HighlightedSelection)
		}
		if _, err := serializeInlineHighlights(prepared.SerializedHighlights); err != nil {
			t.Fatalf("serialize successful preparation: %v", err)
		}
	})
}
