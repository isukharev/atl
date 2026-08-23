package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraPlanPreviewRequestCap  = 1024
	jiraPlanApplyRequestCap    = 2048
	jiraPlanPreviewResponseCap = int64(256 << 20)
	jiraPlanApplyResponseCap   = int64(512 << 20)
)

type JiraPlanRunOpts struct {
	Mode                 string
	ExpectedProposalHash string
	AllowOps             []string
	AllowFields          []string
	AllowLinkTypes       []string
	ContinueOnError      bool
}

type JiraPlanDocumentProjection struct {
	NormalizedSHA256 string `json:"normalized_sha256"`
}

type JiraPlanFamilyCounts struct {
	Links    int `json:"link"`
	Label    int `json:"label"`
	Comments int `json:"comment"`
	Field    int `json:"field"`
}

type JiraPlanStatusCounts struct {
	WouldApply       int `json:"would_apply"`
	AlreadySatisfied int `json:"already_satisfied"`
	Applied          int `json:"applied"`
	Recovered        int `json:"recovered"`
	Blocked          int `json:"blocked"`
	NotApplied       int `json:"not_applied"`
	Skipped          int `json:"skipped"`
	OutcomeUnknown   int `json:"outcome_unknown"`
}

type JiraPlanBounds struct {
	MaxDocumentBytes  int64              `json:"max_document_bytes"`
	MaxRows           int                `json:"max_rows"`
	MaxFieldCellBytes int                `json:"max_field_cell_bytes"`
	Formulas          JiraPlanFormulas   `json:"formulas"`
	HardCaps          JiraPlanHardCaps   `json:"hard_caps"`
	Admitted          JiraPlanAdmissions `json:"admitted"`
}

type JiraPlanBudget struct {
	MaxRequests      int   `json:"max_requests"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
}

type JiraPlanFormulas struct {
	PreviewRequests      string `json:"preview_requests"`
	ApplyRequests        string `json:"apply_requests"`
	PreviewResponseBytes string `json:"preview_response_bytes"`
	ApplyResponseBytes   string `json:"apply_response_bytes"`
}

type JiraPlanHardCaps struct {
	PreviewRequests      int   `json:"preview_requests"`
	ApplyRequests        int   `json:"apply_requests"`
	PreviewResponseBytes int64 `json:"preview_response_bytes"`
	ApplyResponseBytes   int64 `json:"apply_response_bytes"`
}

type JiraPlanAdmissions struct {
	PreviewRequests      int   `json:"preview_requests"`
	ApplyRequests        int   `json:"apply_requests"`
	PreviewResponseBytes int64 `json:"preview_response_bytes"`
	ApplyResponseBytes   int64 `json:"apply_response_bytes"`
}

type JiraPlanUsage struct {
	Requests      int   `json:"requests"`
	ResponseBytes int64 `json:"response_bytes"`
}

type JiraPlanSourceRequested struct {
	SourceKey string `json:"source_key"`
}

type JiraPlanLinkRequested struct {
	SourceKey string `json:"source_key"`
	TargetKey string `json:"target_key"`
}

type JiraPlanFieldRequested struct {
	SourceKey string `json:"source_key"`
	FieldID   string `json:"field_id"`
}

type JiraPlanLinkEffect struct {
	Action         string `json:"action"`
	SelectorBytes  int    `json:"selector_bytes"`
	SelectorSHA256 string `json:"selector_sha256"`
	ResolvedTypeID string `json:"resolved_type_id"`
	ResolvedRole   string `json:"resolved_role"`
}

type JiraPlanLabelEffect struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type JiraPlanCommentEffect struct {
	SatisfactionPolicy string `json:"satisfaction_policy"`
	BodyBytes          int    `json:"body_bytes"`
	BodySHA256         string `json:"body_sha256"`
}

type JiraPlanFieldEffect struct {
	Source         string `json:"source"`
	Kind           string `json:"kind"`
	Bytes          int    `json:"bytes"`
	SHA256         string `json:"sha256"`
	PreparedBytes  int    `json:"prepared_bytes"`
	PreparedSHA256 string `json:"prepared_sha256"`
}

type JiraPlanLinkQualified struct {
	SourceID            string `json:"source_id"`
	TargetID            string `json:"target_id"`
	SourceProject       string `json:"source_project"`
	TargetProject       string `json:"target_project"`
	SourceUpdatedSHA256 string `json:"source_updated_sha256"`
}

type JiraPlanSourceQualified struct {
	SourceID      string `json:"source_id"`
	Project       string `json:"project"`
	UpdatedSHA256 string `json:"updated_sha256"`
}

type JiraPlanCommentQualified struct {
	SourceID       string `json:"source_id"`
	Project        string `json:"project"`
	UpdatedSHA256  string `json:"updated_sha256"`
	BaselineCount  int    `json:"baseline_count"`
	BaselineSHA256 string `json:"baseline_sha256"`
	ActorSHA256    string `json:"actor_sha256"`
}

type JiraPlanFieldQualified struct {
	SourceID      string `json:"source_id"`
	Project       string `json:"project"`
	UpdatedSHA256 string `json:"updated_sha256"`
	CatalogCount  int    `json:"catalog_count"`
	CatalogSHA256 string `json:"catalog_sha256"`
}

type JiraPlanResultRow struct {
	Row            int                    `json:"row"`
	Family         string                 `json:"family"`
	Requested      any                    `json:"requested"`
	Effect         any                    `json:"effect"`
	Qualified      any                    `json:"qualified,omitempty"`
	Authorization  *JiraPlanAuthorization `json:"authorization,omitempty"`
	ProposalHash   string                 `json:"proposal_hash,omitempty"`
	Status         string                 `json:"status"`
	Reason         string                 `json:"reason,omitempty"`
	Complete       bool                   `json:"complete"`
	WriteAttempted bool                   `json:"write_attempted"`
	Reconciled     bool                   `json:"reconciled"`
	Usage          JiraPlanUsage          `json:"usage"`
}

type JiraPlanResult struct {
	SchemaVersion int                           `json:"schema_version"`
	Operation     string                        `json:"operation"`
	Mode          string                        `json:"mode"`
	Status        string                        `json:"status"`
	Complete      bool                          `json:"complete"`
	RowCount      int                           `json:"row_count"`
	BackendSHA256 string                        `json:"backend_sha256,omitempty"`
	Document      JiraPlanDocumentProjection    `json:"document"`
	FamilyCounts  JiraPlanFamilyCounts          `json:"family_counts"`
	StatusCounts  JiraPlanStatusCounts          `json:"status_counts"`
	Bounds        JiraPlanBounds                `json:"bounds"`
	ParentBudget  JiraPlanBudget                `json:"parent_budget"`
	Authorization *JiraPlanAuthorizationSummary `json:"authorization,omitempty"`
	ProposalHash  string                        `json:"proposal_hash,omitempty"`
	Usage         JiraPlanUsage                 `json:"usage"`
	Rows          []JiraPlanResultRow           `json:"rows"`
}

type JiraPlanAuthorization struct {
	Verbs   domain.WriteVerbSet `json:"verbs"`
	Targets []JiraPlanTarget    `json:"targets"`
}

type JiraPlanTarget struct {
	Service string `json:"service"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Project string `json:"project"`
}

type JiraPlanAuthorizationSummary struct {
	RequestCount int    `json:"request_count"`
	SHA256       string `json:"sha256"`
}

type jiraPlanPreparedRow struct {
	execution     *jiraGuardedExecution
	link          *jiraGuardedLinkPrepared
	label         *jiraGuardedLabelPrepared
	comment       *jiraGuardedCommentPrepared
	field         *jiraGuardedFieldPrepared
	linkOpts      JiraGuardedLinkOpts
	labelOpts     JiraGuardedLabelOpts
	commentOpts   JiraCommentAddOpts
	fieldOpts     jiraGuardedFieldExecutionOpts
	linkSelectors []jiraPlanResolvedSelector
	sourceKey     string
	targetKey     string
	sourceProject string
	targetProject string
}

type jiraPlanResolvedSelector struct {
	Selector string `json:"selector"`
	TypeID   string `json:"type_id"`
	Role     string `json:"role"`
}

type jiraPlanResolvedRowSelectors struct {
	Row       int                        `json:"row"`
	Selectors []jiraPlanResolvedSelector `json:"selectors"`
}

func (prepared *jiraPlanPreparedRow) close() {
	if prepared != nil && prepared.execution != nil {
		prepared.execution.Close()
	}
}

func (s *JiraService) RunJiraPlan(ctx context.Context, document *JiraPlanDocument, opts JiraPlanRunOpts) (*JiraPlanResult, error) {
	if document == nil {
		return nil, fmt.Errorf("%w: Jira plan document is required", domain.ErrCheckFailed)
	}
	document.mu.Lock()
	if document.command != opts.Mode || document.consumed || len(document.rows) == 0 {
		document.mu.Unlock()
		return nil, fmt.Errorf("%w: Jira plan document lifecycle is invalid", domain.ErrCheckFailed)
	}
	document.consumed = true
	rows := append([]jiraPlanDocumentRow(nil), document.rows...)
	digest := document.normalizedSHA256
	document.mu.Unlock()

	normalized, familyCounts, err := normalizeJiraPlanRunOpts(opts, rows)
	if err != nil {
		return nil, err
	}
	opts = normalized
	requestMax, responseMax, bounds, err := jiraPlanAdmit(opts.Mode, familyCounts)
	if err != nil {
		return nil, err
	}
	parent, err := domain.NewReadBudget(requestMax, responseMax)
	if err != nil {
		return nil, fmt.Errorf("%w: Jira plan parent budget is invalid", domain.ErrCheckFailed)
	}
	result := &JiraPlanResult{
		SchemaVersion: 2, Operation: "jira_issue_plan", Mode: opts.Mode, RowCount: len(rows),
		Document: JiraPlanDocumentProjection{NormalizedSHA256: digest}, FamilyCounts: familyCounts,
		Bounds: bounds, ParentBudget: JiraPlanBudget{MaxRequests: requestMax, MaxResponseBytes: responseMax},
		Rows: make([]JiraPlanResultRow, len(rows)),
	}
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: Jira plan backend identity is invalid", domain.ErrCheckFailed)
	}
	result.BackendSHA256 = backendHash
	prepared := make([]jiraPlanPreparedRow, len(rows))
	defer func() {
		for i := range prepared {
			prepared[i].close()
		}
	}()
	for i, row := range rows {
		result.Rows[i] = jiraPlanBaseRow(row)
		if qualificationErr := s.qualifyJiraPlanRow(ctx, parent, row, opts, &result.Rows[i], &prepared[i]); qualificationErr != nil {
			setJiraPlanRowUsage(&result.Rows[i], prepared[i].execution)
			result.Rows[i].Status, result.Rows[i].Reason, result.Rows[i].Complete = "blocked", "qualification_failed", true
			for j := range rows {
				if j == i {
					continue
				}
				if j > i {
					result.Rows[j] = jiraPlanBaseRow(rows[j])
				}
				result.Rows[j].Status, result.Rows[j].Reason, result.Rows[j].Complete = "skipped", "aggregate_barrier", false
			}
			jiraPlanFinalize(result, parent)
			return result, jiraPlanFailure()
		}
		if jiraPlanRowBackend(&prepared[i]) != result.BackendSHA256 {
			result.Rows[i].Status, result.Rows[i].Reason, result.Rows[i].Complete = "blocked", "qualification_failed", true
			for j := range rows {
				if j == i {
					continue
				}
				if j > i {
					result.Rows[j] = jiraPlanBaseRow(rows[j])
				}
				result.Rows[j].Status, result.Rows[j].Reason, result.Rows[j].Complete = "skipped", "aggregate_barrier", false
			}
			jiraPlanFinalize(result, parent)
			return result, jiraPlanFailure()
		}
	}

	authorization := make([]domain.WriteAuthorizationRequest, len(rows))
	projections := make([]JiraPlanAuthorization, len(rows))
	for i := range rows {
		authorization[i] = jiraPlanCanonicalAuthorization(result.Rows[i], prepared[i])
		projection := jiraPlanAuthorizationProjection(authorization[i])
		result.Rows[i].Authorization = &projection
		projections[i] = projection
	}
	result.Authorization = jiraPlanAuthorizationSummary(projections)
	if preflight, ok := s.writeAuthorizer.(domain.WritePreflightAuthorizer); ok {
		for i, request := range authorization {
			if err := preflight.Preflight(request); err != nil {
				result.Rows[i].Status, result.Rows[i].Reason, result.Rows[i].Complete = "blocked", "policy_denied", true
				for j := range result.Rows {
					if j != i {
						result.Rows[j].Status, result.Rows[j].Reason, result.Rows[j].Complete = "skipped", "aggregate_barrier", false
					}
				}
				jiraPlanFinalize(result, parent)
				return result, jiraPlanFailure()
			}
		}
	}
	proposalHash, err := jiraPlanProposalHash(result, opts, prepared)
	if err != nil {
		return nil, jiraPlanFailure()
	}
	result.ProposalHash = proposalHash
	if opts.Mode == "apply" && opts.ExpectedProposalHash != proposalHash {
		for i := range result.Rows {
			result.Rows[i].Status, result.Rows[i].Reason, result.Rows[i].Complete = "blocked", "proposal_changed", true
		}
		jiraPlanFinalize(result, parent)
		return result, jiraPlanFailure()
	}

	for i := range rows {
		err := s.executeJiraPlanRow(&result.Rows[i], &prepared[i])
		if result.Rows[i].Status == "outcome_unknown" {
			for j := i + 1; j < len(rows); j++ {
				result.Rows[j].Status, result.Rows[j].Reason, result.Rows[j].Complete = "skipped", "ambiguous_outcome", false
			}
			break
		}
		if err != nil && opts.Mode == "apply" && !opts.ContinueOnError {
			for j := i + 1; j < len(rows); j++ {
				result.Rows[j].Status, result.Rows[j].Reason, result.Rows[j].Complete = "skipped", "prior_row_failed", false
			}
			break
		}
	}
	jiraPlanFinalize(result, parent)
	if jiraPlanSuccessfulStatus(result.Status) {
		return result, nil
	}
	return result, jiraPlanFailure()
}

func normalizeJiraPlanRunOpts(opts JiraPlanRunOpts, rows []jiraPlanDocumentRow) (JiraPlanRunOpts, JiraPlanFamilyCounts, error) {
	if opts.Mode != "preview" && opts.Mode != "apply" {
		return opts, JiraPlanFamilyCounts{}, fmt.Errorf("%w: invalid Jira plan mode", domain.ErrUsage)
	}
	if opts.Mode == "preview" && opts.ContinueOnError {
		return opts, JiraPlanFamilyCounts{}, fmt.Errorf("%w: continue-on-error is apply-only", domain.ErrUsage)
	}
	opts.ExpectedProposalHash = strings.TrimSpace(opts.ExpectedProposalHash)
	if opts.Mode == "preview" && opts.ExpectedProposalHash != "" {
		return opts, JiraPlanFamilyCounts{}, fmt.Errorf("%w: reviewed proposal hash is apply-only", domain.ErrUsage)
	}
	if opts.Mode == "apply" && !strictLowerSHA256(opts.ExpectedProposalHash) {
		return opts, JiraPlanFamilyCounts{}, fmt.Errorf("%w: expected proposal hash must be lowercase SHA-256", domain.ErrUsage)
	}
	var err error
	opts.AllowOps, err = jiraPlanNormalizeAllow(opts.AllowOps, map[string]bool{"link": true, "label_add": true, "label_remove": true, "comment": true, "field": true}, "--allow-ops")
	if err != nil {
		return opts, JiraPlanFamilyCounts{}, err
	}
	if len(opts.AllowOps) == 0 {
		opts.AllowOps = []string{"link"}
	}
	opts.AllowFields, err = jiraPlanNormalizeFields(opts.AllowFields)
	if err != nil {
		return opts, JiraPlanFamilyCounts{}, err
	}
	opts.AllowLinkTypes, err = jiraPlanNormalizeLinkSelectors(opts.AllowLinkTypes)
	if err != nil {
		return opts, JiraPlanFamilyCounts{}, err
	}
	opSet, fieldSet := stringBoolSet(opts.AllowOps), stringBoolSet(opts.AllowFields)
	counts := JiraPlanFamilyCounts{}
	for _, row := range rows {
		if !opSet[row.operation] {
			return opts, counts, fmt.Errorf("%w: plan operation is not admitted by --allow-ops", domain.ErrUsage)
		}
		switch row.operation {
		case "link":
			counts.Links++
		case "label_add", "label_remove":
			counts.Label++
		case "comment":
			counts.Comments++
		case "field":
			counts.Field++
			if !fieldSet[row.field] {
				return opts, counts, fmt.Errorf("%w: plan field is not admitted by --allow-fields", domain.ErrUsage)
			}
		}
	}
	if counts.Field == 0 && len(opts.AllowFields) != 0 {
		return opts, counts, fmt.Errorf("%w: --allow-fields requires at least one field row", domain.ErrUsage)
	}
	if counts.Field != 0 && len(opts.AllowFields) == 0 {
		return opts, counts, fmt.Errorf("%w: --allow-fields is required for field rows", domain.ErrUsage)
	}
	if counts.Links == 0 && len(opts.AllowLinkTypes) != 0 {
		return opts, counts, fmt.Errorf("%w: --allow-link-types requires at least one link row", domain.ErrUsage)
	}
	if counts.Links != 0 && len(opts.AllowLinkTypes) == 0 {
		return opts, counts, fmt.Errorf("%w: --allow-link-types is required for link rows", domain.ErrUsage)
	}
	return opts, counts, nil
}

func jiraPlanNormalizeAllow(values []string, known map[string]bool, flag string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || !utf8.ValidString(value) || seen[value] || known != nil && !known[value] {
			return nil, fmt.Errorf("%w: %s contains an invalid or duplicate value", domain.ErrUsage, flag)
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func jiraPlanNormalizeFields(values []string) ([]string, error) {
	out, err := jiraPlanNormalizeAllow(values, nil, "--allow-fields")
	if err != nil {
		return nil, err
	}
	for _, field := range out {
		if !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) {
			return nil, fmt.Errorf("%w: --allow-fields contains an invalid or reserved field", domain.ErrUsage)
		}
	}
	return out, nil
}

func jiraPlanNormalizeLinkSelectors(values []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		folded := strings.ToLower(value)
		if value == "" || !utf8.ValidString(value) || len(value) > jiraGuardedLinkSelectorMaxBytes || seen[folded] {
			return nil, fmt.Errorf("%w: --allow-link-types contains an invalid or duplicate selector", domain.ErrUsage)
		}
		seen[folded] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out, nil
}

func stringBoolSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func jiraPlanAdmit(mode string, counts JiraPlanFamilyCounts) (int, int64, JiraPlanBounds, error) {
	if counts.Links < 0 || counts.Label < 0 || counts.Comments < 0 || counts.Field < 0 ||
		counts.Links > JiraPlanMaxRows || counts.Label > JiraPlanMaxRows || counts.Comments > JiraPlanMaxRows || counts.Field > JiraPlanMaxRows ||
		counts.Links+counts.Label+counts.Comments+counts.Field > JiraPlanMaxRows {
		return 0, 0, JiraPlanBounds{}, fmt.Errorf("%w: Jira plan family counts are invalid", domain.ErrUsage)
	}
	previewRequests := 3*counts.Links + counts.Label + 102*counts.Comments + 2*counts.Field
	applyRequests := 9*counts.Links + 4*counts.Label + 306*counts.Comments + 6*counts.Field
	previewResponses := int64(16<<20)*int64(counts.Links+counts.Label+counts.Comments) + int64(80<<20)*int64(counts.Field)
	applyResponses := int64(16<<20)*int64(counts.Links+counts.Label+counts.Comments) + int64(225<<20)*int64(counts.Field)
	bounds := JiraPlanBounds{
		MaxDocumentBytes: JiraPlanMaxDocumentBytes, MaxRows: JiraPlanMaxRows, MaxFieldCellBytes: JiraPlanMaxFieldCellBytes,
		Formulas: JiraPlanFormulas{PreviewRequests: "3L+1A+102C+2F", ApplyRequests: "9L+4A+306C+6F", PreviewResponseBytes: "16777216*(L+A+C)+83886080*F", ApplyResponseBytes: "16777216*(L+A+C)+235929600*F"},
		HardCaps: JiraPlanHardCaps{PreviewRequests: jiraPlanPreviewRequestCap, ApplyRequests: jiraPlanApplyRequestCap, PreviewResponseBytes: jiraPlanPreviewResponseCap, ApplyResponseBytes: jiraPlanApplyResponseCap},
		Admitted: JiraPlanAdmissions{PreviewRequests: previewRequests, ApplyRequests: applyRequests, PreviewResponseBytes: previewResponses, ApplyResponseBytes: applyResponses},
	}
	requests, responses, requestCap, responseCap := previewRequests, previewResponses, jiraPlanPreviewRequestCap, jiraPlanPreviewResponseCap
	if mode == "apply" {
		requests, responses, requestCap, responseCap = applyRequests, applyResponses, jiraPlanApplyRequestCap, jiraPlanApplyResponseCap
	}
	if requests > requestCap || responses > responseCap {
		return 0, 0, bounds, fmt.Errorf("%w: Jira plan exceeds the command request or response budget", domain.ErrUsage)
	}
	return requests, responses, bounds, nil
}

func jiraPlanBaseRow(row jiraPlanDocumentRow) JiraPlanResultRow {
	family := row.operation
	if strings.HasPrefix(family, "label_") {
		family = "label"
	}
	var requested, effect any
	switch row.operation {
	case "link":
		requested = JiraPlanLinkRequested{SourceKey: row.source, TargetKey: row.target}
		effect = JiraPlanLinkEffect{Action: "add", SelectorBytes: len(row.typeName), SelectorSHA256: sha256Hex([]byte(row.typeName))}
	case "label_add":
		requested = JiraPlanSourceRequested{SourceKey: row.source}
		effect = JiraPlanLabelEffect{Action: "add", Count: 1, Bytes: len(row.value), SHA256: sha256Hex([]byte(row.value))}
	case "label_remove":
		requested = JiraPlanSourceRequested{SourceKey: row.source}
		effect = JiraPlanLabelEffect{Action: "remove", Count: 1, Bytes: len(row.value), SHA256: sha256Hex([]byte(row.value))}
	case "comment":
		requested = JiraPlanSourceRequested{SourceKey: row.source}
		effect = JiraPlanCommentEffect{SatisfactionPolicy: "exact_body_present", BodyBytes: len(row.value), BodySHA256: sha256Hex([]byte(row.value))}
	case "field":
		requested = JiraPlanFieldRequested{SourceKey: row.source, FieldID: row.field}
		effect = JiraPlanFieldEffect{Source: "raw", Kind: jiraPlanJSONKind(row.fieldJSON), Bytes: len(row.value), SHA256: sha256Hex([]byte(row.value))}
	}
	return JiraPlanResultRow{Row: row.row, Family: family, Requested: requested, Effect: effect, Status: "blocked", Usage: JiraPlanUsage{}}
}

func jiraPlanJSONKind(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return ""
}

func jiraPlanCanonicalAuthorization(row JiraPlanResultRow, prepared jiraPlanPreparedRow) domain.WriteAuthorizationRequest {
	verb := domain.WriteVerbUpdate
	if row.Family == "comment" {
		verb = domain.WriteVerbComment
	}
	kind := "issue"
	if row.Family == "link" {
		kind = "link"
	}
	targets := []domain.WriteTarget{{Service: "jira", Kind: kind, Key: prepared.sourceKey, Project: prepared.sourceProject}}
	if row.Family == "link" {
		effect := row.Effect.(JiraPlanLinkEffect)
		qualified := row.Qualified.(JiraPlanLinkQualified)
		targets = []domain.WriteTarget{{Service: "jira", Kind: "link", Key: prepared.sourceKey, Project: prepared.sourceProject}, {Service: "jira", Kind: "link", Key: prepared.targetKey, Project: prepared.targetProject}}
		if effect.ResolvedRole == "inward" || effect.ResolvedRole == "neutral" && guardedDecimalLess(qualified.TargetID, qualified.SourceID) {
			targets[0], targets[1] = targets[1], targets[0]
		}
	}
	return domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{verb}, Targets: targets}
}

func jiraPlanAuthorizationProjection(request domain.WriteAuthorizationRequest) JiraPlanAuthorization {
	projection := JiraPlanAuthorization{Verbs: append(domain.WriteVerbSet(nil), request.Verbs...), Targets: make([]JiraPlanTarget, len(request.Targets))}
	for i, target := range request.Targets {
		projection.Targets[i] = JiraPlanTarget{Service: target.Service, Kind: target.Kind, Key: target.Key, Project: target.Project}
	}
	return projection
}

func jiraPlanAuthorizationSummary(requests []JiraPlanAuthorization) *JiraPlanAuthorizationSummary {
	encoded, _ := json.Marshal(requests)
	return &JiraPlanAuthorizationSummary{RequestCount: len(requests), SHA256: sha256Hex(encoded)}
}

func jiraPlanProposalHash(result *JiraPlanResult, opts JiraPlanRunOpts, prepared []jiraPlanPreparedRow) (string, error) {
	type rowProjection struct {
		Row           int                   `json:"row"`
		Family        string                `json:"family"`
		Requested     any                   `json:"requested"`
		Effect        any                   `json:"effect"`
		Qualified     any                   `json:"qualified"`
		Authorization JiraPlanAuthorization `json:"authorization"`
		ProposalHash  string                `json:"proposal_hash"`
	}
	rows := make([]rowProjection, len(result.Rows))
	for i, row := range result.Rows {
		rows[i] = rowProjection{row.Row, row.Family, row.Requested, row.Effect, row.Qualified, *row.Authorization, row.ProposalHash}
	}
	resolvedSelectors := []jiraPlanResolvedRowSelectors{}
	for i, item := range prepared {
		if len(item.linkSelectors) != 0 {
			resolvedSelectors = append(resolvedSelectors, jiraPlanResolvedRowSelectors{
				Row:       result.Rows[i].Row,
				Selectors: append([]jiraPlanResolvedSelector(nil), item.linkSelectors...),
			})
		}
	}
	payload := struct {
		SchemaVersion         int                            `json:"schema_version"`
		Operation             string                         `json:"operation"`
		Document              JiraPlanDocumentProjection     `json:"document"`
		Rows                  []rowProjection                `json:"rows"`
		AllowOps              []string                       `json:"allow_ops"`
		AllowFields           []string                       `json:"allow_fields"`
		AllowLinkTypes        []string                       `json:"allow_link_types"`
		ResolvedLinkSelectors []jiraPlanResolvedRowSelectors `json:"resolved_link_selectors"`
		FamilyCounts          JiraPlanFamilyCounts           `json:"family_counts"`
		Bounds                JiraPlanBounds                 `json:"bounds"`
	}{2, "jira_issue_plan", result.Document, rows, opts.AllowOps, opts.AllowFields, opts.AllowLinkTypes, resolvedSelectors, result.FamilyCounts, result.Bounds}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return guardedProposalDigest(encoded), nil
}

func jiraPlanFinalize(result *JiraPlanResult, parent *domain.ReadBudget) {
	result.StatusCounts = JiraPlanStatusCounts{}
	allComplete := len(result.Rows) == result.RowCount
	hasUnknown, hasBlocked, hasWould, hasPositive, hasRejected, allSatisfied := false, false, false, false, false, true
	for _, row := range result.Rows {
		switch row.Status {
		case "would_apply":
			result.StatusCounts.WouldApply++
			hasWould = true
			allSatisfied = false
		case "already_satisfied":
			result.StatusCounts.AlreadySatisfied++
		case "applied":
			result.StatusCounts.Applied++
			hasPositive = true
			allSatisfied = false
		case "recovered":
			result.StatusCounts.Recovered++
			hasPositive = true
			allSatisfied = false
		case "blocked":
			result.StatusCounts.Blocked++
			hasBlocked = true
			allSatisfied = false
		case "not_applied":
			result.StatusCounts.NotApplied++
			hasRejected = true
			allSatisfied = false
		case "skipped":
			result.StatusCounts.Skipped++
			allComplete = false
			allSatisfied = false
		case "outcome_unknown":
			result.StatusCounts.OutcomeUnknown++
			hasUnknown = true
			allComplete = false
			allSatisfied = false
		}
		allComplete = allComplete && row.Complete && row.Status != "skipped"
	}
	if hasUnknown {
		result.Status = "outcome_unknown"
	} else if result.Mode == "preview" {
		if hasBlocked {
			result.Status = "blocked"
		} else if hasWould {
			result.Status = "would_apply"
		} else {
			result.Status = "already_satisfied"
		}
	} else if hasPositive {
		if result.StatusCounts.Blocked+result.StatusCounts.NotApplied+result.StatusCounts.Skipped > 0 {
			result.Status = "partially_applied"
		} else {
			result.Status = "applied"
		}
	} else if hasRejected {
		result.Status = "not_applied"
	} else if allSatisfied {
		result.Status = "already_satisfied"
	} else {
		result.Status = "blocked"
	}
	result.Complete = allComplete
	usage := parent.Usage()
	result.Usage = JiraPlanUsage{usage.Attempts, usage.ResponseBytes}
}

func jiraPlanSuccessfulStatus(status string) bool {
	return status == "would_apply" || status == "already_satisfied" || status == "applied"
}
func jiraPlanFailure() error {
	return fmt.Errorf("%w: guarded Jira plan did not complete successfully", domain.ErrCheckFailed)
}
func jiraPlanRowBackend(prepared *jiraPlanPreparedRow) string {
	switch {
	case prepared.link != nil:
		return prepared.link.result.BackendSHA256
	case prepared.label != nil:
		return prepared.label.result.BackendSHA256
	case prepared.comment != nil:
		return prepared.comment.result.BackendSHA256
	case prepared.field != nil:
		return prepared.field.result.BackendSHA256
	}
	return ""
}
func jiraPlanAuthorizationDigest(requests []domain.WriteAuthorizationRequest) string {
	encoded, _ := json.Marshal(requests)
	return sha256Hex(encoded)
}
func jiraPlanUpdatedDigest(value string) string { return sha256Hex([]byte(value)) }
func jiraPlanErrorAmbiguous(err error) bool {
	var diagnostic interface{ DiagnosticAmbiguousWrite() bool }
	return errors.As(err, &diagnostic) && diagnostic.DiagnosticAmbiguousWrite()
}

func JiraPlanResultText(result *JiraPlanResult) string {
	if result == nil {
		return ""
	}
	hash := result.ProposalHash
	if hash == "" {
		hash = "-"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plan\tmode=%s\tstatus=%s\tcomplete=%t\trows=%d\tproposal_hash=%s\trequests=%d\tresponse_bytes=%d", result.Mode, result.Status, result.Complete, result.RowCount, hash, result.Usage.Requests, result.Usage.ResponseBytes)
	for _, row := range result.Rows {
		rowHash := row.ProposalHash
		if rowHash == "" {
			rowHash = "-"
		}
		fmt.Fprintf(&b, "\nrow\trow=%d\tfamily=%s\tstatus=%s\tcomplete=%t\twrite_attempted=%t\treconciled=%t\tproposal_hash=%s", row.Row, row.Family, row.Status, row.Complete, row.WriteAttempted, row.Reconciled, rowHash)
	}
	return b.String()
}
