package wikimerge

import "testing"

func TestSemanticBlocksUsesApplyScannerAndBounds(t *testing.T) {
	body := []byte("h1. Heading\n\nparagraph\n\n{code}\nx\n{code}\n\n|a|b|\n")
	blocks, err := SemanticBlocks(body, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"heading", "paragraph", "code", "table"}
	if len(blocks) != len(want) {
		t.Fatalf("blocks=%+v", blocks)
	}
	for i := range want {
		if blocks[i].Kind != want[i] || len(blocks[i].SHA256) != 64 {
			t.Fatalf("block[%d]=%+v", i, blocks[i])
		}
	}
	if _, err := SemanticBlocks(body, 3); err == nil {
		t.Fatal("over-limit block scan succeeded")
	}
}
