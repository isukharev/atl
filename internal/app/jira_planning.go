package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// PlanningReportOpts controls deterministic read-only planning quality checks.
type PlanningReportOpts struct {
	JQL           string
	Required      []string
	EstimateField string
	EpicField     string
	Limit         int
	CSVPath       string
}

type PlanningReport struct {
	JQL     string                 `json:"jql"`
	Count   int                    `json:"count"`
	CSVPath string                 `json:"csv_path,omitempty"`
	Issues  []PlanningIssueQuality `json:"issues"`
	Summary PlanningSummary        `json:"summary"`
}

type PlanningSummary struct {
	Good int `json:"good"`
	Warn int `json:"warn"`
	Poor int `json:"poor"`
}

type PlanningIssueQuality struct {
	Key      string        `json:"key"`
	Summary  string        `json:"summary,omitempty"`
	Type     string        `json:"type,omitempty"`
	Epic     string        `json:"epic,omitempty"`
	Score    int           `json:"score"`
	MaxScore int           `json:"max_score"`
	Level    string        `json:"level"`
	Gaps     []string      `json:"gaps,omitempty"`
	Refs     []PlanningRef `json:"refs,omitempty"`
	Children []string      `json:"children,omitempty"`
}

type PlanningRef struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

// PlanningReport builds a deterministic planning quality report over a JQL query.
func (s *JiraService) PlanningReport(ctx context.Context, opts PlanningReportOpts) (*PlanningReport, error) {
	if strings.TrimSpace(opts.JQL) == "" {
		return nil, fmt.Errorf("%w: --jql is required", domain.ErrUsage)
	}
	fields := planningFields(opts)
	issues, err := s.collectPlanningIssues(ctx, opts.JQL, fields, opts.Limit)
	if err != nil {
		return nil, err
	}
	rows := scorePlanningIssues(issues, opts)
	report := &PlanningReport{JQL: opts.JQL, Count: len(rows), Issues: rows, CSVPath: opts.CSVPath}
	for _, row := range rows {
		switch row.Level {
		case "good":
			report.Summary.Good++
		case "warn":
			report.Summary.Warn++
		default:
			report.Summary.Poor++
		}
	}
	if opts.CSVPath != "" {
		data, err := renderPlanningCSV(rows)
		if err != nil {
			return nil, err
		}
		if err := writePlanningFile(opts.CSVPath, data); err != nil {
			return nil, err
		}
	}
	return report, nil
}

func planningFields(opts PlanningReportOpts) []string {
	base := []string{"summary", "description", "issuetype", "assignee", "comment"}
	return mergeFields(base, append(append([]string{}, opts.Required...), opts.EstimateField, opts.EpicField))
}

func mergeFields(base, extra []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range append(base, extra...) {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func (s *JiraService) collectPlanningIssues(ctx context.Context, jql string, fields []string, limit int) ([]domain.Issue, error) {
	var out []domain.Issue
	cursor := ""
	for len(out) < limit || limit == 0 {
		pageLimit := 100
		if limit > 0 && limit-len(out) < pageLimit {
			pageLimit = limit - len(out)
		}
		issues, next, err := s.tr.Search(ctx, jql, fields, pageLimit, cursor)
		if err != nil {
			return out, err
		}
		out = append(out, issues...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		if next == "" || len(issues) == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

func scorePlanningIssues(issues []domain.Issue, opts PlanningReportOpts) []PlanningIssueQuality {
	childrenByEpic := map[string][]string{}
	if opts.EpicField != "" {
		for _, is := range issues {
			if epic := fieldString(is.Fields[opts.EpicField]); epic != "" {
				childrenByEpic[epic] = append(childrenByEpic[epic], is.Key)
			}
		}
	}
	rows := make([]PlanningIssueQuality, 0, len(issues))
	for _, is := range issues {
		row := PlanningIssueQuality{Key: is.Key, Summary: is.Summary, Type: is.Type}
		checks := []planningCheck{
			{name: "summary", ok: strings.TrimSpace(is.Summary) != ""},
			{name: "description", ok: strings.TrimSpace(is.Body) != ""},
			{name: "assignee", ok: strings.TrimSpace(is.Assignee) != ""},
		}
		if opts.EstimateField != "" {
			checks = append(checks, planningCheck{name: opts.EstimateField, ok: !fieldEmpty(is.Fields[opts.EstimateField])})
		}
		for _, f := range opts.Required {
			if f == opts.EstimateField {
				continue
			}
			checks = append(checks, planningCheck{name: f, ok: !fieldEmpty(is.Fields[f])})
		}
		row.Refs = ExtractPlanningRefs(issueText(is))
		checks = append(checks, planningCheck{name: "artifact_ref", ok: len(row.Refs) > 0})
		for _, check := range checks {
			row.MaxScore++
			if check.ok {
				row.Score++
			} else {
				row.Gaps = append(row.Gaps, "missing_"+check.name)
			}
		}
		if opts.EpicField != "" {
			row.Epic = fieldString(is.Fields[opts.EpicField])
			if strings.EqualFold(is.Type, "epic") {
				row.Children = append(row.Children, childrenByEpic[is.Key]...)
				sort.Strings(row.Children)
				if len(row.Children) == 0 {
					row.Gaps = append(row.Gaps, "missing_children")
				}
			} else if row.Epic == "" {
				row.Gaps = append(row.Gaps, "missing_epic")
			}
		}
		row.Level = planningLevel(row.Score, row.MaxScore, row.Gaps)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows
}

type planningCheck struct {
	name string
	ok   bool
}

func planningLevel(score, max int, gaps []string) string {
	if max == 0 {
		return "poor"
	}
	pct := score * 100 / max
	if pct >= 80 && len(gaps) == 0 {
		return "good"
	}
	if pct >= 50 {
		return "warn"
	}
	return "poor"
}

func issueText(is domain.Issue) string {
	var b strings.Builder
	b.WriteString(is.Body)
	for _, c := range is.Comments {
		b.WriteByte('\n')
		b.WriteString(c.Body)
	}
	return b.String()
}

func fieldString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		for _, k := range []string{"key", "name", "value", "displayName"} {
			if s := fieldString(t[k]); s != "" {
				return s
			}
		}
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := fieldString(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	default:
		b, err := json.Marshal(t)
		if err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", t)
	}
	return ""
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

func ExtractPlanningRefs(text string) []PlanningRef {
	seen := map[string]bool{}
	var refs []PlanningRef
	for _, raw := range urlRe.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ".,;:")
		if seen[raw] {
			continue
		}
		seen[raw] = true
		refs = append(refs, PlanningRef{URL: raw, Kind: classifyPlanningRef(raw)})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].URL < refs[j].URL })
	return refs
}

func classifyPlanningRef(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "link"
	}
	host := strings.ToLower(u.Host)
	path := strings.ToLower(u.Path)
	switch {
	case strings.Contains(host, "figma.com"):
		return "design"
	case strings.Contains(host, "confluence") || strings.Contains(path, "/wiki/"):
		return "doc"
	case strings.Contains(host, "jira") || strings.Contains(path, "/browse/"):
		return "jira"
	case strings.Contains(host, "slack.com") || strings.Contains(host, "teams.microsoft.com"):
		return "chat"
	case strings.Contains(host, "docs.") || strings.Contains(host, "notion.") || strings.Contains(host, "sharepoint."):
		return "doc"
	default:
		return "link"
	}
}

func renderPlanningCSV(rows []PlanningIssueQuality) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.Write([]string{"key", "level", "score", "max_score", "gaps", "refs", "children"}); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := w.Write([]string{
			row.Key,
			row.Level,
			fmt.Sprint(row.Score),
			fmt.Sprint(row.MaxScore),
			strings.Join(row.Gaps, ";"),
			fmt.Sprint(len(row.Refs)),
			strings.Join(row.Children, ";"),
		}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writePlanningFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	if !safepath.Within(dir, clean) {
		return fmt.Errorf("refusing unsafe output path %q", path)
	}
	return safepath.WriteFile(clean, data, 0o644)
}
