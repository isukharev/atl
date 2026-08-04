package mdmerge

import (
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
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

func TestMergeAlignmentBudgetKeepsCheckFailedSentinel(t *testing.T) {
	base := strings.Repeat("<p>base</p>", 1000)
	edited := strings.TrimSuffix(strings.Repeat("edited\n\n", 1000), "\n\n")
	out, report, err := Merge([]byte(base), nil, edited, Options{AllowFragmentLoss: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want ErrCheckFailed", err)
	}
	if got, want := err.Error(), "check failed: Markdown alignment exceeds the bounded safety budget; edit the native .csf directly"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if out != nil || report != nil {
		t.Fatalf("bounded refusal returned output/report: %q / %+v", out, report)
	}
}
