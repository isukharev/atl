package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const planApplyConfirm = "APPLY"
const jiraPlanSchemaVersion = 1

// JiraPlanApplyOpts controls guarded plan execution.
type JiraPlanApplyOpts struct {
	CSVPath         string
	Apply           bool
	Confirm         string
	AllowOps        []string
	AllowFields     []string
	AllowLinkTypes  []string
	ContinueOnError bool
}

// JiraPlanApplyResult is an auditable dry-run/apply report.
type JiraPlanApplyResult struct {
	Version int                      `json:"version"`
	Path    string                   `json:"path,omitempty"`
	Mode    string                   `json:"mode"`
	Count   int                      `json:"count"`
	Results []JiraPlanApplyResultRow `json:"results"`
}

// JiraPlanApplyResultRow is one operation result.
type JiraPlanApplyResultRow struct {
	Row             int    `json:"row"`
	Op              string `json:"op"`
	Source          string `json:"source"`
	Target          string `json:"target,omitempty"`
	Type            string `json:"type,omitempty"`
	Field           string `json:"field,omitempty"`
	Value           string `json:"value,omitempty"`
	Rationale       string `json:"rationale,omitempty"`
	ExpectedUpdated string `json:"expected_updated"`
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
}

type jiraPlanOp struct {
	Version         int
	Row             int
	Op              string
	Source          string
	Target          string
	Type            string
	Field           string
	Value           string
	Rationale       string
	ExpectedUpdated string
}

// ApplyPlan executes or previews a guarded Jira operation plan.
func (s *JiraService) ApplyPlan(ctx context.Context, opts JiraPlanApplyOpts) (*JiraPlanApplyResult, error) {
	path := strings.TrimSpace(opts.CSVPath)
	if path == "" {
		return nil, fmt.Errorf("%w: --csv is required", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.Confirm) != planApplyConfirm {
		return nil, fmt.Errorf("%w: pass --confirm %s with --apply", domain.ErrUsage, planApplyConfirm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ops, err := parseJiraPlanCSV(data)
	if err != nil {
		return nil, err
	}
	allowedOps := allowSet(opts.AllowOps)
	if len(allowedOps) == 0 {
		allowedOps["link"] = true
	}
	allowedFields := exactAllowSet(opts.AllowFields)
	policyBlocked, err := s.planPolicyBlocks(ctx, ops, allowedOps, allowedFields, opts.AllowLinkTypes)
	if err != nil {
		return nil, err
	}
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	result := &JiraPlanApplyResult{Version: jiraPlanSchemaVersion, Path: path, Mode: mode}
	if len(policyBlocked) > 0 && !opts.ContinueOnError {
		for _, op := range ops {
			row := planRow(op)
			if message := policyBlocked[op.Row]; message != "" {
				row.Status, row.Message = "blocked", message
			} else {
				row.Status, row.Message = "skipped", "plan policy validation failed"
			}
			result.Results = append(result.Results, row)
		}
		result.Count = len(result.Results)
		return result, planResultError(result)
	}
	failed := false
	for i, op := range ops {
		var row JiraPlanApplyResultRow
		if message := policyBlocked[op.Row]; message != "" {
			row = planRow(op)
			row.Status, row.Message = "blocked", message
		} else {
			row = s.applyPlanOp(ctx, op, opts.Apply)
		}
		result.Results = append(result.Results, row)
		if planRowFailed(row) {
			failed = true
			if !opts.ContinueOnError {
				for _, rest := range ops[i+1:] {
					skipped := planRow(rest)
					skipped.Status, skipped.Message = "skipped", "stopped after previous row failure"
					result.Results = append(result.Results, skipped)
				}
				break
			}
		}
	}
	result.Count = len(result.Results)
	sort.Slice(result.Results, func(i, j int) bool { return result.Results[i].Row < result.Results[j].Row })
	if failed {
		return result, planResultError(result)
	}
	return result, nil
}

func planRow(op jiraPlanOp) JiraPlanApplyResultRow {
	return JiraPlanApplyResultRow{
		Row:             op.Row,
		Op:              op.Op,
		Source:          op.Source,
		Target:          op.Target,
		Type:            op.Type,
		Field:           op.Field,
		Value:           op.Value,
		Rationale:       op.Rationale,
		ExpectedUpdated: op.ExpectedUpdated,
	}
}

func (s *JiraService) applyPlanOp(ctx context.Context, op jiraPlanOp, apply bool) JiraPlanApplyResultRow {
	row := planRow(op)
	switch op.Op {
	case "link":
		return s.applyPlanLink(ctx, row, apply)
	case "label_add", "label_remove":
		return s.applyPlanLabel(ctx, row, apply)
	case "comment":
		return s.applyPlanComment(ctx, row, apply)
	case "field":
		return s.applyPlanField(ctx, row, apply)
	default:
		row.Status = "blocked"
		row.Message = "unsupported operation"
		return row
	}
}

func (s *JiraService) applyPlanLink(ctx context.Context, row JiraPlanApplyResultRow, apply bool) JiraPlanApplyResultRow {
	if _, err := s.tr.GetIssue(ctx, row.Target, []string{"updated"}); err != nil {
		return failedPlanRow(row, fmt.Errorf("validate link target %s: %w", row.Target, err))
	}
	issue, err := s.tr.GetIssue(ctx, row.Source, []string{"issuelinks", "updated"})
	if err != nil {
		return failedPlanRow(row, err)
	}
	if existingOutwardLinks(issue.Links)[linkIdentity(row.Target, row.Type)] {
		row.Status = "already_satisfied"
		return row
	}
	if stale := stalePlanRow(row, issue); stale.Status != "" {
		return stale
	}
	if !apply {
		row.Status = "would_apply"
		return row
	}
	if err := s.Link(ctx, row.Source, row.Target, row.Type); err != nil {
		return failedPlanRow(row, err)
	}
	row.Status = "applied"
	return row
}

func (s *JiraService) applyPlanLabel(ctx context.Context, row JiraPlanApplyResultRow, apply bool) JiraPlanApplyResultRow {
	issue, err := s.tr.GetIssue(ctx, row.Source, []string{"labels", "updated"})
	if err != nil {
		return failedPlanRow(row, err)
	}
	labels := stringSet(issue.Labels)
	values := splitCSVValue(row.Value)
	if len(values) == 0 {
		row.Status = "blocked"
		row.Message = "value must include at least one label"
		return row
	}
	var add, remove []string
	switch row.Op {
	case "label_add":
		for _, label := range values {
			if !labels[label] {
				add = append(add, label)
			}
		}
	case "label_remove":
		for _, label := range values {
			if labels[label] {
				remove = append(remove, label)
			}
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		row.Status = "already_satisfied"
		return row
	}
	if stale := stalePlanRow(row, issue); stale.Status != "" {
		return stale
	}
	if !apply {
		row.Status = "would_apply"
		return row
	}
	if err := s.UpdateLabels(ctx, row.Source, add, remove); err != nil {
		return failedPlanRow(row, err)
	}
	row.Status = "applied"
	return row
}

func (s *JiraService) applyPlanComment(ctx context.Context, row JiraPlanApplyResultRow, apply bool) JiraPlanApplyResultRow {
	if strings.TrimSpace(row.Value) == "" {
		row.Status = "blocked"
		row.Message = "value must include comment body"
		return row
	}
	comments, err := s.Comments(ctx, row.Source)
	if err != nil {
		return failedPlanRow(row, err)
	}
	for _, comment := range comments {
		if comment.Body == row.Value {
			row.Status = "already_satisfied"
			return row
		}
	}
	issue, err := s.tr.GetIssue(ctx, row.Source, []string{"updated"})
	if err != nil {
		return failedPlanRow(row, err)
	}
	if stale := stalePlanRow(row, issue); stale.Status != "" {
		return stale
	}
	if !apply {
		row.Status = "would_apply"
		return row
	}
	if _, err := s.Comment(ctx, row.Source, []byte(row.Value)); err != nil {
		return failedPlanRow(row, err)
	}
	row.Status = "applied"
	return row
}

func (s *JiraService) applyPlanField(ctx context.Context, row JiraPlanApplyResultRow, apply bool) JiraPlanApplyResultRow {
	issue, err := s.tr.GetIssue(ctx, row.Source, []string{row.Field, "updated"})
	if err != nil {
		return failedPlanRow(row, err)
	}
	if planFieldEqual(issue.Fields[row.Field], row.Value) {
		row.Status = "already_satisfied"
		return row
	}
	if stale := stalePlanRow(row, issue); stale.Status != "" {
		return stale
	}
	if !apply {
		row.Status = "would_apply"
		return row
	}
	if err := s.Update(ctx, row.Source, "", nil, map[string]string{row.Field: row.Value}); err != nil {
		return failedPlanRow(row, err)
	}
	row.Status = "applied"
	return row
}

func failedPlanRow(row JiraPlanApplyResultRow, err error) JiraPlanApplyResultRow {
	row.Status = "failed"
	row.Message = "operation preflight/write failed: " + failReason(err)
	return row
}

func stalePlanRow(row JiraPlanApplyResultRow, issue *domain.Issue) JiraPlanApplyResultRow {
	actual := ""
	if issue != nil {
		actual = fieldString(issue.Fields["updated"])
	}
	if actual == row.ExpectedUpdated {
		return JiraPlanApplyResultRow{}
	}
	row.Status = "blocked"
	row.Message = fmt.Sprintf("stale issue: expected updated %q, got %q", row.ExpectedUpdated, actual)
	return row
}

func planRowFailed(row JiraPlanApplyResultRow) bool {
	return row.Status == "blocked" || row.Status == "failed"
}

func planResultError(result *JiraPlanApplyResult) error {
	failed, blocked := 0, 0
	for _, row := range result.Results {
		switch row.Status {
		case "failed":
			failed++
		case "blocked":
			blocked++
		}
	}
	return fmt.Errorf("%w: Jira plan completed with %d blocked and %d failed operation(s)", domain.ErrCheckFailed, blocked, failed)
}

func (s *JiraService) planPolicyBlocks(ctx context.Context, ops []jiraPlanOp, allowedOps, allowedFields map[string]bool, allowedLinkTypes []string) (map[int]string, error) {
	blocked := map[int]string{}
	rowsBySource := map[string][]int{}
	for _, op := range ops {
		source := strings.ToUpper(strings.TrimSpace(op.Source))
		rowsBySource[source] = append(rowsBySource[source], op.Row)
	}
	for source, rows := range rowsBySource {
		if len(rows) < 2 {
			continue
		}
		for _, row := range rows {
			blocked[row] = fmt.Sprintf("schema version 1 permits one mutating row per source; %s appears %d times", source, len(rows))
		}
	}
	explicitTypes := lowerSet(allowedLinkTypes)
	needMetadata := false
	for _, op := range ops {
		if blocked[op.Row] != "" {
			continue
		}
		if !allowedOps[op.Op] {
			blocked[op.Row] = "operation is not allowlisted"
			continue
		}
		if op.Op == "field" && !allowedFields[op.Field] {
			blocked[op.Row] = "field is not allowlisted"
		}
		if op.Op == "link" && !explicitTypes[strings.ToLower(op.Type)] {
			needMetadata = true
		}
	}
	metadataTypes := map[string]bool{}
	if needMetadata {
		types, err := s.tr.LinkTypes(ctx)
		if err != nil {
			return nil, fmt.Errorf("validate Jira link types: %w", err)
		}
		metadataTypes = lowerSet(types)
	}
	for _, op := range ops {
		if blocked[op.Row] != "" || op.Op != "link" || explicitTypes[strings.ToLower(op.Type)] {
			continue
		}
		if !metadataTypes[strings.ToLower(op.Type)] {
			blocked[op.Row] = "link type is not present in Jira metadata or --allow-link-types"
		}
	}
	return blocked, nil
}

func lowerSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out[strings.ToLower(part)] = true
			}
		}
	}
	return out
}

func planFieldEqual(current any, desired string) bool {
	desired = strings.TrimSpace(desired)
	if desired == "" || (desired[0] != '{' && desired[0] != '[') {
		return fieldString(current) == desired
	}
	var decoded any
	if json.Unmarshal([]byte(desired), &decoded) != nil {
		return false
	}
	return planValueContains(current, decoded)
}

func planValueContains(current, desired any) bool {
	switch want := desired.(type) {
	case map[string]any:
		got, ok := current.(map[string]any)
		if !ok {
			return false
		}
		if len(want) == 0 {
			return len(got) == 0
		}
		for key, value := range want {
			gotValue, exists := got[key]
			if !exists || !planValueContains(gotValue, value) {
				return false
			}
		}
		return true
	case []any:
		got, ok := current.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if !planValueContains(got[i], want[i]) {
				return false
			}
		}
		return true
	default:
		currentJSON, currentErr := json.Marshal(current)
		desiredJSON, desiredErr := json.Marshal(desired)
		return currentErr == nil && desiredErr == nil && bytes.Equal(currentJSON, desiredJSON)
	}
}

func parseJiraPlanCSV(data []byte) ([]jiraPlanOp, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: CSV is empty", domain.ErrUsage)
	}
	cols := jiraPlanColumns(records[0])
	for _, required := range []string{"version", "op", "source", "expected_updated"} {
		if cols[required] < 0 {
			return nil, fmt.Errorf("%w: CSV must include %s column", domain.ErrUsage, required)
		}
	}
	var ops []jiraPlanOp
	for i, record := range records[1:] {
		rowNo := i + 2
		op := jiraPlanOp{
			Version:         planVersion(csvCell(record, cols["version"])),
			Row:             rowNo,
			Op:              normalizePlanOp(csvCell(record, cols["op"])),
			Source:          csvCell(record, cols["source"]),
			Target:          csvCell(record, cols["target"]),
			Type:            csvCell(record, cols["type"]),
			Field:           csvCell(record, cols["field"]),
			Value:           csvCell(record, cols["value"]),
			Rationale:       csvCell(record, cols["rationale"]),
			ExpectedUpdated: csvCell(record, cols["expected_updated"]),
		}
		if op.Op == "" && op.Source == "" {
			continue
		}
		if err := validateJiraPlanOp(op); err != nil {
			return nil, fmt.Errorf("%w: CSV row %d: %v", domain.ErrUsage, rowNo, err)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func jiraPlanColumns(header []string) map[string]int {
	out := map[string]int{"version": -1, "op": -1, "source": -1, "target": -1, "type": -1, "field": -1, "value": -1, "rationale": -1, "expected_updated": -1}
	aliases := map[string]string{
		"version":         "version",
		"schemaversion":   "version",
		"op":              "op",
		"operation":       "op",
		"action":          "op",
		"source":          "source",
		"from":            "source",
		"issue":           "source",
		"key":             "source",
		"target":          "target",
		"to":              "target",
		"linkedissue":     "target",
		"type":            "type",
		"linktype":        "type",
		"field":           "field",
		"value":           "value",
		"label":           "value",
		"comment":         "value",
		"rationale":       "rationale",
		"reason":          "rationale",
		"expectedupdated": "expected_updated",
	}
	for i, raw := range header {
		if name, ok := aliases[normalizeHeader(raw)]; ok && out[name] < 0 {
			out[name] = i
		}
	}
	return out
}

func planVersion(value string) int {
	version, _ := strconv.Atoi(strings.TrimSpace(value))
	return version
}

func normalizePlanOp(op string) string {
	op = normalizeHeader(op)
	switch op {
	case "link", "addlink":
		return "link"
	case "labeladd", "addlabel", "labelsadd":
		return "label_add"
	case "labelremove", "removelabel", "labelsremove":
		return "label_remove"
	case "comment", "addcomment":
		return "comment"
	case "field", "setfield", "updatefield":
		return "field"
	default:
		return op
	}
}

func validateJiraPlanOp(op jiraPlanOp) error {
	if op.Version != jiraPlanSchemaVersion {
		return fmt.Errorf("unsupported schema version %d (want %d)", op.Version, jiraPlanSchemaVersion)
	}
	if op.Op == "" {
		return fmt.Errorf("op is required")
	}
	if op.Source == "" {
		return fmt.Errorf("source is required")
	}
	if op.ExpectedUpdated == "" {
		return fmt.Errorf("expected_updated is required")
	}
	switch op.Op {
	case "link":
		if op.Target == "" || op.Type == "" {
			return fmt.Errorf("link requires target and type")
		}
	case "label_add", "label_remove", "comment":
		if op.Value == "" {
			return fmt.Errorf("%s requires value", op.Op)
		}
	case "field":
		if op.Field == "" || op.Value == "" {
			return fmt.Errorf("field requires field and value")
		}
	default:
		return fmt.Errorf("unsupported operation %q", op.Op)
	}
	return nil
}

func allowSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out[normalizePlanOp(part)] = true
			}
		}
	}
	return out
}

func exactAllowSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out[part] = true
			}
		}
	}
	return out
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func splitCSVValue(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
