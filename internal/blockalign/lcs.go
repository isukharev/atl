// Package blockalign provides format-neutral, bounded block alignment.
package blockalign

const (
	maxLCSCells = 1_000_000
	maxLCSItems = 100_000
)

// LCS computes a longest-common-subsequence matching between two string
// slices. It returns, for each side, the matched index on the other side (-1
// when unmatched), plus whether exact alignment stayed within the fixed
// dynamic-programming budget. Matched pairs are strictly increasing on both
// sides, so kept content preserves source order.
func LCS(a, b []string) (matchA, matchB []int, complete bool) {
	n, m := len(a), len(b)
	// Refuse before allocating even the linear match vectors. The matrix uses
	// the extra zero row/column, so the exact allocation is (n+1)*(m+1), not
	// n*m. The independent item cap also covers one empty or one tiny side.
	if !withinLCSBudget(n, m) {
		return nil, nil, false
	}
	matchA = fill(n)
	matchB = fill(m)
	if n == 0 || m == 0 {
		return matchA, matchB, true
	}

	// dp[i][j] = LCS length of a[i:], b[j:].
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j] && dp[i][j] == dp[i+1][j+1]+1:
			matchA[i], matchB[j] = j, i
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			// Preserve the established deterministic tie rule: advance on the
			// first input when both remaining subsequences are equally long.
			i++
		default:
			j++
		}
	}
	return matchA, matchB, true
}

// withinLCSBudget checks dimensions without multiplying or overflowing. The
// item checks intentionally precede n+1/m+1 so even maximum-int dimensions are
// rejected before arithmetic or allocation.
func withinLCSBudget(n, m int) bool {
	if n < 0 || m < 0 || n > maxLCSItems || m > maxLCSItems {
		return false
	}
	return n+1 <= maxLCSCells/(m+1)
}

func fill(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = -1
	}
	return s
}
