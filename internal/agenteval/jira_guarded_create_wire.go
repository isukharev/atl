package agenteval

import (
	"encoding/json"
	"fmt"
	"io"
)

// JiraGuardedCreateResult is the evaluator-owned current schema-v1 result used
// by selected-binary create workflows. It intentionally projects only the
// reviewed identity/status evidence those workflows need.
type JiraGuardedCreateResult struct {
	SchemaVersion          int                                   `json:"schema_version"`
	Operation              string                                `json:"operation"`
	BackendSHA256          string                                `json:"backend_sha256,omitempty"`
	RequestedProject       string                                `json:"requested_project"`
	Project                JiraGuardedCreateProject              `json:"project"`
	TypeSelector           json.RawMessage                       `json:"type_selector"`
	IssueType              JiraGuardedCreateIssueType            `json:"issue_type"`
	Summary                json.RawMessage                       `json:"summary"`
	Description            json.RawMessage                       `json:"description"`
	Fields                 json.RawMessage                       `json:"fields"`
	MetadataCount          int                                   `json:"metadata_count"`
	MetadataSHA256         string                                `json:"metadata_sha256,omitempty"`
	RequestSHA256          string                                `json:"request_sha256,omitempty"`
	RequestBytes           int                                   `json:"request_bytes"`
	RegistrationRequested  bool                                  `json:"registration_requested"`
	RegistrationRootSHA256 string                                `json:"registration_root_sha256,omitempty"`
	RenderProjectionSHA256 string                                `json:"render_projection_sha256,omitempty"`
	RegistrationEffects    *JiraGuardedCreateRegistrationEffects `json:"registration_effects,omitempty"`
	Bounds                 json.RawMessage                       `json:"bounds"`
	ProposalHash           string                                `json:"proposal_hash,omitempty"`
	Mode                   string                                `json:"mode"`
	Status                 string                                `json:"status"`
	WriteAttempted         bool                                  `json:"write_attempted"`
	Acknowledgement        json.RawMessage                       `json:"acknowledgement,omitempty"`
	Issue                  *JiraGuardedCreateIdentity            `json:"issue,omitempty"`
	ReadbackReconciled     bool                                  `json:"readback_reconciled"`
	Registration           json.RawMessage                       `json:"registration,omitempty"`
	Usage                  json.RawMessage                       `json:"usage"`
}

type JiraGuardedCreateProject struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Archived bool   `json:"archived"`
}

type JiraGuardedCreateIssueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

type JiraGuardedCreateIdentity struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type JiraGuardedCreateRegistrationEffects struct {
	PlannedFiles []string `json:"planned_files"`
	ActualFiles  []string `json:"actual_files"`
}

func DecodeJiraGuardedCreateResult(r io.Reader) (JiraGuardedCreateResult, error) {
	var result JiraGuardedCreateResult
	if err := decodeJiraWorkflowWire(r, jiraIssueCreateWireMaxBytes, "Jira guarded issue create", &result, validateJiraGuardedCreateMembers); err != nil {
		return JiraGuardedCreateResult{}, err
	}
	if result.SchemaVersion != 1 || result.Operation != "jira_issue_create" ||
		!jiraWorkflowNormalized(result.Project.ID) || !jiraWorkflowNormalized(result.Project.Key) ||
		!jiraWorkflowNormalized(result.IssueType.ID) || !jiraWorkflowNormalized(result.IssueType.Name) ||
		!validSHA256(result.ProposalHash) {
		return JiraGuardedCreateResult{}, fmt.Errorf("validate Jira guarded issue create: invalid qualified proposal")
	}
	switch result.Status {
	case "would_apply":
		if result.Mode != "preview" || result.WriteAttempted || result.ReadbackReconciled || result.Issue != nil {
			return JiraGuardedCreateResult{}, fmt.Errorf("validate Jira guarded issue create: preview status is contradictory")
		}
	case "applied":
		if result.Mode != "apply" || !result.WriteAttempted || !result.ReadbackReconciled || result.Issue == nil ||
			!jiraWorkflowNormalized(result.Issue.ID) || !jiraWorkflowNormalized(result.Issue.Key) {
			return JiraGuardedCreateResult{}, fmt.Errorf("validate Jira guarded issue create: applied status is unproved")
		}
	default:
		return JiraGuardedCreateResult{}, fmt.Errorf("validate Jira guarded issue create: unexpected terminal status")
	}
	return result, nil
}

func validateJiraGuardedCreateMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira guarded issue create")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(root, "Jira guarded issue create", []string{
		"schema_version", "operation", "requested_project", "project", "type_selector", "issue_type",
		"summary", "description", "fields", "metadata_count", "request_bytes", "registration_requested",
		"bounds", "mode", "status", "write_attempted", "readback_reconciled", "usage",
	}, []string{"backend_sha256", "metadata_sha256", "request_sha256", "registration_root_sha256", "render_projection_sha256",
		"registration_effects", "proposal_hash", "acknowledgement", "issue", "registration"})
}
