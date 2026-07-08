package wikimerge

// lcs computes a longest-common-subsequence matching between two string slices
// (the base blocks' rendered markdown and the edited markdown blocks). It
// returns, for each side, the matched index on the other side (-1 when
// unmatched); matched pairs are strictly increasing on both sides, so kept
// content preserves document order. Mirrors internal/mdmerge/align.go — sizes
// are description-scale, well within O(n·m) dynamic programming.
func lcs(a, b []string) (matchA, matchB []int) {
	n, m := len(a), len(b)
	matchA = fill(n)
	matchB = fill(m)
	if n == 0 || m == 0 {
		return matchA, matchB
	}
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
			i++
		default:
			j++
		}
	}
	return matchA, matchB
}

func fill(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = -1
	}
	return s
}
