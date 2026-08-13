//go:build !linux

package agentskills

import "testing"

func TestAdmitStructureFailsClosedWhenSecureCaptureIsUnsupported(t *testing.T) {
	result, err := AdmitStructure(ImportRequest{
		SkillRoot: "synthetic", Format: FormatAgentSkillsGuideV1, Baseline: BaselineNoSkill,
	})
	requireStructuralFinding(t, result, err, FindingPlatformUnsupported,
		FindingPolicyRefusal, "skill")
}
