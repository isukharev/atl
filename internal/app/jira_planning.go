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
	RawCSV        bool
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

// JiraIssueRefsOpts controls standalone artifact reference extraction.
type JiraIssueRefsOpts struct {
	Key    string
	JQL    string
	Fields []string
	Limit  int
}

// JiraIssueRefsResult is a deterministic list of artifact refs per issue.
type JiraIssueRefsResult struct {
	Key    string          `json:"key,omitempty"`
	JQL    string          `json:"jql,omitempty"`
	Count  int             `json:"count"`
	Issues []JiraIssueRefs `json:"issues"`
}

// JiraIssueRefs is the refs found in one issue.
type JiraIssueRefs struct {
	Key     string        `json:"key"`
	Summary string        `json:"summary,omitempty"`
	Type    string        `json:"type,omitempty"`
	Refs    []PlanningRef `json:"refs"`
}

// JiraIssueTreeOpts controls normalized epic tree extraction.
type JiraIssueTreeOpts struct {
	JQL       string
	EpicField string
	Fields    []string
	Limit     int
}

// JiraIssueTreeResult groups selected issues by epic field.
type JiraIssueTreeResult struct {
	JQL           string              `json:"jql"`
	EpicField     string              `json:"epic_field"`
	Count         int                 `json:"count"`
	Epics         []JiraIssueTreeEpic `json:"epics"`
	ExternalEpics []JiraIssueTreeEpic `json:"external_epics,omitempty"`
	Orphans       []JiraIssueTreeItem `json:"orphans,omitempty"`
}

// JiraIssueTreeEpic is one epic and its selected child issues.
type JiraIssueTreeEpic struct {
	Key      string              `json:"key"`
	Summary  string              `json:"summary,omitempty"`
	Type     string              `json:"type,omitempty"`
	External bool                `json:"external,omitempty"`
	Children []JiraIssueTreeItem `json:"children"`
}

// JiraIssueTreeItem is a selected issue in the tree.
type JiraIssueTreeItem struct {
	Key     string `json:"key"`
	Summary string `json:"summary,omitempty"`
	Type    string `json:"type,omitempty"`
	Epic    string `json:"epic,omitempty"`
}

// PlanningReport builds a deterministic planning quality report over a JQL query.
func (s *JiraService) PlanningReport(ctx context.Context, opts PlanningReportOpts) (*PlanningReport, error) {
	if strings.TrimSpace(opts.JQL) == "" {
		return nil, fmt.Errorf("%w: --jql is required", domain.ErrUsage)
	}
	if opts.RawCSV && strings.TrimSpace(opts.CSVPath) == "" {
		return nil, fmt.Errorf("%w: --raw-csv requires --csv", domain.ErrUsage)
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
		data, err := renderPlanningCSV(rows, opts.RawCSV)
		if err != nil {
			return nil, err
		}
		if err := writePlanningFile(opts.CSVPath, data); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// IssueRefs extracts artifact references from one issue or a JQL selection.
func (s *JiraService) IssueRefs(ctx context.Context, opts JiraIssueRefsOpts) (*JiraIssueRefsResult, error) {
	key := strings.TrimSpace(opts.Key)
	jql := strings.TrimSpace(opts.JQL)
	if (key == "") == (jql == "") {
		return nil, fmt.Errorf("%w: pass exactly one of issue key or --jql", domain.ErrUsage)
	}
	fields := mergeFields([]string{"summary", "description", "issuetype", "comment"}, opts.Fields)
	var issues []domain.Issue
	if key != "" {
		issue, err := s.tr.GetIssue(ctx, key, fields)
		if err != nil {
			return nil, err
		}
		issues = append(issues, *issue)
	} else {
		found, err := s.collectPlanningIssues(ctx, jql, fields, opts.Limit)
		if err != nil {
			return nil, err
		}
		issues = found
	}
	rows := make([]JiraIssueRefs, 0, len(issues))
	for _, issue := range issues {
		rows = append(rows, JiraIssueRefs{
			Key:     issue.Key,
			Summary: issue.Summary,
			Type:    issue.Type,
			Refs:    ExtractPlanningRefs(issueText(issue)),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return &JiraIssueRefsResult{Key: key, JQL: jql, Count: len(rows), Issues: rows}, nil
}

// IssueTree groups a JQL selection into epics, external epics, and orphans.
func (s *JiraService) IssueTree(ctx context.Context, opts JiraIssueTreeOpts) (*JiraIssueTreeResult, error) {
	jql := strings.TrimSpace(opts.JQL)
	if jql == "" {
		return nil, fmt.Errorf("%w: --jql is required", domain.ErrUsage)
	}
	epicField := strings.TrimSpace(opts.EpicField)
	if epicField == "" {
		return nil, fmt.Errorf("%w: --epic-field is required", domain.ErrUsage)
	}
	fields := mergeFields([]string{"summary", "issuetype", epicField}, opts.Fields)
	issues, err := s.collectPlanningIssues(ctx, jql, fields, opts.Limit)
	if err != nil {
		return nil, err
	}
	result := buildJiraIssueTree(jql, epicField, issues)
	return &result, nil
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

func buildJiraIssueTree(jql, epicField string, issues []domain.Issue) JiraIssueTreeResult {
	result := JiraIssueTreeResult{JQL: jql, EpicField: epicField, Count: len(issues)}
	epics := map[string]*JiraIssueTreeEpic{}
	external := map[string]*JiraIssueTreeEpic{}
	for _, issue := range issues {
		if strings.EqualFold(issue.Type, "epic") {
			epics[issue.Key] = &JiraIssueTreeEpic{Key: issue.Key, Summary: issue.Summary, Type: issue.Type}
		}
	}
	for _, issue := range issues {
		if strings.EqualFold(issue.Type, "epic") {
			continue
		}
		item := JiraIssueTreeItem{Key: issue.Key, Summary: issue.Summary, Type: issue.Type, Epic: fieldString(issue.Fields[epicField])}
		if item.Epic == "" {
			result.Orphans = append(result.Orphans, item)
			continue
		}
		parent := epics[item.Epic]
		if parent == nil {
			parent = external[item.Epic]
			if parent == nil {
				parent = &JiraIssueTreeEpic{Key: item.Epic, External: true}
				external[item.Epic] = parent
			}
		}
		parent.Children = append(parent.Children, item)
	}
	result.Epics = sortedTreeEpics(epics)
	result.ExternalEpics = sortedTreeEpics(external)
	sortTreeItems(result.Orphans)
	return result
}

func sortedTreeEpics(m map[string]*JiraIssueTreeEpic) []JiraIssueTreeEpic {
	out := make([]JiraIssueTreeEpic, 0, len(m))
	for _, epic := range m {
		sortTreeItems(epic.Children)
		out = append(out, *epic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func sortTreeItems(items []JiraIssueTreeItem) {
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
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

func renderPlanningCSV(rows []PlanningIssueQuality, rawCSV bool) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.Write(spreadsheetRecord([]string{"key", "level", "score", "max_score", "gaps", "refs", "children"}, rawCSV)); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := w.Write(spreadsheetRecord([]string{
			row.Key,
			row.Level,
			fmt.Sprint(row.Score),
			fmt.Sprint(row.MaxScore),
			strings.Join(row.Gaps, ";"),
			fmt.Sprint(len(row.Refs)),
			strings.Join(row.Children, ";"),
		}, rawCSV)); err != nil {
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
