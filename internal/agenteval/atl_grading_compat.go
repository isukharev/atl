package agenteval

import profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"

// legacyGradeKind centralizes the ATL adapter vocabulary. Every admitted kind
// is projected to a generic grading plan before the durable attempt starts.
func legacyGradeKind(kind string) bool {
	_, supported := profileatl.LegacyGradingFamily(kind)
	return supported
}
