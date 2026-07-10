package wikimerge

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/wikimd"
)

// seedBodies are representative Jira wiki bodies covering the constructs the
// scanner and merge must handle. They double as deterministic regression seeds
// under plain `go test`.
var seedBodies = []string{
	"",
	"Just a paragraph.",
	"Para A.\n\nPara B.\n\nPara C.",
	"h1. Title\n\nBody *bold* text.\n\nh2. Sub\n\nmore",
	"{code:go}\nfmt.Println(\"hi\")\n{code}",
	"{noformat}\nraw text\n{noformat}",
	"{quote}\nquoted line\n{quote}",
	"{panel:title=Note}\npanel body\n{panel}",
	"|| H1 || H2 ||\n| a | b |\n| c | d |",
	"|| People || Role ||\n| first@example.test\nsecond@example.test\nthird@example.test | builders |\n\nAfter table.",
	"* one\n* two\n** nested\n# ordered",
	"Text with {color:red}colored{color} span.",
	"Mention [~jdoe] and image !pic.png! embed.",
	"A [link text|https://example.com] here.",
	"Inline {{monospace}} code.",
	"----\n\nAfter a rule.",
	"\n\nLeading blanks.\n\n\nMultiple gaps.\n\n",
	"line one\r\nline two\r\n\r\npara two",
	// A paragraph line immediately followed (no blank line) by a list/heading:
	// wikimd must separate the two blocks so the view re-splits into the same
	// blocks it renders from (round-trip regression, fuzz f6b53e1f).
	"0\n# ",
	"para\n* item\n* two",
	"text\n|| H ||\n| a |",
}

// FuzzMerge asserts Merge never panics on arbitrary (baseWiki, editedMD) input,
// and — the load-bearing invariant — that feeding a base body's own pristine
// markdown render back as the edit round-trips to the base byte-for-byte.
//
// The byte-exact round-trip is asserted only when every base block is *visible*
// in the render. wikimd is a deliberately lossy read view: a degenerate wiki block
// can render to whitespace-only markdown (a lone "|", a bare "\\", …) and so has
// no representation the user could re-submit — it cannot byte-round-trip, by nature
// of the lossy view, not through a merge bug. For those inputs the guarantee is the
// weaker (still important) one: Merge does not error or panic. Real issue bodies
// have visible blocks and get the strong guarantee.
func FuzzMerge(f *testing.F) {
	for _, base := range seedBodies {
		f.Add(base, wikimd.Render(base, wikimd.Options{}))
		f.Add(base, base)
		f.Add(base, "")
	}
	f.Fuzz(func(t *testing.T, base, edited string) {
		// Never panic on arbitrary edits.
		_, _, _ = Merge([]byte(base), edited, Options{AllowLoss: true})

		// Feed the base's own pristine render back as the edit.
		md := wikimd.Render(base, wikimd.Options{})
		out, _, err := Merge([]byte(base), md, Options{AllowLoss: true})
		if err != nil {
			// A lossy read view can emit markdown that a strict parser re-reads
			// ambiguously (a literal ``` becomes a fence opener, …). The merge then
			// fails closed — that is acceptable and safe, but it must be a *BlockError
			// ("edit the .wiki directly"), never a panic or a silent bad merge.
			var be *BlockError
			if !errors.As(err, &be) {
				t.Fatalf("pristine round-trip errored with non-BlockError: %v\nbase=%q\nmd=%q", err, base, md)
			}
			return
		}
		// When wikimd renders block-compositionally, the round-trip is byte-exact.
		if blockCompositional(base) && string(out) != base {
			t.Fatalf("pristine round-trip mismatch\nbase=%q\nmd=%q\nout=%q", base, md, out)
		}
	})
}

// blockCompositional reports whether wikimd's whole-body render equals the
// blank-joined renders of the individual base blocks — i.e. no block rendered to
// nothing, and re-parsing the view recovers exactly the per-block markdown the
// merge aligns against. This is precisely the condition under which the pristine
// round-trip is byte-exact: every base block's unit then matches its counterpart in
// the edited view and is reproduced from its base bytes.
//
// It fails for the lossy-view edge cases where wiki text is re-read differently as
// markdown — a block that renders to whitespace-only (a lone "|", a bare "\\"), or
// a literal markdown-significant sequence (```, "---", …) that a markdown parser
// groups across the wiki block boundaries. Those are an inherent property of
// representing wiki as a lossy markdown view, not a merge defect; the merge still
// must not panic and must fail closed (both asserted above).
func blockCompositional(base string) bool {
	var perBlock []string
	for _, b := range scanWikiBlocks(base) {
		ps := splitMDBlocks(wikimd.Render(base[b.start:b.end], wikimd.Options{}))
		if len(ps) == 0 {
			return false // block invisible in the view
		}
		perBlock = append(perBlock, ps...)
	}
	whole := splitMDBlocks(wikimd.Render(base, wikimd.Options{}))
	if len(whole) != len(perBlock) {
		return false
	}
	for i := range whole {
		if whole[i] != perBlock[i] {
			return false
		}
	}
	return true
}
