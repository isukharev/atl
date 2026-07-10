package mirror

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

const blocksSample = `<h1>Intro</h1><p>Hello <strong>world</strong>.</p>` +
	`<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">AB-1</ac:parameter></ac:structured-macro>` +
	`<table><tbody><tr><th>K</th></tr><tr><td>v</td></tr></tbody></table>` +
	`<ul><li>one</li><li>two</li></ul>`

func parseBlocks(t *testing.T, raw string, refs []domain.Ref) ([]Block, *csf.Node) {
	t.Helper()
	root, err := csf.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return RenderBlocks(root, refs), root
}

func TestRenderBlocksRangesAndJoin(t *testing.T) {
	blocks, root := parseBlocks(t, blocksSample, nil)
	if len(blocks) != 5 {
		t.Fatalf("blocks = %d, want 5: %+v", len(blocks), blocks)
	}
	wantKinds := []string{"h1", "p", "macro:jira", "table", "list"}
	prevEnd := 0
	for i, b := range blocks {
		if b.Kind != wantKinds[i] {
			t.Errorf("block %d kind = %q, want %q", i, b.Kind, wantKinds[i])
		}
		if b.CSFStart < prevEnd || b.CSFEnd <= b.CSFStart || b.CSFEnd > len(blocksSample) {
			t.Errorf("block %d range [%d,%d) out of order/bounds", i, b.CSFStart, b.CSFEnd)
		}
		if blocksSample[b.CSFStart] != '<' || blocksSample[b.CSFEnd-1] != '>' {
			t.Errorf("block %d range does not cover a whole element: %q", i, blocksSample[b.CSFStart:b.CSFEnd])
		}
		prevEnd = b.CSFEnd
	}

	// Joining the block MDs reproduces the full render (this sample has no
	// whitespace edge cases, so equality is exact).
	var parts []string
	for _, b := range blocks {
		parts = append(parts, b.MD)
	}
	joined := strings.Join(parts, "\n\n") + "\n"
	_, full, _ := RenderMarkdownViewParts(root, nil, MDViewOpts{})
	if joined != full {
		t.Errorf("join mismatch:\n--- joined ---\n%q\n--- full ---\n%q", joined, full)
	}
}

func TestRenderBlocksDescendsLayout(t *testing.T) {
	raw := `<ac:layout><ac:layout-section ac:type="single"><ac:layout-cell>` +
		`<h2>Inside</h2><p>text</p>` +
		`</ac:layout-cell></ac:layout-section></ac:layout>`
	blocks, _ := parseBlocks(t, raw, nil)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (layout containers must not be blocks): %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != "h2" || blocks[1].Kind != "p" {
		t.Errorf("kinds = %q, %q", blocks[0].Kind, blocks[1].Kind)
	}
	if got := raw[blocks[0].CSFStart:blocks[0].CSFEnd]; got != "<h2>Inside</h2>" {
		t.Errorf("layout block span = %q", got)
	}
}

func TestRenderBlocksSkipsEmpty(t *testing.T) {
	raw := `<p> </p><h1>Real</h1><p></p>`
	blocks, _ := parseBlocks(t, raw, nil)
	if len(blocks) != 1 || blocks[0].Kind != "h1" {
		t.Fatalf("blocks = %+v, want only the h1", blocks)
	}
}

func TestRenderBlocksParagraphWrappedMacroKind(t *testing.T) {
	raw := `<p><ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:structured-macro></p>`
	blocks, _ := parseBlocks(t, raw, nil)
	if len(blocks) != 1 || blocks[0].Kind != "macro:code" {
		t.Fatalf("blocks = %+v, want one macro:code block", blocks)
	}
	// The range covers the wrapping <p>, since that is the node the merge
	// would replace.
	if raw[blocks[0].CSFStart] != '<' || !strings.HasPrefix(raw[blocks[0].CSFStart:], "<p>") {
		t.Errorf("range should start at the <p> wrapper, got %q", raw[blocks[0].CSFStart:blocks[0].CSFEnd])
	}
}
