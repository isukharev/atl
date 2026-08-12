package agenteval

import profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"

func legacyGradeKind(kind string) bool {
	_, supported := profileatl.LegacyGradingFamily(kind)
	return supported
}
