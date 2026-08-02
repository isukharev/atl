package app

import "testing"

func TestClassifyNativeReconcileBlocksThreeWayRegions(t *testing.T) {
	b := func(kind, hash string) nativeSemanticBlock { return nativeSemanticBlock{kind: kind, hash: hash} }
	base := []nativeSemanticBlock{b("p", "a"), b("p", "anchor"), b("p", "z")}
	ours := []nativeSemanticBlock{b("p", "local"), b("p", "anchor"), b("p", "z")}
	theirs := []nativeSemanticBlock{b("p", "remote"), b("p", "anchor"), b("p", "z")}
	rows, summary, err := classifyNativeReconcileBlocks(base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].State != "diverged" || summary.Diverged != 1 || summary.Unchanged != 2 {
		t.Fatalf("rows=%+v summary=%+v", rows, summary)
	}
}

func TestClassifyNativeReconcileBlocksConvergedInsertion(t *testing.T) {
	inserted := nativeSemanticBlock{kind: "p", hash: "same"}
	rows, _, err := classifyNativeReconcileBlocks(nil, []nativeSemanticBlock{inserted}, []nativeSemanticBlock{inserted})
	if err != nil || len(rows) != 1 || rows[0].State != "unchanged" || !rows[0].Converged {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestClassifyNativeReconcileBlocksCountsAllocatedMatrixEdges(t *testing.T) {
	base := make([]nativeSemanticBlock, 244)
	ours := make([]nativeSemanticBlock, 2049)
	theirs := make([]nativeSemanticBlock, 2049)
	if _, _, err := classifyNativeReconcileBlocks(base, ours, theirs); err == nil {
		t.Fatal("matrix edge rows/columns were omitted from the alignment bound")
	}
}
