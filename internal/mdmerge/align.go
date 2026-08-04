package mdmerge

import "github.com/isukharev/atl/internal/blockalign"

// lcs keeps merge assembly and its domain-specific refusal local while the
// format-neutral bounded alignment kernel has one owner.
func lcs(a, b []string) (matchA, matchB []int, complete bool) {
	return blockalign.LCS(a, b)
}
