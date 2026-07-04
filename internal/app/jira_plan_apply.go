package app

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const planApplyConfirm = "APPLY"

// JiraPlanApplyOpts controls guarded plan execution.
type JiraPlanApplyOpts struct {
	CSVPath     string
	Apply       bool
	Confirm     string
	AllowOps    []string
	AllowFields []string
}

// JiraPlanApplyResult is an auditable dry-run/apply report.
type JiraPlanApplyResult struct {
	Path    string                   `json:"path,omitempty"`
	Mode    string                   `json:"mode"`
	Count   int                      `json:"count"`
	Results []JiraPlanApplyResultRow `json:"results"`
}

// JiraPlanApplyResultRow is one operation result.
type JiraPlanApplyResultRow struct {
	Row       int    `json:"row"`
	Op        string `json:"op"`
	Source    string `json:"source"`
	Target    string `json:"target,omitempty"`
	Type      string `json:"type,omitempty"`
	Field     string `json:"field,omitempty"`
	Value     string `json:"value,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

type jiraPlanOp struct {
	Row       int
	Op        string
	Source    string
	Target    string
	Type      string
	Field     string
	Value     string
	Rationale string
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
	allowedFields := allowSet(opts.AllowFields)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	result := &JiraPlanApplyResult{Path: path, Mode: mode}
	for _, op := range ops {
		row := s.applyPlanOp(ctx, op, opts.Apply, allowedOps, allowedFields)
		result.Results = append(result.Results, row)
	}
	result.Count = len(result.Results)
	sort.Slice(result.Results, func(i, j int) bool { return result.Results[i].Row < result.Results[j].Row })
	return result, nil
}

func (s *JiraService) applyPlanOp(ctx context.Context, op jiraPlanOp, apply bool, allowedOps, allowedFields map[string]bool) JiraPlanApplyResultRow {
	row := JiraPlanApplyResultRow{
		Row:       op.Row,
		Op:        op.Op,
		Source:    op.Source,
		Target:    op.Target,
		Type:      op.Type,
		Field:     op.Field,
		Value:     op.Value,
		Rationale: op.Rationale,
	}
	if !allowedOps[op.Op] {
		row.Status = "blocked"
		row.Message = "operation is not allowlisted"
		return row
	}
	switch op.Op {
	case "link":
		return s.applyPlanLink(ctx, row, apply)
	case "label_add", "label_remove":
		return s.applyPlanLabel(ctx, row, apply)
	case "comment":
		return s.applyPlanComment(ctx, row, apply)
	case "field":
		if !allowedFields[row.Field] {
			row.Status = "blocked"
			row.Message = "field is not allowlisted"
			return row
		}
		return s.applyPlanField(ctx, row, apply)
	default:
		row.Status = "blocked"
		row.Message = "unsupported operation"
		return row
	}
}

func (s *JiraService) applyPlanLink(ctx context.Context, row JiraPlanApplyResultRow, apply bool) JiraPlanApplyResultRow {
	links, err := s.Links(ctx, row.Source)
	if err != nil {
		return failedPlanRow(row, err)
	}
	if existingOutwardLinks(links)[linkIdentity(row.Target, row.Type)] {
		row.Status = "already_satisfied"
		return row
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
	issue, err := s.tr.GetIssue(ctx, row.Source, []string{"labels"})
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
	issue, err := s.tr.GetIssue(ctx, row.Source, []string{row.Field})
	if err != nil {
		return failedPlanRow(row, err)
	}
	if fieldString(issue.Fields[row.Field]) == row.Value {
		row.Status = "already_satisfied"
		return row
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
	row.Message = err.Error()
	return row
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
	for _, required := range []string{"op", "source"} {
		if cols[required] < 0 {
			return nil, fmt.Errorf("%w: CSV must include %s column", domain.ErrUsage, required)
		}
	}
	var ops []jiraPlanOp
	for i, record := range records[1:] {
		rowNo := i + 2
		op := jiraPlanOp{
			Row:       rowNo,
			Op:        normalizePlanOp(csvCell(record, cols["op"])),
			Source:    csvCell(record, cols["source"]),
			Target:    csvCell(record, cols["target"]),
			Type:      csvCell(record, cols["type"]),
			Field:     csvCell(record, cols["field"]),
			Value:     csvCell(record, cols["value"]),
			Rationale: csvCell(record, cols["rationale"]),
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
	out := map[string]int{"op": -1, "source": -1, "target": -1, "type": -1, "field": -1, "value": -1, "rationale": -1}
	aliases := map[string]string{
		"op":          "op",
		"operation":   "op",
		"action":      "op",
		"source":      "source",
		"from":        "source",
		"issue":       "source",
		"key":         "source",
		"target":      "target",
		"to":          "target",
		"linkedissue": "target",
		"type":        "type",
		"linktype":    "type",
		"field":       "field",
		"value":       "value",
		"label":       "value",
		"comment":     "value",
		"rationale":   "rationale",
		"reason":      "rationale",
	}
	for i, raw := range header {
		if name, ok := aliases[normalizeHeader(raw)]; ok && out[name] < 0 {
			out[name] = i
		}
	}
	return out
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
	if op.Op == "" {
		return fmt.Errorf("op is required")
	}
	if op.Source == "" {
		return fmt.Errorf("source is required")
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
