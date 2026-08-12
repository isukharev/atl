package agenteval

import profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"

// legacyGradeKind centralizes the legacy validation vocabulary. It does not
// replace the historical evaluator or claim generic receipt authority.
func legacyGradeKind(kind string) bool {
	_, supported := profileatl.LegacyGradingFamily(kind)
	return supported
}
