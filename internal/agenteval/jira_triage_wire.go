package agenteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	jiraTriageIssueGetWireMaxBytes       = 512 << 10
	jiraTriageCommentPreviewWireMaxBytes = maxContractBytes
	jiraTriageWireMaximumFields          = 128
	jiraTriageWireMaximumCollectionItems = 1000
	jiraTriageWireMaximumBodyBytes       = 1 << 20
)

// JiraTriageIssueGet is the deliberately small evaluator-owned projection of
// the released default `atl jira issue get` JSON used by the triage workflow.
// Fields remains raw because Jira field values are backend-defined; the five
// fields that qualify the workflow are reconciled against the top-level view.
type JiraTriageIssueGet struct {
	ID          string                     `json:"id,omitempty"`
	Key         string                     `json:"key"`
	Summary     string                     `json:"summary"`
	Status      string                     `json:"status"`
	StatusID    string                     `json:"status_id,omitempty"`
	Type        string                     `json:"type"`
	Project     string                     `json:"project"`
	Assignee    string                     `json:"assignee,omitempty"`
	Reporter    string                     `json:"reporter,omitempty"`
	Description string                     `json:"description"`
	Fields      map[string]json.RawMessage `json:"fields"`
	Labels      []string                   `json:"labels,omitempty"`
	Links       []JiraTriageIssueLink      `json:"links,omitempty"`
	Comments    []JiraTriageIssueComment   `json:"comments,omitempty"`
}

// JiraTriageIssueLink and JiraTriageIssueComment close the two structured
// optional collections in the released issue-get projection. Arbitrary Jira
// custom fields remain open only under Fields.
type JiraTriageIssueLink struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	TypeName  string `json:"type_name,omitempty"`
	Direction string `json:"direction"`
	Key       string `json:"key"`
}

type JiraTriageIssueComment struct {
	ID          string `json:"id"`
	Author      string `json:"author"`
	Created     string `json:"created"`
	Body        string `json:"body"`
	BodyStorage string `json:"body_storage,omitempty"`
}

// JiraTriageCommentPreview is the evaluator-owned wire of the exact guarded
// dry-run emitted by `atl jira issue comment add` without --apply. Proposal
// hashes are opaque commitments: their shape is validated, not reproduced.
type JiraTriageCommentPreview struct {
	Key            string                 `json:"key"`
	Mode           string                 `json:"mode"`
	Status         string                 `json:"status"`
	Body           string                 `json:"body"`
	BodySHA256     string                 `json:"body_sha256"`
	BodyBytes      int                    `json:"body_bytes"`
	Actor          JiraTriageCommentActor `json:"actor"`
	CurrentCount   int                    `json:"current_count"`
	BaselineSHA256 string                 `json:"baseline_sha256"`
	ProposalHash   string                 `json:"proposal_hash"`
	Complete       bool                   `json:"complete"`
}

// JiraTriageCommentActor carries only the stable identity facts returned by
// the guarded preview. It intentionally does not model display or contact
// data from the transport response.
type JiraTriageCommentActor struct {
	Name string `json:"name"`
	Key  string `json:"key,omitempty"`
}

// DecodeJiraTriageIssueGet strictly decodes the selected triage issue-read
// result without crossing into product app or domain types.
func DecodeJiraTriageIssueGet(r io.Reader) (JiraTriageIssueGet, error) {
	var issue JiraTriageIssueGet
	if err := decodeJiraWorkflowWire(r, jiraTriageIssueGetWireMaxBytes, "Jira triage issue get", &issue, validateJiraTriageIssueGetMembers); err != nil {
		return JiraTriageIssueGet{}, err
	}
	if err := issue.validate(); err != nil {
		return JiraTriageIssueGet{}, fmt.Errorf("validate Jira triage issue get: %w", err)
	}
	return issue, nil
}

// DecodeJiraTriageCommentPreview strictly decodes the selected guarded
// comment dry-run result. It is intentionally not an apply-result decoder.
func DecodeJiraTriageCommentPreview(r io.Reader) (JiraTriageCommentPreview, error) {
	var preview JiraTriageCommentPreview
	if err := decodeJiraWorkflowWire(r, jiraTriageCommentPreviewWireMaxBytes, "Jira triage comment preview", &preview, validateJiraTriageCommentPreviewMembers); err != nil {
		return JiraTriageCommentPreview{}, err
	}
	if err := preview.validate(); err != nil {
		return JiraTriageCommentPreview{}, fmt.Errorf("validate Jira triage comment preview: %w", err)
	}
	return preview, nil
}

func validateJiraTriageIssueGetMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira triage issue get")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "Jira triage issue get", []string{
		"key", "summary", "status", "type", "project", "description", "fields",
	}, []string{"id", "status_id", "assignee", "reporter", "labels", "links", "comments"}); err != nil {
		return err
	}
	if _, err = jiraWorkflowNestedObject(root["fields"], "Jira triage issue get.fields"); err != nil {
		return err
	}
	if raw, ok := root["links"]; ok {
		if err := jiraWorkflowArray(raw, "Jira triage issue get.links", func(link map[string]json.RawMessage, owner string) error {
			return jiraWorkflowMembers(link, owner, []string{"type", "direction", "key"}, []string{"id", "type_name"})
		}); err != nil {
			return err
		}
	}
	if raw, ok := root["comments"]; ok {
		if err := jiraWorkflowArray(raw, "Jira triage issue get.comments", func(comment map[string]json.RawMessage, owner string) error {
			return jiraWorkflowMembers(comment, owner, []string{"id", "author", "created", "body"}, []string{"body_storage"})
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateJiraTriageCommentPreviewMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira triage comment preview")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "Jira triage comment preview", []string{
		"key", "mode", "status", "body", "body_sha256", "body_bytes", "actor",
		"current_count", "baseline_sha256", "proposal_hash", "complete",
	}, nil); err != nil {
		return err
	}
	actor, err := jiraWorkflowNestedObject(root["actor"], "Jira triage comment preview.actor")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(actor, "Jira triage comment preview.actor", []string{"name"}, []string{"key"})
}

func (issue JiraTriageIssueGet) validate() error {
	for name, value := range map[string]string{
		"key": issue.Key, "summary": issue.Summary, "status": issue.Status,
		"type": issue.Type, "project": issue.Project,
	} {
		if !jiraWorkflowNormalized(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	for name, value := range map[string]string{
		"id": issue.ID, "status_id": issue.StatusID, "assignee": issue.Assignee, "reporter": issue.Reporter,
	} {
		if value != "" && !jiraWorkflowNormalized(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if strings.TrimSpace(issue.Description) == "" || len(issue.Description) > jiraTriageWireMaximumBodyBytes || !utf8.ValidString(issue.Description) {
		return fmt.Errorf("description is invalid")
	}
	if issue.Fields == nil || len(issue.Fields) == 0 || len(issue.Fields) > jiraTriageWireMaximumFields {
		return fmt.Errorf("fields are invalid")
	}
	for name, expected := range map[string]string{
		"summary": issue.Summary, "description": issue.Description,
	} {
		actual, err := jiraTriageWireStringField(issue.Fields, name)
		if err != nil || actual != expected {
			return fmt.Errorf("fields.%s is not reconciled", name)
		}
	}
	for _, check := range []struct {
		field  string
		member string
		want   string
	}{
		{field: "status", member: "name", want: issue.Status},
		{field: "issuetype", member: "name", want: issue.Type},
		{field: "project", member: "key", want: issue.Project},
	} {
		actual, err := jiraTriageWireObjectStringMember(issue.Fields, check.field, check.member)
		if err != nil || actual != check.want {
			return fmt.Errorf("fields.%s.%s is not reconciled", check.field, check.member)
		}
	}
	fieldStatusID, present, err := jiraTriageWireOptionalObjectStringMember(issue.Fields, "status", "id")
	if err != nil || present != (issue.StatusID != "") || present && fieldStatusID != issue.StatusID {
		return fmt.Errorf("fields.status.id is not reconciled")
	}
	for index, label := range issue.Labels {
		if !jiraWorkflowNormalized(label) {
			return fmt.Errorf("labels[%d] is invalid", index)
		}
	}
	if len(issue.Labels) > jiraTriageWireMaximumCollectionItems || len(issue.Links) > jiraTriageWireMaximumCollectionItems || len(issue.Comments) > jiraTriageWireMaximumCollectionItems {
		return fmt.Errorf("optional collection exceeds its bound")
	}
	for index, link := range issue.Links {
		if !jiraWorkflowNormalized(link.Type) || !jiraWorkflowNormalized(link.Key) ||
			link.Direction != "inward" && link.Direction != "outward" ||
			link.ID != "" && !jiraWorkflowNormalized(link.ID) || link.TypeName != "" && !jiraWorkflowNormalized(link.TypeName) {
			return fmt.Errorf("links[%d] is invalid", index)
		}
	}
	for index, comment := range issue.Comments {
		if !jiraWorkflowNormalized(comment.ID) || !jiraWorkflowNormalized(comment.Author) || !jiraWorkflowNormalized(comment.Created) ||
			len(comment.Body) > jiraTriageWireMaximumBodyBytes || !utf8.ValidString(comment.Body) ||
			len(comment.BodyStorage) > jiraTriageWireMaximumBodyBytes || !utf8.ValidString(comment.BodyStorage) {
			return fmt.Errorf("comments[%d] is invalid", index)
		}
	}
	return nil
}

func jiraTriageWireStringField(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok || jiraWorkflowNull(raw) {
		return "", fmt.Errorf("field is missing")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !utf8.ValidString(value) {
		return "", fmt.Errorf("field is not a string")
	}
	return value, nil
}

func jiraTriageWireObjectStringMember(fields map[string]json.RawMessage, field, member string) (string, error) {
	raw, ok := fields[field]
	if !ok || jiraWorkflowNull(raw) {
		return "", fmt.Errorf("field is missing")
	}
	object, err := jiraWorkflowNestedObject(raw, "Jira triage issue get.fields."+field)
	if err != nil {
		return "", err
	}
	value, ok := object[member]
	if !ok || jiraWorkflowNull(value) {
		return "", fmt.Errorf("member is missing")
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil || !jiraWorkflowNormalized(decoded) {
		return "", fmt.Errorf("member is invalid")
	}
	return decoded, nil
}

func jiraTriageWireOptionalObjectStringMember(fields map[string]json.RawMessage, field, member string) (string, bool, error) {
	raw, ok := fields[field]
	if !ok || jiraWorkflowNull(raw) {
		return "", false, fmt.Errorf("field is missing")
	}
	object, err := jiraWorkflowNestedObject(raw, "Jira triage issue get.fields."+field)
	if err != nil {
		return "", false, err
	}
	value, ok := object[member]
	if !ok {
		return "", false, nil
	}
	if jiraWorkflowNull(value) {
		return "", false, fmt.Errorf("member is null")
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil || !jiraWorkflowNormalized(decoded) {
		return "", false, fmt.Errorf("member is invalid")
	}
	return decoded, true, nil
}

func (preview JiraTriageCommentPreview) validate() error {
	if !jiraWorkflowNormalized(preview.Key) || preview.Mode != "dry-run" || preview.Status != "would_apply" || !preview.Complete {
		return fmt.Errorf("guarded preview state is invalid")
	}
	if strings.TrimSpace(preview.Body) == "" || len(preview.Body) > jiraTriageWireMaximumBodyBytes || !utf8.ValidString(preview.Body) || preview.BodyBytes != len(preview.Body) {
		return fmt.Errorf("preview body is invalid")
	}
	if !triageWireSHA256(preview.BodySHA256) || !triageWireSHA256(preview.BaselineSHA256) || !triageWireSHA256(preview.ProposalHash) {
		return fmt.Errorf("preview hash is invalid")
	}
	bodySum := sha256.Sum256([]byte(preview.Body))
	if preview.BodySHA256 != hex.EncodeToString(bodySum[:]) {
		return fmt.Errorf("preview body hash is not reconciled")
	}
	if !jiraWorkflowNormalized(preview.Actor.Name) || preview.Actor.Key != "" && !jiraWorkflowNormalized(preview.Actor.Key) ||
		preview.CurrentCount < 0 || preview.CurrentCount > jiraTriageWireMaximumCollectionItems {
		return fmt.Errorf("preview actor or baseline is invalid")
	}
	return nil
}

func triageWireSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if !((value[index] >= '0' && value[index] <= '9') || (value[index] >= 'a' && value[index] <= 'f')) {
			return false
		}
	}
	return true
}
