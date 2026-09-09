package agenteval

import (
	"encoding/json"
	"fmt"
)

// These conservative wire bounds are evaluator-owned and independent of the
// product's tighter metadata byte inventory and checkpoint selection limits.
const (
	mirrorSnapshotCompletePullMaxCheckpoints = 128
	mirrorSnapshotCompletePullMaxSelected    = 128000000
)

// A nil CompletePull in the containing Jira schema-v1 wire means the historical
// binary reported no checkpoint facts. It must not become synthetic empty health.
type jiraMirrorSnapshotCompletePullWire struct {
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	Recovery     string `json:"recovery"`
	Checkpoints  int    `json:"checkpoints"`
	Selected     int    `json:"selected"`
	Completed    int    `json:"completed"`
	Remaining    int    `json:"remaining"`
	Journals     int    `json:"journals"`
	Publications int    `json:"publications"`
	Complete     bool   `json:"complete"`
	Healthy      bool   `json:"healthy"`
}

func validateJiraMirrorSnapshotCompletePullMembers(root map[string]json.RawMessage) error {
	_, err := mirrorSnapshotNested(root, "complete_pull", "Jira mirror snapshot", []string{
		"status", "reason", "recovery", "checkpoints", "selected", "completed", "remaining", "journals", "publications", "complete", "healthy",
	})
	return err
}

func (value jiraMirrorSnapshotCompletePullWire) validate() error {
	for _, count := range []int{value.Checkpoints, value.Journals, value.Publications} {
		if count < 0 || count > mirrorSnapshotCompletePullMaxCheckpoints {
			return fmt.Errorf("checkpoint count is outside 0..%d", mirrorSnapshotCompletePullMaxCheckpoints)
		}
	}
	for _, count := range []int{value.Selected, value.Completed, value.Remaining} {
		if count < 0 || count > mirrorSnapshotCompletePullMaxSelected {
			return fmt.Errorf("selection count is outside 0..%d", mirrorSnapshotCompletePullMaxSelected)
		}
	}
	if value.Selected != value.Completed+value.Remaining || value.Journals > value.Checkpoints || value.Publications > value.Checkpoints {
		return fmt.Errorf("counts are not reconciled")
	}
	wantComplete, wantHealthy := false, false
	wantRecovery := "preserve_for_inspection"
	switch value.Status {
	case "empty":
		wantComplete, wantHealthy, wantRecovery = true, true, "none"
		if value.Checkpoints != 0 || value.Selected != 0 || value.Journals != 0 || value.Publications != 0 {
			return fmt.Errorf("empty status has nonzero counts")
		}
	case "resumable":
		wantComplete, wantHealthy, wantRecovery = true, true, "rerun_original_command"
		if value.Checkpoints == 0 || value.Journals != 0 || value.Publications != 0 {
			return fmt.Errorf("resumable status contradicts checkpoint counts")
		}
	case "recovery_pending":
		wantComplete = true
		if value.Checkpoints == 0 || value.Journals+value.Publications == 0 {
			return fmt.Errorf("recovery_pending status has no pending recovery")
		}
	case "invalid":
		if value.Reason != "malformed" && value.Reason != "unsupported_schema" && value.Reason != "orphaned" && value.Reason != "unreadable" {
			return fmt.Errorf("invalid status has an unsupported reason")
		}
	case "inventory_limit":
		if value.Reason != "entry_limit" && value.Reason != "byte_limit" {
			return fmt.Errorf("inventory_limit status has an unsupported reason")
		}
	default:
		return fmt.Errorf("unsupported status")
	}
	if wantComplete && value.Reason != "" {
		return fmt.Errorf("complete inventory has a nonempty reason")
	}
	if value.Complete != wantComplete || value.Healthy != wantHealthy || value.Recovery != wantRecovery {
		return fmt.Errorf("complete, healthy, or recovery contradicts status")
	}
	return nil
}
