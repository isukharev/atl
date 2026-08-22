package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	jiraGuardedCreateSchemaVersion                 = 1
	jiraGuardedCreateMaxFields                     = 1000
	jiraGuardedCreateMaxInventoryRows              = 1000
	jiraGuardedCreateMaxStringBytes                = 1024
	jiraGuardedCreateMaxPayloadBytes               = 64 << 20
	jiraGuardedCreateMaxReadbackFields             = 1024
	jiraGuardedCreateMaxReadbackQueryBytes         = 64 << 10
	jiraGuardedCreateMaxResponseBytes        int64 = 16 << 20
	jiraGuardedCreateDeadline                      = 60 * time.Second
	jiraGuardedCreatePreviewRequests               = 11
	jiraGuardedCreatePreviewRegisterRequests       = 12
	jiraGuardedCreateApplyRequests                 = 24
	jiraGuardedCreateApplyRegisterRequests         = 26
)

type JiraGuardedCreateOpts struct {
	Project              string
	IssueType            string
	Summary              string
	Description          []byte
	DescriptionSource    string
	Fields               map[string]domain.JiraFieldInput
	Register             bool
	Into                 string
	Apply                bool
	ExpectedProposalHash string
}

type JiraGuardedCreateDigest struct {
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type JiraGuardedCreateDescription struct {
	Source  string `json:"source"`
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
	Bytes   int    `json:"bytes"`
}

type JiraGuardedCreateProject struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Archived bool   `json:"archived"`
}

type JiraGuardedCreateField struct {
	FieldID         string `json:"field_id"`
	InputKind       string `json:"input_kind"`
	JSONKind        string `json:"normalized_json_kind"`
	NormalizedSHA   string `json:"normalized_sha256"`
	NormalizedBytes int    `json:"normalized_bytes"`
	SchemaSHA256    string `json:"schema_sha256"`
}

type JiraGuardedCreateBounds struct {
	MaxFields             int   `json:"max_fields"`
	MaxInventoryRows      int   `json:"max_inventory_rows"`
	MaxStringBytes        int   `json:"max_string_bytes"`
	MaxPayloadBytes       int   `json:"max_payload_bytes"`
	MaxReadbackFields     int   `json:"max_readback_fields"`
	MaxReadbackQueryBytes int   `json:"max_readback_query_bytes"`
	MaxRequests           int   `json:"max_requests"`
	MaxResponseBytes      int64 `json:"max_response_bytes"`
	DeadlineMillis        int64 `json:"deadline_millis"`
}

type JiraGuardedCreateUsage struct {
	Requests      int   `json:"requests"`
	ResponseBytes int64 `json:"response_bytes"`
}

type JiraGuardedCreateIdentity struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// JiraGuardedCreateRegistrationEffects distinguishes reviewed possible local
// staging from files this invocation actually created. Paths are stable,
// mirror-relative names; the parent coordination lock is deliberately
// represented without disclosing the caller's root path.
type JiraGuardedCreateRegistrationEffects struct {
	PlannedFiles []string `json:"planned_files"`
	ActualFiles  []string `json:"actual_files"`
}

type JiraGuardedCreateResult struct {
	SchemaVersion          int                                      `json:"schema_version"`
	Operation              string                                   `json:"operation"`
	BackendSHA256          string                                   `json:"backend_sha256,omitempty"`
	RequestedProject       string                                   `json:"requested_project"`
	Project                JiraGuardedCreateProject                 `json:"project"`
	TypeSelector           JiraGuardedCreateDigest                  `json:"type_selector"`
	IssueType              domain.JiraIssueType                     `json:"issue_type"`
	Summary                JiraGuardedCreateDigest                  `json:"summary"`
	Description            JiraGuardedCreateDescription             `json:"description"`
	Fields                 []JiraGuardedCreateField                 `json:"fields"`
	MetadataCount          int                                      `json:"metadata_count"`
	MetadataSHA256         string                                   `json:"metadata_sha256,omitempty"`
	RequestSHA256          string                                   `json:"request_sha256,omitempty"`
	RequestBytes           int                                      `json:"request_bytes"`
	RegistrationRequested  bool                                     `json:"registration_requested"`
	RegistrationRootSHA256 string                                   `json:"registration_root_sha256,omitempty"`
	RenderProjectionSHA256 string                                   `json:"render_projection_sha256,omitempty"`
	RegistrationEffects    *JiraGuardedCreateRegistrationEffects    `json:"registration_effects,omitempty"`
	Bounds                 JiraGuardedCreateBounds                  `json:"bounds"`
	ProposalHash           string                                   `json:"proposal_hash,omitempty"`
	Mode                   string                                   `json:"mode"`
	Status                 string                                   `json:"status"`
	WriteAttempted         bool                                     `json:"write_attempted"`
	Acknowledgement        *domain.JiraGuardedCreateAcknowledgement `json:"acknowledgement,omitempty"`
	Issue                  *JiraGuardedCreateIdentity               `json:"issue,omitempty"`
	ReadbackReconciled     bool                                     `json:"readback_reconciled"`
	Registration           *CreatedMirrorRegistration               `json:"registration,omitempty"`
	Usage                  JiraGuardedCreateUsage                   `json:"usage"`
}

type jiraGuardedCreateSnapshot struct {
	result     *JiraGuardedCreateResult
	prepared   domain.JiraGuardedCreatePreparation
	project    domain.JiraProject
	metadata   *domain.JiraQualifiedCreateMetadata
	render     RenderSettings
	readFields []string
}

type jiraGuardedCreateStage struct {
	root         string
	mirror       *mirror.Mirror
	binding      *mirror.BackendBindingPopulationGuard
	registration *CreatedMirrorRegistration
	effects      *JiraGuardedCreateRegistrationEffects
	locks        []interface{ Unlock() error }
}

func (s *jiraGuardedCreateStage) release() {
	if s == nil {
		return
	}
	for index := len(s.locks) - 1; index >= 0; index-- {
		_ = s.locks[index].Unlock()
	}
}

type jiraGuardedCreateError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraGuardedCreateError) Error() string { return e.message }
func (e *jiraGuardedCreateError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}
func (e *jiraGuardedCreateError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }

func guardedCreateFailure(message string, cause error, closed, ambiguous bool) error {
	return &jiraGuardedCreateError{message: message, cause: sanitizeRemoteWriteCause(cause), closed: closed, ambiguous: ambiguous}
}

// GuardedCreate previews or applies one exact Jira issue creation under one deadline and aggregate response budget across both snapshots, the sole POST, and immutable-id readback.
func (s *JiraService) GuardedCreate(ctx context.Context, opts JiraGuardedCreateOpts) (*JiraGuardedCreateResult, error) {
	normalizeGuardedCreateOpts(&opts)
	base := newGuardedCreateResult(opts)
	if err := ValidateJiraGuardedCreateOpts(opts); err != nil {
		return base, err
	}
	port, ok := s.tr.(domain.JiraGuardedCreatePort)
	if !ok {
		return base, guardedCreateFailure("guarded Jira create is unavailable", domain.ErrConfig, true, false)
	}
	maxRequests := guardedCreateMaxRequests(opts)
	base.Bounds.MaxRequests = maxRequests
	budget, err := domain.NewReadBudget(maxRequests, jiraGuardedCreateMaxResponseBytes)
	if err != nil {
		return base, guardedCreateFailure("guarded Jira create budget is invalid", err, true, false)
	}
	workflowCtx, cancel := context.WithTimeout(ctx, jiraGuardedCreateDeadline)
	defer cancel()
	workflowCtx = domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(workflowCtx, budget)))
	deadline, _ := workflowCtx.Deadline()
	defer func() {
		usage := budget.Usage()
		base.Usage = JiraGuardedCreateUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
	}()

	initial, err := s.buildGuardedCreateSnapshot(workflowCtx, port, opts)
	if err != nil {
		base.Status = "blocked"
		return base, guardedCreateFailure("guarded Jira create proposal qualification failed", err, true, false)
	}
	base = initial.result
	base.Bounds.MaxRequests = maxRequests
	if err := workflowCtx.Err(); err != nil {
		base.Status = "blocked"
		return base, guardedCreateFailure("guarded Jira create deadline expired during proposal qualification", err, true, false)
	}
	if !opts.Apply {
		base.Status = "would_apply"
		usage := budget.Usage()
		base.Usage = JiraGuardedCreateUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
		return base, nil
	}
	if opts.ExpectedProposalHash != base.ProposalHash {
		base.Status = "blocked"
		return base, guardedCreateFailure("guarded Jira create proposal changed since review", domain.ErrCheckFailed, true, false)
	}

	var stage *jiraGuardedCreateStage
	if opts.Register {
		stage, err = s.prepareGuardedCreateRegistration(opts.Into)
		if stage != nil {
			base.RegistrationEffects = stage.effects
			base.Registration = stage.registration
			defer stage.release()
		}
		if err != nil {
			base.Status = "blocked"
			return base, guardedCreateFailure("guarded Jira create registration staging failed", err, true, false)
		}
	}
	prewrite, err := s.buildGuardedCreateSnapshot(workflowCtx, port, opts)
	if err != nil || prewrite.result.ProposalHash != base.ProposalHash {
		base.Status = "blocked"
		return base, guardedCreateFailure("guarded Jira create proposal changed immediately before dispatch", errors.Join(err, domain.ErrCheckFailed), true, false)
	}
	if err := workflowCtx.Err(); err != nil {
		base.Status = "blocked"
		return base, guardedCreateFailure("guarded Jira create deadline expired before dispatch", err, true, false)
	}

	base.WriteAttempted = true
	ack, writeErr := port.WriteGuardedCreate(workflowCtx, domain.JiraGuardedCreateWrite{
		Payload: bytes.Clone(prewrite.prepared.Payload), ProjectID: prewrite.project.ID, ProjectKey: prewrite.project.Key,
	})
	if ack.ID != "" {
		copy := ack
		base.Acknowledgement = &copy
	}
	if writeDefinitelyNotAttempted(writeErr) {
		base.WriteAttempted = false
		base.Status = "blocked"
		return base, guardedCreateFailure("guarded Jira create was refused before dispatch", writeErr, true, false)
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		base.Status = "not_applied"
		return base, guardedCreateFailure("Jira definitively rejected the reviewed issue create", writeErr, false, false)
	}
	if ack.ID == "" {
		base.Status = "outcome_unknown"
		return base, guardedCreateFailure("guarded Jira create outcome is unknown because no immutable acknowledgement was available; do not replay automatically", writeErr, true, true)
	}

	closeout, closeCancel := context.WithDeadline(context.WithoutCancel(workflowCtx), deadline)
	defer closeCancel()
	readback, readErr := port.ReadGuardedCreate(closeout, domain.JiraGuardedCreateReadRequest{ID: ack.ID, Fields: prewrite.readFields})
	validationErr := validateGuardedCreateReadback(prewrite, ack, readback)
	deadlineErr := closeout.Err()
	if writeErr != nil || readErr != nil || validationErr != nil || deadlineErr != nil {
		base.Status = "outcome_unknown"
		return base, guardedCreateFailure("guarded Jira create readback did not prove the exact reviewed issue; do not replay automatically", errors.Join(writeErr, readErr, validationErr, deadlineErr), true, true)
	}
	base.ReadbackReconciled = true
	base.Issue = &JiraGuardedCreateIdentity{ID: readback.ID, Key: readback.Key}
	base.Status = "applied"
	if stage == nil {
		usage := budget.Usage()
		base.Usage = JiraGuardedCreateUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
		return base, nil
	}
	registration, finishErr := s.finishGuardedCreateRegistration(closeout, closeCancel, stage, prewrite, readback)
	base.Registration = registration
	if finishErr != nil {
		base.Status = "applied_not_registered"
		return base, guardedCreateFailure("the reviewed Jira issue was applied but mirror registration failed; do not replay create", finishErr, true, false)
	}
	usage := budget.Usage()
	base.Usage = JiraGuardedCreateUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
	return base, nil
}

func normalizeGuardedCreateOpts(opts *JiraGuardedCreateOpts) {
	opts.Project = strings.ToUpper(strings.TrimSpace(opts.Project))
	opts.IssueType = strings.TrimSpace(opts.IssueType)
	opts.DescriptionSource = strings.TrimSpace(opts.DescriptionSource)
	opts.Into = strings.TrimSpace(opts.Into)
	opts.ExpectedProposalHash = strings.TrimSpace(opts.ExpectedProposalHash)
}

func ValidateJiraGuardedCreateOpts(opts JiraGuardedCreateOpts) error {
	if !domain.ValidJiraIssueKey(opts.Project+"-1") || len(opts.Project) > jiraGuardedCreateMaxStringBytes {
		return fmt.Errorf("%w: --project must be a canonical Jira project key", domain.ErrUsage)
	}
	if opts.IssueType == "" || len(opts.IssueType) > jiraGuardedCreateMaxStringBytes || !utf8.ValidString(opts.IssueType) {
		return fmt.Errorf("%w: --type must be non-empty valid UTF-8 within 1024 bytes", domain.ErrUsage)
	}
	if opts.Summary == "" || !utf8.ValidString(opts.Summary) {
		return fmt.Errorf("%w: --summary must be non-empty valid UTF-8", domain.ErrUsage)
	}
	if opts.DescriptionSource != "none" && opts.DescriptionSource != "wiki" && opts.DescriptionSource != "markdown" {
		return fmt.Errorf("%w: Jira create description source is invalid", domain.ErrUsage)
	}
	if len(opts.Fields) > jiraGuardedCreateMaxFields {
		return fmt.Errorf("%w: Jira create accepts at most 1000 supplied fields", domain.ErrUsage)
	}
	for key := range opts.Fields {
		if key == "" || len(key) > jiraGuardedCreateMaxStringBytes || !utf8.ValidString(key) || strings.ContainsAny(key, ",\x00\r\n") {
			return fmt.Errorf("%w: Jira create field ids must be valid UTF-8 within 1024 bytes", domain.ErrUsage)
		}
	}
	if err := rejectReservedCreateFields(opts.Fields); err != nil {
		return err
	}
	if opts.Register != (opts.Into != "") {
		return fmt.Errorf("%w: --register and a non-empty --into must be used together", domain.ErrUsage)
	}
	if !opts.Apply {
		if opts.ExpectedProposalHash != "" {
			return fmt.Errorf("%w: --expected-proposal-hash requires --apply", domain.ErrUsage)
		}
		return nil
	}
	return ValidateJiraDescriptionEditReviewHash(opts.ExpectedProposalHash)
}

func newGuardedCreateResult(opts JiraGuardedCreateOpts) *JiraGuardedCreateResult {
	mode := "preview"
	if opts.Apply {
		mode = "apply"
	}
	result := &JiraGuardedCreateResult{
		SchemaVersion: jiraGuardedCreateSchemaVersion, Operation: "jira_issue_create",
		RequestedProject: opts.Project, RegistrationRequested: opts.Register,
		Mode: mode, Status: "blocked", Fields: []JiraGuardedCreateField{},
		Bounds: JiraGuardedCreateBounds{
			MaxFields: jiraGuardedCreateMaxFields, MaxInventoryRows: jiraGuardedCreateMaxInventoryRows,
			MaxStringBytes: jiraGuardedCreateMaxStringBytes, MaxPayloadBytes: jiraGuardedCreateMaxPayloadBytes,
			MaxReadbackFields: jiraGuardedCreateMaxReadbackFields, MaxReadbackQueryBytes: jiraGuardedCreateMaxReadbackQueryBytes,
			MaxRequests: guardedCreateMaxRequests(opts), MaxResponseBytes: jiraGuardedCreateMaxResponseBytes,
			DeadlineMillis: jiraGuardedCreateDeadline.Milliseconds(),
		},
	}
	if opts.Register {
		result.RegistrationEffects = &JiraGuardedCreateRegistrationEffects{PlannedFiles: guardedCreateRegistrationPlannedFiles(), ActualFiles: []string{}}
	}
	return result
}

func guardedCreateRegistrationPlannedFiles() []string {
	return []string{
		"<mirror-parent>/.atl-jira-create-registration.lock",
		".gitignore",
		".atl/pending/jira/.mirror.lock",
		".atl/backend-bindings.lock",
		".atl/backend-bindings.json",
		".atl/state.lock",
		".atl/state.json",
	}
}

func guardedCreateMaxRequests(opts JiraGuardedCreateOpts) int {
	if opts.Apply && opts.Register {
		return jiraGuardedCreateApplyRegisterRequests
	}
	if opts.Apply {
		return jiraGuardedCreateApplyRequests
	}
	if opts.Register {
		return jiraGuardedCreatePreviewRegisterRequests
	}
	return jiraGuardedCreatePreviewRequests
}
