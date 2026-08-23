package agenteval

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
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

// JiraTriageCommentPreview is the evaluator-owned content-minimized wire of
// the exact guarded dry-run emitted by `atl jira issue comment preview`.
type JiraTriageCommentPreview struct {
	SchemaVersion      int                     `json:"schema_version"`
	Operation          string                  `json:"operation"`
	SatisfactionPolicy string                  `json:"satisfaction_policy"`
	BackendSHA256      string                  `json:"backend_sha256"`
	RequestedKey       string                  `json:"requested_key"`
	IssueID            string                  `json:"issue_id"`
	Key                string                  `json:"key"`
	Project            string                  `json:"project"`
	Updated            string                  `json:"updated"`
	BodySHA256         string                  `json:"body_sha256"`
	BodyBytes          int                     `json:"body_bytes"`
	ActorSHA256        string                  `json:"actor_sha256"`
	CurrentCount       int                     `json:"current_count"`
	BaselineSHA256     string                  `json:"baseline_sha256"`
	ExactBodyCount     int                     `json:"exact_body_count"`
	Bounds             JiraTriageCommentBounds `json:"bounds"`
	Usage              JiraTriageCommentUsage  `json:"usage"`
	Mode               string                  `json:"mode"`
	Status             string                  `json:"status"`
	ProposalHash       string                  `json:"proposal_hash"`
	WriteAttempted     bool                    `json:"write_attempted"`
	Reconciled         bool                    `json:"reconciled"`
	Complete           bool                    `json:"complete"`
}

// JiraTriageCommentApply is the evaluator-owned closed recovered-apply wire
// used by the active triage holdout. It remains separate from the producer
// decoder so an apply result can never satisfy a preview binding.
type JiraTriageCommentApply struct {
	JiraTriageCommentPreview
	ReadbackUpdated string `json:"readback_updated"`
	CommentID       string `json:"comment_id"`
}

type JiraTriageCommentBounds struct {
	MaxKeyBytes               int   `json:"max_key_bytes"`
	MaxBodyBytes              int   `json:"max_body_bytes"`
	MaxEvidenceIDBytes        int   `json:"max_evidence_id_bytes"`
	MaxEvidenceMetadataBytes  int   `json:"max_evidence_metadata_bytes"`
	MaxPages                  int   `json:"max_pages"`
	MaxItems                  int   `json:"max_items"`
	MaxInventoryBytes         int64 `json:"max_inventory_bytes"`
	PreviewMaxRequests        int   `json:"preview_max_requests"`
	ApplyMaxRequests          int   `json:"apply_max_requests"`
	MaxAggregateResponseBytes int64 `json:"max_aggregate_response_bytes"`
	DeadlineMillis            int64 `json:"deadline_millis"`
}

type JiraTriageCommentUsage struct {
	Requests      int   `json:"requests"`
	ResponseBytes int64 `json:"response_bytes"`
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

func DecodeJiraTriageCommentApply(r io.Reader) (JiraTriageCommentApply, error) {
	var result JiraTriageCommentApply
	if err := decodeJiraWorkflowWire(r, jiraTriageCommentPreviewWireMaxBytes, "Jira triage comment apply", &result, validateJiraTriageCommentApplyMembers); err != nil {
		return JiraTriageCommentApply{}, err
	}
	if err := result.validate(); err != nil {
		return JiraTriageCommentApply{}, fmt.Errorf("validate Jira triage comment apply: %w", err)
	}
	return result, nil
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
		"schema_version", "operation", "satisfaction_policy", "backend_sha256", "requested_key",
		"issue_id", "key", "project", "updated", "body_sha256", "body_bytes", "actor_sha256",
		"current_count", "baseline_sha256", "exact_body_count", "bounds", "usage", "mode", "status",
		"proposal_hash", "write_attempted", "reconciled", "complete",
	}, nil); err != nil {
		return err
	}
	bounds, err := jiraWorkflowNestedObject(root["bounds"], "Jira triage comment preview.bounds")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(bounds, "Jira triage comment preview.bounds", []string{
		"max_key_bytes", "max_body_bytes", "max_evidence_id_bytes", "max_evidence_metadata_bytes", "max_pages",
		"max_items", "max_inventory_bytes", "preview_max_requests", "apply_max_requests",
		"max_aggregate_response_bytes", "deadline_millis",
	}, nil); err != nil {
		return err
	}
	usage, err := jiraWorkflowNestedObject(root["usage"], "Jira triage comment preview.usage")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(usage, "Jira triage comment preview.usage", []string{"requests", "response_bytes"}, nil)
}

func validateJiraTriageCommentApplyMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira triage comment apply")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "Jira triage comment apply", []string{
		"schema_version", "operation", "satisfaction_policy", "backend_sha256", "requested_key",
		"issue_id", "key", "project", "updated", "readback_updated", "body_sha256", "body_bytes", "actor_sha256",
		"current_count", "baseline_sha256", "exact_body_count", "bounds", "usage", "mode", "status",
		"proposal_hash", "comment_id", "write_attempted", "reconciled", "complete",
	}, nil); err != nil {
		return err
	}
	bounds, err := jiraWorkflowNestedObject(root["bounds"], "Jira triage comment apply.bounds")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(bounds, "Jira triage comment apply.bounds", []string{
		"max_key_bytes", "max_body_bytes", "max_evidence_id_bytes", "max_evidence_metadata_bytes", "max_pages",
		"max_items", "max_inventory_bytes", "preview_max_requests", "apply_max_requests",
		"max_aggregate_response_bytes", "deadline_millis",
	}, nil); err != nil {
		return err
	}
	usage, err := jiraWorkflowNestedObject(root["usage"], "Jira triage comment apply.usage")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(usage, "Jira triage comment apply.usage", []string{"requests", "response_bytes"}, nil)
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
	if len(issue.Fields) == 0 || len(issue.Fields) > jiraTriageWireMaximumFields {
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
	if preview.SchemaVersion != 1 || preview.Operation != "jira_issue_comment_append" || preview.SatisfactionPolicy != "append_always" ||
		!jiraTriageCanonicalIssueIdentity(preview.RequestedKey, preview.Key, preview.Project, preview.IssueID) ||
		preview.Mode != "dry-run" || preview.Status != "would_apply" || preview.WriteAttempted || preview.Reconciled || !preview.Complete {
		return fmt.Errorf("guarded preview state is invalid")
	}
	if _, err := jiraTriageStrictInstant(preview.Updated); err != nil {
		return fmt.Errorf("preview timestamp is invalid")
	}
	if preview.BodyBytes <= 0 || preview.BodyBytes > jiraTriageWireMaximumBodyBytes || preview.CurrentCount < 0 ||
		preview.ExactBodyCount < 0 || preview.ExactBodyCount > preview.CurrentCount {
		return fmt.Errorf("preview counts are invalid")
	}
	if !jiraTriageTaggedSHA256(preview.BackendSHA256) || !triageWireSHA256(preview.BodySHA256) || !triageWireSHA256(preview.ActorSHA256) ||
		!triageWireSHA256(preview.BaselineSHA256) || !triageWireSHA256(preview.ProposalHash) {
		return fmt.Errorf("preview hash is invalid")
	}
	wantBounds := JiraTriageCommentBounds{
		MaxKeyBytes: 64, MaxBodyBytes: 1 << 20, MaxEvidenceIDBytes: 64, MaxEvidenceMetadataBytes: 64 << 10,
		MaxPages: 100, MaxItems: 10_000, MaxInventoryBytes: 16 << 20, PreviewMaxRequests: 102,
		ApplyMaxRequests: 306, MaxAggregateResponseBytes: 16 << 20, DeadlineMillis: 60_000,
	}
	if preview.Bounds != wantBounds || preview.CurrentCount > preview.Bounds.MaxItems || preview.Usage.Requests < 3 || preview.Usage.Requests > preview.Bounds.PreviewMaxRequests ||
		preview.Usage.ResponseBytes < 0 || preview.Usage.ResponseBytes > preview.Bounds.MaxAggregateResponseBytes {
		return fmt.Errorf("preview bounds or usage are invalid")
	}
	return nil
}

func (result JiraTriageCommentApply) validate() error {
	if result.SchemaVersion != 1 || result.Operation != "jira_issue_comment_append" || result.SatisfactionPolicy != "append_always" ||
		!jiraTriageCanonicalIssueIdentity(result.RequestedKey, result.Key, result.Project, result.IssueID) ||
		!jiraTriagePositiveDecimal(result.CommentID, result.Bounds.MaxEvidenceIDBytes) ||
		result.Mode != "apply" || result.Status != "recovered" || !result.WriteAttempted || !result.Reconciled || !result.Complete {
		return fmt.Errorf("guarded apply state is invalid")
	}
	updated, updatedErr := jiraTriageStrictInstant(result.Updated)
	readbackUpdated, readbackErr := jiraTriageStrictInstant(result.ReadbackUpdated)
	if updatedErr != nil || readbackErr != nil || !readbackUpdated.After(updated) {
		return fmt.Errorf("apply timestamps are invalid")
	}
	if result.BodyBytes <= 0 || result.BodyBytes > jiraTriageWireMaximumBodyBytes || result.CurrentCount < 0 ||
		result.ExactBodyCount < 0 || result.ExactBodyCount > result.CurrentCount {
		return fmt.Errorf("apply counts are invalid")
	}
	if !jiraTriageTaggedSHA256(result.BackendSHA256) || !triageWireSHA256(result.BodySHA256) || !triageWireSHA256(result.ActorSHA256) ||
		!triageWireSHA256(result.BaselineSHA256) || !triageWireSHA256(result.ProposalHash) {
		return fmt.Errorf("apply hash is invalid")
	}
	wantBounds := JiraTriageCommentBounds{
		MaxKeyBytes: 64, MaxBodyBytes: 1 << 20, MaxEvidenceIDBytes: 64, MaxEvidenceMetadataBytes: 64 << 10,
		MaxPages: 100, MaxItems: 10_000, MaxInventoryBytes: 16 << 20, PreviewMaxRequests: 102,
		ApplyMaxRequests: 306, MaxAggregateResponseBytes: 16 << 20, DeadlineMillis: 60_000,
	}
	if result.Bounds != wantBounds || result.CurrentCount > result.Bounds.MaxItems || result.Usage.Requests < 9 || result.Usage.Requests > result.Bounds.ApplyMaxRequests ||
		result.Usage.ResponseBytes < 0 || result.Usage.ResponseBytes > result.Bounds.MaxAggregateResponseBytes {
		return fmt.Errorf("apply bounds or usage are invalid")
	}
	return nil
}

func jiraTriageCanonicalIssueIdentity(requestedKey, key, project, issueID string) bool {
	if requestedKey != key || len(key) > 64 || !jiraTriageCanonicalIssueKey(key) ||
		len(project) > 64 || !jiraTriageCanonicalIssueKey(project+"-1") || !strings.HasPrefix(key, project+"-") {
		return false
	}
	return jiraTriagePositiveDecimal(issueID, 64)
}

func jiraTriageCanonicalIssueKey(value string) bool {
	dash := strings.LastIndexByte(value, '-')
	if dash < 2 || dash > 32 || dash == len(value)-1 || value[0] < 'A' || value[0] > 'Z' || value[dash+1] == '0' {
		return false
	}
	for index := 0; index < dash; index++ {
		char := value[index]
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return jiraTriageDigits(value[dash+1:])
}

func jiraTriagePositiveDecimal(value string, maximum int) bool {
	if value == "" || maximum <= 0 || len(value) > maximum || value[0] == '0' {
		return false
	}
	if !jiraTriageDigits(value) {
		return false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return err == nil && number > 0
}

func jiraTriageTaggedSHA256(value string) bool {
	return strings.HasPrefix(value, "sha256:") && triageWireSHA256(value[len("sha256:"):])
}

func jiraTriageStrictInstant(value string) (time.Time, error) {
	core := ""
	switch {
	case len(value) >= 20 && value[len(value)-1] == 'Z':
		core = value[:len(value)-1]
	case len(value) >= 25 && (value[len(value)-6] == '+' || value[len(value)-6] == '-') && value[len(value)-3] == ':' &&
		jiraTriageDigits(value[len(value)-5:len(value)-3]) && jiraTriageDigits(value[len(value)-2:]):
		core = value[:len(value)-6]
	case len(value) >= 24 && (value[len(value)-5] == '+' || value[len(value)-5] == '-') && jiraTriageDigits(value[len(value)-4:]):
		core = value[:len(value)-5]
	default:
		return time.Time{}, fmt.Errorf("unsupported Jira timestamp")
	}
	if len(core) < 19 || core[4] != '-' || core[7] != '-' || core[10] != 'T' || core[13] != ':' || core[16] != ':' ||
		!jiraTriageDigits(core[0:4]) || !jiraTriageDigits(core[5:7]) || !jiraTriageDigits(core[8:10]) ||
		!jiraTriageDigits(core[11:13]) || !jiraTriageDigits(core[14:16]) || !jiraTriageDigits(core[17:19]) ||
		len(core) > 19 && (core[19] != '.' || len(core) > 29 || !jiraTriageDigits(core[20:])) {
		return time.Time{}, fmt.Errorf("unsupported Jira timestamp")
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported Jira timestamp")
}

func jiraTriageDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func triageWireSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		decimal := value[index] >= '0' && value[index] <= '9'
		lowerHex := value[index] >= 'a' && value[index] <= 'f'
		if !decimal && !lowerHex {
			return false
		}
	}
	return true
}
