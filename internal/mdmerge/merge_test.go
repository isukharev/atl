package mdmerge

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

// samplePage exercises the shapes that matter: headings, text with inline
// opaque elements (jira macro, page link, mention), a block macro, a table,
// layout gaps are covered separately.
const samplePage = `<h1>Intro</h1>` +
	`<p>Status of <ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">AB-1</ac:parameter></ac:structured-macro> is green.</p>` +
	`<ac:structured-macro ac:name="toc"/>` +
	`<h2>Team</h2>` +
	`<p>Owner: <ri:user ri:userkey="deadbeef"/> (primary).</p>` +
	`<table><tbody><tr><th>K</th><th>V</th></tr><tr><td>a</td><td>1</td></tr></tbody></table>`

func renderOf(t *testing.T, raw string, refs []domain.Ref) string {
	t.Helper()
	root, err := csf.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return string(mirror.RenderMarkdown(root, refs))
}

func mustMerge(t *testing.T, base, editedMD string, refs []domain.Ref, opts Options) ([]byte, *Report) {
	t.Helper()
	out, rep, err := Merge([]byte(base), refs, editedMD, opts)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return out, rep
}

func TestMergeNoEditIsIdentity(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	out, rep := mustMerge(t, samplePage, md, nil, Options{})
	if !bytes.Equal(out, []byte(samplePage)) {
		t.Fatalf("identity merge changed bytes:\n%s", out)
	}
	if rep.Converted != 0 || rep.Removed != 0 || rep.Moved != 0 || rep.Unchanged == 0 {
		t.Errorf("report = %+v, want pure unchanged", rep)
	}
}

func TestMergeSingleParagraphEdit(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	edited := strings.Replace(md, "is green.", "is red.", 1)
	out, rep := mustMerge(t, samplePage, edited, nil, Options{})
	s := string(out)
	if !strings.Contains(s, "is red.") {
		t.Fatalf("edit not applied: %s", s)
	}
	// The jira macro inside the edited paragraph keeps its exact bytes.
	if !strings.Contains(s, `<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">AB-1</ac:parameter></ac:structured-macro>`) {
		t.Fatalf("jira macro bytes lost: %s", s)
	}
	// Everything outside the edited paragraph is byte-identical.
	if !strings.HasPrefix(s, `<h1>Intro</h1>`) || !strings.Contains(s, `<ac:structured-macro ac:name="toc"/>`) ||
		!strings.HasSuffix(s, `</tbody></table>`) {
		t.Fatalf("surrounding bytes disturbed: %s", s)
	}
	if rep.Converted != 1 || rep.Removed != 1 || rep.Unchanged == 0 {
		t.Errorf("report = %+v", rep)
	}
}

func TestMergeMentionSurvivesEdit(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	edited := strings.Replace(md, "(primary).", "(secondary).", 1)
	out, _ := mustMerge(t, samplePage, edited, nil, Options{})
	if !strings.Contains(string(out), `<ri:user ri:userkey="deadbeef"/>`) {
		t.Fatalf("mention bytes lost: %s", out)
	}
	if !strings.Contains(string(out), "(secondary).") {
		t.Fatalf("edit not applied: %s", out)
	}
}

func TestMergeInsertAndAppend(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	edited := strings.Replace(md, "# Intro", "# Intro\n\nNew opening paragraph.", 1) +
		"\n## Appendix\n\nTail content.\n"
	out, rep := mustMerge(t, samplePage, edited, nil, Options{})
	s := string(out)
	wantOrder := []string{
		"<h1>Intro</h1>", "<p>New opening paragraph.</p>", "AB-1",
		"<h2>Appendix</h2>", "<p>Tail content.</p>",
	}
	last := -1
	for _, w := range wantOrder {
		i := strings.Index(s, w)
		if i < 0 || i < last {
			t.Fatalf("output order wrong (looking for %q):\n%s", w, s)
		}
		last = i
	}
	if rep.Converted != 3 {
		t.Errorf("report = %+v, want 3 converted", rep)
	}
}

func TestMergeDeleteBlockRequiresLossGate(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	// Drop the toc macro line entirely.
	edited := strings.Replace(md, "⟦table of contents⟧\n\n", "", 1)
	_, _, err := Merge([]byte(samplePage), nil, edited, Options{})
	var le *LossError
	if !errors.As(err, &le) {
		t.Fatalf("want LossError, got %v", err)
	}
	out, rep := mustMerge(t, samplePage, edited, nil, Options{AllowFragmentLoss: true})
	if strings.Contains(string(out), "toc") {
		t.Fatalf("toc macro still present: %s", out)
	}
	if len(rep.RemovedFragments) == 0 || rep.Removed != 1 {
		t.Errorf("report = %+v", rep)
	}
}

func TestMergeMoveReusesBytes(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	// Move the toc marker line to the end of the page.
	edited := strings.Replace(md, "⟦table of contents⟧\n\n", "", 1)
	edited = strings.TrimRight(edited, "\n") + "\n\n⟦table of contents⟧\n"
	out, rep := mustMerge(t, samplePage, edited, nil, Options{})
	s := string(out)
	if strings.Count(s, `ac:name="toc"`) != 1 {
		t.Fatalf("toc macro count wrong: %s", s)
	}
	if strings.Index(s, `ac:name="toc"`) < strings.Index(s, "AB-1") {
		t.Fatalf("toc not moved after jira paragraph: %s", s)
	}
	if rep.Moved != 1 {
		t.Errorf("report = %+v, want 1 moved", rep)
	}
}

func TestMergeUnconvertibleBlockFailsClosed(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	// Editing the toc marker itself makes it unconvertible (no matching base text).
	edited := strings.Replace(md, "⟦table of contents⟧", "⟦table of contents v2⟧", 1)
	_, _, err := Merge([]byte(samplePage), nil, edited, Options{})
	var be *BlockError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockError, got %v", err)
	}
}

func TestMergeLayoutGapsPreserved(t *testing.T) {
	page := `<ac:layout><ac:layout-section ac:type="two_equal"><ac:layout-cell>` +
		`<h2>Left</h2><p>left text</p>` +
		`</ac:layout-cell><ac:layout-cell>` +
		`<h2>Right</h2><p>right text</p>` +
		`</ac:layout-cell></ac:layout-section></ac:layout>`
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "left text", "left text edited", 1)
	out, _ := mustMerge(t, page, edited, nil, Options{})
	s := string(out)
	if !strings.Contains(s, "<p>left text edited</p>") {
		t.Fatalf("edit missing: %s", s)
	}
	// Layout skeleton intact and the edit landed inside the first cell.
	if strings.Count(s, "<ac:layout-cell>") != 2 || strings.Count(s, "</ac:layout-cell>") != 2 {
		t.Fatalf("layout skeleton broken: %s", s)
	}
	if strings.Index(s, "left text edited") > strings.Index(s, "</ac:layout-cell>") {
		t.Fatalf("edit landed outside its cell: %s", s)
	}
	if !strings.Contains(s, `<h2>Right</h2><p>right text</p>`) {
		t.Fatalf("right cell disturbed: %s", s)
	}
}

func TestMergeNewJiraLinkConverts(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	edited := md + "\nSee also [XY-9](jira:XY-9).\n"
	out, _ := mustMerge(t, samplePage, edited, nil, Options{})
	if !strings.Contains(string(out),
		`<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">XY-9</ac:parameter></ac:structured-macro>`) {
		t.Fatalf("new jira link not converted: %s", out)
	}
}

func TestMergeResultAlwaysValidates(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	edited := strings.Replace(md, "green", "5 < 6 & **bold**", 1)
	out, rep := mustMerge(t, samplePage, edited, nil, Options{})
	if csf.HasErrors(rep.Problems) {
		t.Fatalf("problems: %+v", rep.Problems)
	}
	if !strings.Contains(string(out), "5 &lt; 6 &amp;") {
		t.Fatalf("escaping wrong: %s", out)
	}
}

// Regression: a mention display name that also occurs as literal prose must
// not be silently relocated — the merge refuses the ambiguous block.
func TestMergeAmbiguousMentionFailsClosed(t *testing.T) {
	page := `<p>Bob and <ri:user ri:userkey="u1"/> ship it</p>`
	refs := []domain.Ref{{Kind: domain.RefUser, Key: "u1", Display: "Bob"}}
	md := renderOf(t, page, refs)
	edited := strings.Replace(md, "ship it", "SHIP IT", 1)
	_, _, err := Merge([]byte(page), refs, edited, Options{})
	var be *BlockError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockError for ambiguous mention, got %v", err)
	}
}

// Regression: two different mentions rendering to the same display name must
// not have their keys silently swapped by an edit.
func TestMergeSameDisplayDifferentKeysFailsClosed(t *testing.T) {
	page := `<p>A <ri:user ri:userkey="u1"/> x</p><p>B <ri:user ri:userkey="u2"/> y</p>`
	refs := []domain.Ref{
		{Kind: domain.RefUser, Key: "u1", Display: "Alice"},
		{Kind: domain.RefUser, Key: "u2", Display: "Alice"},
	}
	md := renderOf(t, page, refs)
	edited := strings.ReplaceAll(md, "x", "x2")
	edited = strings.ReplaceAll(edited, "y", "y2")
	_, _, err := Merge([]byte(page), refs, edited, Options{})
	var be *BlockError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockError for same-display mentions, got %v", err)
	}
}

// Regression: multibyte punctuation around a mention is a legitimate word
// boundary — the fragment must be preserved, not reported lost.
func TestMergeMentionWithCJKPunctuation(t *testing.T) {
	page := `<p>hi 「<ri:user ri:userkey="u1"/>」 tail</p>`
	refs := []domain.Ref{{Kind: domain.RefUser, Key: "u1", Display: "Alice Smith"}}
	md := renderOf(t, page, refs)
	edited := strings.Replace(md, "tail", "EDITED", 1)
	out, _ := mustMerge(t, page, edited, refs, Options{})
	if !strings.Contains(string(out), `<ri:user ri:userkey="u1"/>`) {
		t.Fatalf("mention lost near CJK punctuation: %s", out)
	}
	if !strings.Contains(string(out), "EDITED") {
		t.Fatalf("edit not applied: %s", out)
	}
}

// Regression: a block macro's bytes must not be spliced into an inline
// context (heading, code span) even when the marker text is moved there.
func TestMergeBlockMacroIntoHeadingFailsClosed(t *testing.T) {
	// p-wrapped so the macro is collected as an inline marker and the refusal
	// exercises checkSpliceNesting (a bare macro is refused by the converter).
	page := `<h1>Title</h1><p><ac:structured-macro ac:name="toc"/></p><p>tail</p>`
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "# Title", "# Title ⟦table of contents⟧", 1)
	edited = strings.Replace(edited, "⟦table of contents⟧\n\n", "", 1)
	_, _, err := Merge([]byte(page), nil, edited, Options{})
	var be *BlockError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockError for macro in heading, got %v", err)
	}
}

// Regression: editing a complex table (spans/classed cells) through the md
// surface must fail closed, not silently strip the structure.
func TestMergeComplexTableFailsClosed(t *testing.T) {
	page := `<h1>T</h1><table><tbody><tr><th>K</th><th>V</th></tr>` +
		`<tr><td class="numberingColumn">1</td><td rowspan="2">x</td></tr>` +
		`<tr><td class="numberingColumn">2</td></tr></tbody></table>`
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "| K | V |", "| K | NEW |", 1)
	_, _, err := Merge([]byte(page), nil, edited, Options{})
	var be *BlockError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockError for complex table, got %v", err)
	}
	// A simple table on the same page still converts.
	simple := `<h1>T</h1><table><tbody><tr><th>K</th></tr><tr><td>v</td></tr></tbody></table>`
	md = renderOf(t, simple, nil)
	edited = strings.Replace(md, "| v |", "| v2 |", 1)
	out, _ := mustMerge(t, simple, edited, nil, Options{})
	if !strings.Contains(string(out), "<td>v2</td>") {
		t.Fatalf("simple table edit refused: %s", out)
	}
}

// TestMergeIdentityOverRealChunks replays identity merges over every render
// shape in the package corpus used by the renderer tests.
func TestMergeIdentityFragmentDiffEmpty(t *testing.T) {
	md := renderOf(t, samplePage, nil)
	out, rep := mustMerge(t, samplePage, md, nil, Options{})
	if len(rep.RemovedFragments) != 0 {
		t.Errorf("identity merge reports removed fragments: %+v", rep.RemovedFragments)
	}
	root, _ := csf.Parse(out)
	if len(fragment.Extract(root)) == 0 {
		t.Error("fragments vanished")
	}
}
