package agentskills

import (
	"strings"
	"testing"
)

func FuzzStructuralFindingLocationIsContentMinimized(f *testing.F) {
	for _, seed := range []string{
		".", "SKILL.md", "nested/file.txt", "../escape", "/absolute", `C:\\host`,
		"contains\\separator", string([]byte{'b', 'a', 'd', 0}), strings.Repeat("x", MaxPathBytes+1),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, untrusted string) {
		result := refuseStructuralAdmission(newStructuralAdmission(), FindingInvalidLocation, "skill", untrusted)
		if len(result.Findings) != 1 {
			t.Fatalf("finding count = %d", len(result.Findings))
		}
		location := result.Findings[0].Location
		if location != "skill" {
			relative := strings.TrimPrefix(location, "skill/")
			if relative == location || !validSourcePath(relative) {
				t.Fatalf("unsafe finding location %q from %q", location, untrusted)
			}
		}
		if !result.BlocksExecution() || result.RuntimeSafetyProven || result.TreeSHA256 != "" || len(result.Entries) != 0 {
			t.Fatalf("invalid refusal projection: %#v", result)
		}
	})
}
