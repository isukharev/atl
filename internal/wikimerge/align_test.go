package wikimerge

import (
	"strconv"
	"testing"
)

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

func TestLCSUsesFullMatrixDimensionsAtBudget(t *testing.T) {
	atBudget := make([]string, 999) // (999+1)^2 == maxLCSCells
	if _, _, complete := lcs(atBudget, atBudget); !complete {
		t.Fatal("exact matrix budget unexpectedly refused")
	}
	over := make([]string, 1000) // (1000+1)^2 > maxLCSCells
	if a, b, complete := lcs(over, over); complete || a != nil || b != nil {
		t.Fatal("matrix above exact budget must be refused before allocation")
	}
}

func TestLCSItemCapAppliesWithEmptyPeer(t *testing.T) {
	large := make([]string, maxLCSItems+1)
	if a, b, complete := lcs(large, nil); complete || a != nil || b != nil {
		t.Fatal("one-sided oversized alignment must be refused before match allocation")
	}
}

func TestLCSBudgetCheckRejectsOverflowShapedDimensions(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if withinLCSBudget(maxInt, maxInt) {
		t.Fatalf("maximum int dimensions admitted on %d-bit platform", strconv.IntSize)
	}
	if withinLCSBudget(-1, 1) || withinLCSBudget(1, -1) {
		t.Fatal("negative dimensions admitted")
	}
}
