package blockalign

import (
	"reflect"
	"strconv"
	"testing"
)

func TestLCSExactMatchVectors(t *testing.T) {
	matchA, matchB, complete := LCS(
		[]string{"a", "b", "c"},
		[]string{"a", "x", "c"},
	)
	if !complete {
		t.Fatal("small alignment unexpectedly refused")
	}
	if want := []int{0, -1, 2}; !reflect.DeepEqual(matchA, want) {
		t.Fatalf("first match vector = %v, want %v", matchA, want)
	}
	if want := []int{0, -1, 2}; !reflect.DeepEqual(matchB, want) {
		t.Fatalf("second match vector = %v, want %v", matchB, want)
	}
}

func TestLCSTieAdvancesFirstInput(t *testing.T) {
	matchA, matchB, complete := LCS(
		[]string{"x", "a"},
		[]string{"a", "x"},
	)
	if !complete {
		t.Fatal("tie alignment unexpectedly refused")
	}
	if want := []int{-1, 0}; !reflect.DeepEqual(matchA, want) {
		t.Fatalf("first match vector = %v, want deterministic tie vector %v", matchA, want)
	}
	if want := []int{1, -1}; !reflect.DeepEqual(matchB, want) {
		t.Fatalf("second match vector = %v, want deterministic tie vector %v", matchB, want)
	}
}

func TestLCSUsesExactFullMatrixBudget(t *testing.T) {
	atBudget := make([]string, 999) // (999+1)^2 == maxLCSCells
	if _, _, complete := LCS(atBudget, atBudget); !complete {
		t.Fatal("exact matrix budget unexpectedly refused")
	}

	overBudget := make([]string, 1000) // (1000+1)^2 > maxLCSCells
	matchA, matchB, complete := LCS(overBudget, overBudget)
	if complete || matchA != nil || matchB != nil {
		t.Fatalf("over-budget alignment = %v, %v, %t; want nil, nil, false", matchA, matchB, complete)
	}
}

func TestLCSOneSidedItemBoundary(t *testing.T) {
	atCap := make([]string, maxLCSItems)
	matchA, matchB, complete := LCS(atCap, nil)
	if !complete || len(matchA) != maxLCSItems || matchB == nil || len(matchB) != 0 {
		t.Fatalf("one-sided boundary alignment lengths = %d/%d, complete=%t", len(matchA), len(matchB), complete)
	}
	if matchA[0] != -1 || matchA[len(matchA)-1] != -1 {
		t.Fatalf("one-sided match vector was not filled with -1 at its boundaries")
	}

	overCap := make([]string, maxLCSItems+1)
	matchA, matchB, complete = LCS(overCap, nil)
	if complete || matchA != nil || matchB != nil {
		t.Fatalf("over-cap one-sided alignment = %v, %v, %t; want nil, nil, false", matchA, matchB, complete)
	}

	matchA, matchB, complete = LCS(nil, atCap)
	if !complete || matchA == nil || len(matchA) != 0 || len(matchB) != maxLCSItems {
		t.Fatalf("mirrored one-sided boundary alignment lengths = %d/%d, complete=%t", len(matchA), len(matchB), complete)
	}
	if matchB[0] != -1 || matchB[len(matchB)-1] != -1 {
		t.Fatal("mirrored one-sided match vector was not filled with -1 at its boundaries")
	}

	matchA, matchB, complete = LCS(nil, overCap)
	if complete || matchA != nil || matchB != nil {
		t.Fatalf("mirrored over-cap alignment = %v, %v, %t; want nil, nil, false", matchA, matchB, complete)
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
