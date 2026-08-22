package agenteval

import (
	"strings"
	"testing"
)

func TestDecodeJiraGuardedCreateResultRegisteredPreviewAndApply(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	preview := `{"schema_version":1,"operation":"jira_issue_create","backend_sha256":"` + hash + `","requested_project":"LAB","project":{"id":"1","key":"LAB","archived":false},"type_selector":{},"issue_type":{"id":"2","name":"Bug","subtask":false},"summary":{},"description":{},"fields":[],"metadata_count":1,"metadata_sha256":"` + hash + `","request_sha256":"` + hash + `","request_bytes":64,"registration_requested":true,"registration_root_sha256":"` + hash + `","render_projection_sha256":"` + hash + `","registration_effects":{"planned_files":[".atl/backend-bindings.json"],"actual_files":[]},"bounds":{},"proposal_hash":"` + hash + `","mode":"preview","status":"would_apply","write_attempted":false,"readback_reconciled":false,"usage":{}}`
	applied := strings.Replace(preview, `"mode":"preview","status":"would_apply","write_attempted":false,"readback_reconciled":false`, `"mode":"apply","status":"applied","write_attempted":true,"acknowledgement":{},"issue":{"id":"10001","key":"LAB-1"},"readback_reconciled":true,"registration":{}`, 1)
	applied = strings.Replace(applied, `"actual_files":[]`, `"actual_files":[".atl/backend-bindings.json"]`, 1)

	for name, wire := range map[string]string{"preview": preview, "apply": applied} {
		result, err := DecodeJiraGuardedCreateResult(strings.NewReader(wire))
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		if result.RegistrationEffects == nil || len(result.RegistrationEffects.PlannedFiles) != 1 {
			t.Fatalf("%s registration effects=%+v", name, result.RegistrationEffects)
		}
	}

	obsolete := strings.Replace(preview, `"registration_effects":{"planned_files":[".atl/backend-bindings.json"],"actual_files":[]}`, `"registration_staging_files":[".atl/backend-bindings.json"]`, 1)
	if _, err := DecodeJiraGuardedCreateResult(strings.NewReader(obsolete)); err == nil {
		t.Fatal("obsolete registration_staging_files member was accepted")
	}
}
