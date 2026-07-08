package wikimerge

import (
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/wikimd"
)

// mergeOK runs Merge and fails the test on an unexpected error, returning the
// merged body and report.
func mergeOK(t *testing.T, base, edited string, opts Options) ([]byte, *Report) {
	t.Helper()
	out, rep, err := Merge([]byte(base), edited, opts)
	if err != nil {
		t.Fatalf("Merge errored: %v\nbase=%q edited=%q", err, base, edited)
	}
	return out, rep
}

// TestRoundTrip feeds each base body's own pristine render back as the edit and
// asserts Merge reproduces the base byte-for-byte with an all-unchanged report.
// This is the load-bearing invariant behind `jira apply` on an untouched view.
func TestRoundTrip(t *testing.T) {
	bases := []string{
		"Just a paragraph.",
		"Para A.\n\nPara B.\n\nPara C.",
		"h1. Title\n\nBody text.\n\nh2. Sub\n\nmore",
		"{code:go}\nfmt.Println(\"hi\")\n\nblank line inside\n{code}",
		"{panel:title=Note}\npanel body\n{panel}",
		"|| H1 || H2 ||\n| a | b |\n| c | d |",
		"|| Owner || Role ||\n| [~first]\n[~second]\n[~third] | DS |\n| [~lead] | Lead |",
		"* one\n* two\n** nested\n# ordered",
		"User:\n # first\n # second",
		"h2. Heading\n\ntext with trailing newline\n",
		"\n\nLeading blank lines.\n\nSecond para.",
		"one\ntwo\nthree",
	}
	for _, base := range bases {
		md := wikimd.Render(base, wikimd.Options{})
		out, rep := mergeOK(t, base, md, Options{})
		if string(out) != base {
			t.Errorf("round-trip mismatch\nbase=%q\nmd=%q\nout=%q", base, md, out)
		}
		if rep.Converted != 0 || rep.Moved != 0 || rep.Removed != 0 {
			t.Errorf("round-trip report not all-unchanged for base=%q: %+v", base, rep)
		}
		if rep.Unchanged == 0 && strings.TrimSpace(base) != "" {
			t.Errorf("round-trip reported 0 unchanged for base=%q: %+v", base, rep)
		}
	}
}

func TestEditLeadingSpaceOrderedList(t *testing.T) {
	base := "User:\n # first\n # second"
	md := wikimd.Render(base, wikimd.Options{})
	edited := strings.Replace(md, "1. second", "1. changed", 1)
	out, rep := mergeOK(t, base, edited, Options{})
	if !strings.Contains(string(out), "# changed") {
		t.Fatalf("ordered-list edit was not applied: %q", out)
	}
	if rep.Converted != 1 {
		t.Fatalf("report = %+v, want one converted list block", rep)
	}
}

func TestHeadingOffsetRoundTripAndDeepHeadingEdit(t *testing.T) {
	base := "h1. Top\n\nh5. Deep\n\nh6. Deepest"
	opts := Options{HeadingOffset: 1}
	md := wikimd.Render(base, wikimd.Options{HeadingOffset: 1})
	out, rep := mergeOK(t, base, md, opts)
	if string(out) != base || rep.Converted != 0 {
		t.Fatalf("offset round-trip: out=%q report=%+v", out, rep)
	}
	edited := strings.Replace(md, "Deepest", "Deepest edited", 1)
	out, rep = mergeOK(t, base, edited, opts)
	if !strings.Contains(string(out), "h6. Deepest edited") || rep.Converted != 1 {
		t.Fatalf("deep heading edit lost level: out=%q report=%+v", out, rep)
	}
}

// TestRoundTripCRLF asserts a CRLF base body (the shape Jira DC serves) round-trips
// byte-for-byte: the scanner keeps `\r` as line content and the assembler preserves
// the original byte ranges, so the merged output carries the same CRLF line
// endings as the base even though the pristine md render is LF-only.
func TestRoundTripCRLF(t *testing.T) {
	base := "First line.\r\nsame paragraph.\r\n\r\nSecond paragraph.\r\n"
	md := wikimd.Render(base, wikimd.Options{})
	if strings.Contains(md, "\r") {
		t.Fatalf("pristine render should be LF-only, got %q", md)
	}
	out, rep := mergeOK(t, base, md, Options{})
	if string(out) != base {
		t.Fatalf("CRLF round-trip mismatch\nbase=%q\nout=%q", base, out)
	}
	if rep.Converted != 0 {
		t.Errorf("CRLF round-trip converted %d blocks, want 0", rep.Converted)
	}
}

// TestSingleParagraphEdit: only the edited block is converted; every untouched
// base block's exact bytes reappear in the output.
func TestSingleParagraphEdit(t *testing.T) {
	base := "First paragraph, {color:red}keep me{color}.\n\nSecond paragraph.\n\nThird paragraph."
	md := wikimd.Render(base, wikimd.Options{})
	edited := strings.Replace(md, "Second paragraph.", "Second paragraph, edited.", 1)
	out, rep := mergeOK(t, base, edited, Options{})
	got := string(out)
	if !strings.Contains(got, "Second paragraph, edited.") {
		t.Errorf("edit not applied: %q", got)
	}
	// The untouched blocks keep their exact base bytes (color macro survives).
	if !strings.Contains(got, "First paragraph, {color:red}keep me{color}.") {
		t.Errorf("first block bytes not preserved: %q", got)
	}
	if !strings.Contains(got, "Third paragraph.") {
		t.Errorf("third block bytes not preserved: %q", got)
	}
	if rep.Converted != 1 || rep.Unchanged != 2 {
		t.Errorf("report = %+v, want 1 converted / 2 unchanged", rep)
	}
}

// TestMultilineParagraphEditKeepsLineBreaks pins issue #164: a base paragraph
// with an intra-paragraph line break renders to adjacent md lines; editing one
// word on one of those lines and merging must convert the block back with the
// `\n` line structure intact (not collapsed to a space-joined single line).
func TestMultilineParagraphEditKeepsLineBreaks(t *testing.T) {
	base := "line one here\nline two here"
	md := wikimd.Render(base, wikimd.Options{})
	if md != "line one here\nline two here" {
		t.Fatalf("render = %q, want the two lines adjacent", md)
	}
	edited := strings.Replace(md, "line two here", "line two edited", 1)
	out, rep := mergeOK(t, base, edited, Options{})
	want := "line one here\nline two edited"
	if string(out) != want {
		t.Errorf("merged body = %q, want %q (line break must survive)", out, want)
	}
	if rep.Converted != 1 {
		t.Errorf("report = %+v, want 1 converted", rep)
	}
}

// TestMoveReusesBaseBytes: swapping two paragraphs reports a move and reuses the
// base bytes rather than re-converting them.
func TestMoveReusesBaseBytes(t *testing.T) {
	base := "Alpha with {{mono}}.\n\nBravo plain."
	md := wikimd.Render(base, wikimd.Options{})
	blocks := strings.Split(md, "\n\n")
	if len(blocks) != 2 {
		t.Fatalf("expected 2 rendered blocks, got %d: %q", len(blocks), md)
	}
	edited := blocks[1] + "\n\n" + blocks[0] // swap
	out, rep := mergeOK(t, base, edited, Options{})
	if rep.Moved < 1 {
		t.Errorf("expected at least one move, report = %+v", rep)
	}
	// The moved block keeps its base bytes, so the monospace construct survives and
	// the loss gate stays quiet.
	if !strings.Contains(string(out), "{{mono}}") {
		t.Errorf("moved block did not reuse base bytes: %q", out)
	}
	if len(rep.RemovedConstructs) != 0 {
		t.Errorf("move should not drop constructs: %+v", rep.RemovedConstructs)
	}
}

// TestLossGate: dropping blocks that carry wiki-only constructs is refused with a
// LossError enumerating them; --allow-loss proceeds and reports them.
func TestLossGate(t *testing.T) {
	base := "Keep this paragraph.\n\n" +
		"{panel:title=Note}\npanel body\n{panel}\n\n" +
		"{color:red}colored{color} text.\n\n" +
		"Mention [~jdoe] here.\n\n" +
		"Image !diagram.png! embed."
	// Edit that keeps only the first paragraph.
	edited := "Keep this paragraph."

	_, rep, err := Merge([]byte(base), edited, Options{})
	var le *LossError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LossError, got %v", err)
	}
	if rep == nil {
		t.Fatal("LossError must carry a report")
	}
	kinds := map[string]bool{}
	for _, c := range le.Removed {
		kinds[c.Kind] = true
	}
	for _, want := range []string{"panel", "color", "mention", "image"} {
		if !kinds[want] {
			t.Errorf("loss report missing kind %q; got %+v", want, le.Removed)
		}
	}

	// Overridden: it proceeds, drops them, and populates RemovedConstructs.
	out, rep2 := mergeOK(t, base, edited, Options{AllowLoss: true})
	if strings.Contains(string(out), "{panel") || strings.Contains(string(out), "[~jdoe]") {
		t.Errorf("allow-loss output still carries dropped constructs: %q", out)
	}
	if len(rep2.RemovedConstructs) == 0 {
		t.Errorf("allow-loss must still report removed constructs")
	}
}

// TestBlockErrorOnUnconvertible: an edited block mdwiki refuses (a task-list item,
// which has no Jira wiki equivalent) aborts the whole merge with a *BlockError and
// a nil body.
func TestBlockErrorOnUnconvertible(t *testing.T) {
	base := "Plain paragraph."
	edited := "- [ ] a task item"
	out, _, err := Merge([]byte(base), edited, Options{AllowLoss: true})
	var be *BlockError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BlockError, got %v", err)
	}
	if out != nil {
		t.Errorf("BlockError must return a nil body, got %q", out)
	}
}

// TestEmptyEditEmptiesBody: an empty edit removes every base block. With no
// wiki-only constructs the body just empties (no loss); the report counts the
// removals.
func TestEmptyEditEmptiesBody(t *testing.T) {
	base := "Para one.\n\nPara two."
	out, rep := mergeOK(t, base, "", Options{})
	if string(out) != "" {
		t.Errorf("emptied body should be empty, got %q", out)
	}
	if rep.Removed != 2 {
		t.Errorf("expected 2 removed, got %+v", rep)
	}
}

// TestNewBodyFromEmptyBase: an empty base with a new edited body converts every
// block (the "base description was empty, add one" path).
func TestNewBodyFromEmptyBase(t *testing.T) {
	out, rep := mergeOK(t, "", "New first line.\n\nNew second line.", Options{})
	got := string(out)
	if !strings.Contains(got, "New first line.") || !strings.Contains(got, "New second line.") {
		t.Errorf("new body not built: %q", got)
	}
	if rep.Converted != 2 {
		t.Errorf("expected 2 converted, got %+v", rep)
	}
}

// TestEveryBaseBlockRendersToOnePiece documents why the multi-piece refusal branch
// in Merge is defensive: wikimd renders each wiki block (quote/panel/code/list/…)
// to exactly one markdown block, so a partial-match-inside-a-multi-block-construct
// never arises from a real render. If wikimd ever starts splitting a block into
// several md blocks, this fails — a signal that the multi-piece path now needs a
// behavioral test with a genuine partial edit.
func TestEveryBaseBlockRendersToOnePiece(t *testing.T) {
	multiBlock := []string{
		"{quote}\nfirst quoted para.\n\nsecond quoted para.\n{quote}",
		"{panel:title=N}\nfirst para.\n\nsecond para.\n{panel}",
		"{code}\na\n\nb\n{code}",
		"* one\n* two\n** nested",
		"|| H ||\n| a |\n| b |",
	}
	for _, base := range multiBlock {
		blocks := scanWikiBlocks(base)
		for _, blk := range blocks {
			md := wikimd.Render(base[blk.start:blk.end], wikimd.Options{})
			if n := len(splitMDBlocks(md)); n != 1 {
				t.Errorf("base block %q rendered to %d md blocks (want 1): %q", base[blk.start:blk.end], n, md)
			}
		}
	}
}

// TestFenceDashParagraphRoundTrip pins issue #167 through the merge: a base
// paragraph carrying literal lines that collide with markdown block markup (a
// ```-prefixed fence line and `---`/`***` thematic-break lines) renders to an
// escaped view, an untouched apply reproduces the base byte-for-byte, and editing
// one word ELSEWHERE in the paragraph applies cleanly while every collision line
// survives byte-identically — with no silent `----` horizontal rule introduced.
func TestFenceDashParagraphRoundTrip(t *testing.T) {
	base := "Intro word here.\n```json\nplain body text\n---\n***\ntrailer *bold* line."
	md := wikimd.Render(base, wikimd.Options{})
	// The view escapes each collision line so it stays inside the paragraph.
	for _, want := range []string{"\\```json", "\\---", "\\***"} {
		if !strings.Contains(md, want) {
			t.Fatalf("view missing escaped line %q:\n%s", want, md)
		}
	}

	// Untouched apply: byte-identical.
	out, rep := mergeOK(t, base, md, Options{})
	if string(out) != base {
		t.Fatalf("untouched round-trip mismatch\nbase=%q\nout=%q", base, out)
	}
	if rep.Converted != 0 || rep.Removed != 0 {
		t.Errorf("untouched apply not all-unchanged: %+v", rep)
	}

	// Edit one word elsewhere: applies, collision lines survive byte-identically.
	edited := strings.Replace(md, "Intro word here.", "Intro word HERE.", 1)
	out2, rep2 := mergeOK(t, base, edited, Options{})
	want := strings.Replace(base, "Intro word here.", "Intro word HERE.", 1)
	if string(out2) != want {
		t.Fatalf("edited round-trip mismatch\ngot=%q\nwant=%q", out2, want)
	}
	if strings.Contains(string(out2), "----") {
		t.Fatalf("silent horizontal rule introduced: %q", out2)
	}
	if rep2.Converted != 1 {
		t.Errorf("report = %+v, want 1 converted", rep2)
	}
}
