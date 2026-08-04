package wikimerge

import "testing"

func TestLCSRefusesQuadraticAllocationBeyondBudget(t *testing.T) {
	a := make([]string, 1001)
	b := make([]string, 1000)
	for i := range a {
		a[i] = "a"
	}
	for i := range b {
		b[i] = "b"
	}
	matchA, matchB, complete := lcs(a, b)
	if complete {
		t.Fatal("oversized alignment must be refused before matrix allocation")
	}
	if matchA != nil || matchB != nil {
		t.Fatalf("bounded refusal allocated match vectors: %d/%d", len(matchA), len(matchB))
	}
}

func TestLCSKeepsExactBehaviorForSmallInput(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"a", "x", "c"}
	matchA, matchB, complete := lcs(a, b)
	if !complete {
		t.Fatal("small alignment unexpectedly refused")
	}
	if matchA[0] != 0 || matchA[1] != -1 || matchA[2] != 2 || matchB[0] != 0 || matchB[1] != -1 || matchB[2] != 2 {
		t.Fatalf("matches = %v / %v", matchA, matchB)
	}
}
