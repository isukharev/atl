package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraDigestChildrenCap = 1000
	jiraDigestCommentsCap = 50
	jiraDigestHistoryCap  = 500
	jiraDigestTextCap     = 128 << 10
)

var jiraDigestDefaultIncludes = []string{"identity", "status-field", "children", "comments", "links", "history", "refs"}

type JiraEpicDigestConfluenceReader interface {
	PageSection(context.Context, string, ConfluencePageSectionOpts) (*ConfluencePageSectionResult, error)
}

type JiraEpicDigestOpts struct {
	Quarter           string
	Since             string
	Until             string
	Include           []string
	StatusField       string
	DoDField          string
	EpicField         string
	ChildLimit        int
	CommentLimit      int
	HistoryLimit      int
	ExpandConfluence  int
	ConfluenceHeading string
	Confluence        JiraEpicDigestConfluenceReader
}

type JiraDigestPeriod struct {
	Quarter string `json:"quarter,omitempty"`
	Since   string `json:"since,omitempty"`
	Until   string `json:"until,omitempty"`
}

type JiraDigestSource struct {
	Complete bool   `json:"complete"`
	Count    int    `json:"count"`
	Warning  string `json:"warning,omitempty"`
}

type JiraDigestIdentity struct {
	Key         string `json:"key"`
	Summary     string `json:"summary"`
	Status      string `json:"status"`
	Resolution  string `json:"resolution,omitempty"`
	Type        string `json:"type,omitempty"`
	Updated     string `json:"updated,omitempty"`
	Description string `json:"description,omitempty"`
}

type JiraDigestFieldEvidence struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Value      string               `json:"value,omitempty"`
	LastChange *JiraFieldLastChange `json:"last_change,omitempty"`
	Truncated  bool                 `json:"truncated,omitempty"`
}

type JiraDigestChildren struct {
	List            *IssueList     `json:"list"`
	ByStatus        map[string]int `json:"by_status"`
	UpdatedInPeriod int            `json:"updated_in_period"`
	LatestUpdated   string         `json:"latest_updated,omitempty"`
}

type JiraDigestStaleness struct {
	Evaluated          bool     `json:"evaluated"`
	Stale              bool     `json:"stale"`
	StatusFieldUpdated string   `json:"status_field_updated,omitempty"`
	LatestEvidenceAt   string   `json:"latest_evidence_at,omitempty"`
	NewerChildUpdates  int      `json:"newer_child_updates"`
	NewerComments      int      `json:"newer_comments"`
	Reasons            []string `json:"reasons"`
}

type JiraDigestConfluenceEvidence struct {
	URL     string                       `json:"url"`
	Section *ConfluencePageSectionResult `json:"section"`
}

type JiraEpicDigestResult struct {
	SchemaVersion int                            `json:"schema_version"`
	Period        JiraDigestPeriod               `json:"period"`
	Includes      []string                       `json:"includes"`
	Sources       map[string]JiraDigestSource    `json:"sources"`
	Epic          JiraDigestIdentity             `json:"epic"`
	StatusField   *JiraDigestFieldEvidence       `json:"status_field,omitempty"`
	DoDField      *JiraDigestFieldEvidence       `json:"dod_field,omitempty"`
	Children      *JiraDigestChildren            `json:"children,omitempty"`
	Comments      []domain.Comment               `json:"comments,omitempty"`
	Links         []domain.IssueLink             `json:"links,omitempty"`
	Blockers      []domain.IssueLink             `json:"blockers,omitempty"`
	History       []domain.ChangelogEntry        `json:"history,omitempty"`
	Refs          []PlanningRef                  `json:"refs,omitempty"`
	Confluence    []JiraDigestConfluenceEvidence `json:"confluence,omitempty"`
	Staleness     JiraDigestStaleness            `json:"staleness"`
	Warnings      []string                       `json:"warnings,omitempty"`
}

func (s *JiraService) EpicDigest(ctx context.Context, key string, opts JiraEpicDigestOpts) (*JiraEpicDigestResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: epic key is required", domain.ErrUsage)
	}
	period, err := jiraDigestPeriod(opts)
	if err != nil {
		return nil, err
	}
	includes, includeSet, err := jiraDigestIncludes(opts.Include)
	if err != nil {
		return nil, err
	}
	if opts.ExpandConfluence < 0 || opts.ExpandConfluence > 10 {
		return nil, fmt.Errorf("%w: --expand-confluence must be between 0 and 10", domain.ErrUsage)
	}
	if opts.ChildLimit < 0 || opts.CommentLimit < 0 || opts.HistoryLimit < 0 {
		return nil, fmt.Errorf("%w: digest limits must be >= 0", domain.ErrUsage)
	}
	if opts.ExpandConfluence > 0 && strings.TrimSpace(opts.ConfluenceHeading) == "" {
		return nil, fmt.Errorf("%w: --confluence-heading is required with --expand-confluence", domain.ErrUsage)
	}
	if includeSet["confluence"] && opts.ExpandConfluence == 0 {
		return nil, fmt.Errorf("%w: confluence include requires --expand-confluence", domain.ErrUsage)
	}
	if opts.ExpandConfluence > 0 {
		includeSet["confluence"] = true
		includeSet["refs"] = true
		includes = sortedDigestIncludes(includeSet)
	}
	defs := []domain.FieldDef{}
	var statusDef, dodDef *domain.FieldDef
	if strings.TrimSpace(opts.StatusField) != "" && includeSet["status-field"] {
		resolved, resolveErr := s.resolveJiraFieldSelectors(ctx, []string{opts.StatusField})
		if resolveErr != nil {
			return nil, resolveErr
		}
		def := resolved[0]
		statusDef = &def
		defs = append(defs, def)
	}
	if strings.TrimSpace(opts.DoDField) != "" {
		resolved, resolveErr := s.resolveJiraFieldSelectors(ctx, []string{opts.DoDField})
		if resolveErr != nil {
			return nil, resolveErr
		}
		def := resolved[0]
		dodDef = &def
		if statusDef == nil || statusDef.ID != def.ID {
			defs = append(defs, def)
		}
	}
	fields := []string{"summary", "status", "resolution", "description", "issuetype", "updated", "issuelinks"}
	fields = append(fields, fieldDefIDs(defs)...)
	issue, err := s.tr.GetIssue(ctx, key, mergeFields(nil, fields))
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("%w: Jira returned no epic snapshot", domain.ErrCheckFailed)
	}
	result := &JiraEpicDigestResult{SchemaVersion: 1, Period: period, Includes: includes, Sources: map[string]JiraDigestSource{}, Warnings: []string{}}
	description, descriptionTruncated := digestBoundedText(issue.Body)
	result.Epic = JiraDigestIdentity{Key: issue.Key, Summary: issue.Summary, Status: issue.Status, Resolution: renderFieldValue(issue.Fields["resolution"]), Type: issue.Type, Updated: renderFieldValue(issue.Fields["updated"]), Description: description}
	result.Sources["identity"] = JiraDigestSource{Complete: !descriptionTruncated, Count: 1, Warning: digestTruncationWarning(descriptionTruncated)}
	if statusDef != nil && includeSet["status-field"] {
		value, truncated := digestBoundedText(renderFieldValue(issue.Fields[statusDef.ID]))
		result.StatusField = &JiraDigestFieldEvidence{ID: statusDef.ID, Name: statusDef.Name, Value: value, Truncated: truncated}
		result.Sources["status-field"] = JiraDigestSource{Complete: !truncated, Count: boolCount(value != ""), Warning: digestTruncationWarning(truncated)}
	} else if includeSet["status-field"] {
		result.Sources["status-field"] = JiraDigestSource{Complete: false, Warning: "status field not configured"}
	}
	if dodDef != nil {
		value, truncated := digestBoundedText(renderFieldValue(issue.Fields[dodDef.ID]))
		result.DoDField = &JiraDigestFieldEvidence{ID: dodDef.ID, Name: dodDef.Name, Value: value, Truncated: truncated}
		result.Sources["dod-field"] = JiraDigestSource{Complete: !truncated, Count: boolCount(value != ""), Warning: digestTruncationWarning(truncated)}
	}

	var historyResult *JiraHistoryResult
	if includeSet["history"] || result.StatusField != nil {
		historyResult, err = s.HistoryFiltered(ctx, key, JiraHistoryOpts{Since: period.Since, Until: period.Until})
		if err != nil {
			result.Sources["history"] = JiraDigestSource{Complete: false, Warning: "history unavailable"}
			result.Warnings = append(result.Warnings, "history unavailable")
			if result.StatusField != nil {
				source := result.Sources["status-field"]
				source.Complete = false
				source.Warning = "status field change history unavailable"
				result.Sources["status-field"] = source
			}
		} else {
			if result.StatusField != nil && !historyResult.Complete {
				source := result.Sources["status-field"]
				source.Complete = false
				source.Warning = "status field change history incomplete"
				result.Sources["status-field"] = source
			}
			if statusDef != nil {
				if change := digestLastFieldChange(historyResult.History, *statusDef); change != nil {
					result.StatusField.LastChange = change
				}
			}
			if includeSet["history"] {
				limit := digestLimit(opts.HistoryLimit, jiraDigestHistoryCap)
				var truncated bool
				result.History, truncated = tailHistory(historyResult.History, limit)
				complete := historyResult.Complete && !truncated
				warning := historyResult.PartialReason
				if truncated {
					warning = "digest history cap reached"
				}
				result.Sources["history"] = JiraDigestSource{Complete: complete, Count: len(result.History), Warning: warning}
			}
		}
	}

	if includeSet["children"] {
		children, source, childErr := s.digestChildren(ctx, key, opts, period)
		result.Sources["children"] = source
		if childErr != nil {
			result.Warnings = append(result.Warnings, "children unavailable")
		} else {
			result.Children = children
		}
	}
	if includeSet["comments"] {
		comments, commentErr := s.tr.ListComments(ctx, key)
		if commentErr != nil {
			result.Sources["comments"] = JiraDigestSource{Complete: false, Warning: "comments unavailable"}
			result.Warnings = append(result.Warnings, "comments unavailable")
		} else {
			comments, timeIncomplete := filterDigestComments(comments, period)
			limit := digestLimit(opts.CommentLimit, jiraDigestCommentsCap)
			var truncated bool
			result.Comments, truncated = tailComments(comments, limit)
			warning := digestCapWarning(truncated, "digest comment/text cap reached")
			if timeIncomplete {
				warning = "comments with unsupported timestamps omitted"
			}
			result.Sources["comments"] = JiraDigestSource{Complete: !truncated && !timeIncomplete, Count: len(result.Comments), Warning: warning}
		}
	}
	if includeSet["links"] {
		result.Links = append([]domain.IssueLink(nil), issue.Links...)
		sort.Slice(result.Links, func(i, j int) bool {
			return result.Links[i].Key+result.Links[i].Type < result.Links[j].Key+result.Links[j].Type
		})
		for _, link := range result.Links {
			if strings.Contains(strings.ToLower(link.Type+" "+link.TypeName), "block") {
				result.Blockers = append(result.Blockers, link)
			}
		}
		result.Sources["links"] = JiraDigestSource{Complete: true, Count: len(result.Links)}
	}
	if includeSet["refs"] || opts.ExpandConfluence > 0 {
		var evidence strings.Builder
		evidence.WriteString(issueText(*issue))
		if result.StatusField != nil {
			evidence.WriteString("\n" + result.StatusField.Value)
		}
		if result.DoDField != nil {
			evidence.WriteString("\n" + result.DoDField.Value)
		}
		for _, comment := range result.Comments {
			evidence.WriteString("\n" + comment.Body)
		}
		result.Refs = ExtractPlanningRefs(evidence.String())
		complete := result.Sources["identity"].Complete && (!includeSet["comments"] || result.Sources["comments"].Complete)
		result.Sources["refs"] = JiraDigestSource{Complete: complete, Count: len(result.Refs), Warning: digestCapWarning(!complete, "reference source text incomplete")}
	}
	if opts.ExpandConfluence > 0 {
		result.expandDigestConfluence(ctx, opts)
	}
	result.Staleness = digestStaleness(result)
	sort.Strings(result.Warnings)
	return result, nil
}

func jiraDigestPeriod(opts JiraEpicDigestOpts) (JiraDigestPeriod, error) {
	quarter := strings.TrimSpace(opts.Quarter)
	since, until := strings.TrimSpace(opts.Since), strings.TrimSpace(opts.Until)
	if quarter != "" && (since != "" || until != "") {
		return JiraDigestPeriod{}, fmt.Errorf("%w: --quarter conflicts with --since/--until", domain.ErrUsage)
	}
	if (since == "") != (until == "") {
		return JiraDigestPeriod{}, fmt.Errorf("%w: pass both --since and --until", domain.ErrUsage)
	}
	if quarter != "" {
		var year, q int
		if _, err := fmt.Sscanf(quarter, "%d-Q%d", &year, &q); err != nil || year < 1970 || q < 1 || q > 4 || fmt.Sprintf("%04d-Q%d", year, q) != quarter {
			return JiraDigestPeriod{}, fmt.Errorf("%w: --quarter must be YYYY-Q1..Q4", domain.ErrUsage)
		}
		start := time.Date(year, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 3, -1)
		return JiraDigestPeriod{Quarter: quarter, Since: start.Format("2006-01-02"), Until: end.Format("2006-01-02")}, nil
	}
	if since != "" {
		start, err1 := parseJiraHistoryBoundary(since, false)
		end, err2 := parseJiraHistoryBoundary(until, true)
		if err1 != nil || err2 != nil || !start.time.Before(end.time) {
			return JiraDigestPeriod{}, fmt.Errorf("%w: invalid digest date range", domain.ErrUsage)
		}
	}
	return JiraDigestPeriod{Since: since, Until: until}, nil
}

func jiraDigestIncludes(raw []string) ([]string, map[string]bool, error) {
	if len(raw) == 0 {
		raw = jiraDigestDefaultIncludes
	}
	valid := map[string]bool{"identity": true, "status-field": true, "children": true, "comments": true, "links": true, "history": true, "refs": true, "confluence": true}
	set := map[string]bool{"identity": true}
	for _, group := range raw {
		for _, item := range strings.Split(group, ",") {
			item = strings.TrimSpace(item)
			if !valid[item] {
				return nil, nil, fmt.Errorf("%w: unknown digest include %q", domain.ErrUsage, item)
			}
			set[item] = true
		}
	}
	return sortedDigestIncludes(set), set, nil
}

func sortedDigestIncludes(set map[string]bool) []string {
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func (s *JiraService) digestChildren(ctx context.Context, key string, opts JiraEpicDigestOpts, period JiraDigestPeriod) (*JiraDigestChildren, JiraDigestSource, error) {
	limit := digestLimit(opts.ChildLimit, jiraDigestChildrenCap)
	epicField, err := s.resolveEpicField(ctx, opts.EpicField)
	if err != nil {
		return nil, JiraDigestSource{Complete: false, Warning: "children unavailable"}, err
	}
	combined := &IssueList{SchemaVersion: issueListSchemaVersion, Source: IssueListSource{Kind: "epic", ID: key}, Rows: []IssueListRow{}, Page: IssueListPage{Complete: true}}
	cursor := ""
	exhausted := false
	for len(combined.Rows) < limit {
		pageLimit := min(100, limit-len(combined.Rows))
		page, pageErr := s.EpicChildrenIssueList(ctx, key, JiraEpicChildrenOpts{Columns: []string{"key", "summary", "status", "issuetype", "assignee", "updated"}, Limit: pageLimit, Cursor: cursor, EpicField: epicField})
		if pageErr != nil {
			return nil, JiraDigestSource{Complete: false, Count: len(combined.Rows), Warning: "children unavailable"}, pageErr
		}
		if combined.Projection.Columns == nil {
			combined.Selection = page.Selection
			combined.Projection = page.Projection
		}
		for _, row := range page.Rows {
			row.Position = len(combined.Rows)
			combined.Rows = append(combined.Rows, row)
		}
		if page.Page.NextCursor == nil || len(page.Rows) == 0 {
			exhausted = true
			break
		}
		cursor = *page.Page.NextCursor
	}
	combined.Page.Count = len(combined.Rows)
	combined.Page.Complete = exhausted
	combined.Page.Truncated = !combined.Page.Complete
	if !combined.Page.Complete {
		next := cursor
		combined.Page.NextCursor = &next
	}
	result := &JiraDigestChildren{List: combined, ByStatus: map[string]int{}}
	var latest time.Time
	timestampIncomplete := false
	for _, row := range combined.Rows {
		status := renderFieldValue(row.Values["status"])
		if status == "" {
			status = "(empty)"
		}
		result.ByStatus[status]++
		raw := renderFieldValue(row.Values["updated"])
		if parsed, e := parseJiraHistoryTime(raw); e == nil {
			if inDigestPeriod(parsed, period) {
				result.UpdatedInPeriod++
			}
			if parsed.After(latest) {
				latest = parsed
				result.LatestUpdated = raw
			}
		} else if raw != "" {
			timestampIncomplete = true
		}
	}
	complete := combined.Page.Complete && !timestampIncomplete
	warning := digestCapWarning(!combined.Page.Complete, "digest child cap reached")
	if timestampIncomplete {
		warning = "child update timestamps incomplete"
	}
	return result, JiraDigestSource{Complete: complete, Count: len(combined.Rows), Warning: warning}, nil
}

func (r *JiraEpicDigestResult) expandDigestConfluence(ctx context.Context, opts JiraEpicDigestOpts) {
	if opts.Confluence == nil {
		r.Sources["confluence"] = JiraDigestSource{Complete: false, Warning: "Confluence reader unavailable"}
		r.Warnings = append(r.Warnings, "Confluence reader unavailable")
		return
	}
	attempted := 0
	scanned := 0
	complete := true
	for _, ref := range r.Refs {
		if attempted >= opts.ExpandConfluence || scanned >= 50 {
			break
		}
		scanned++
		section, err := opts.Confluence.PageSection(ctx, ref.URL, ConfluencePageSectionOpts{Heading: opts.ConfluenceHeading})
		if err != nil {
			continue
		}
		attempted++
		r.Confluence = append(r.Confluence, JiraDigestConfluenceEvidence{URL: ref.URL, Section: section})
		complete = complete && section.Complete
	}
	if attempted < opts.ExpandConfluence {
		complete = false
		r.Warnings = append(r.Warnings, "fewer resolvable Confluence references than requested")
	}
	r.Sources["confluence"] = JiraDigestSource{Complete: complete, Count: attempted, Warning: digestCapWarning(!complete, "Confluence expansion incomplete")}
}

func digestLastFieldChange(entries []domain.ChangelogEntry, def domain.FieldDef) *JiraFieldLastChange {
	var best *JiraFieldLastChange
	var bestTime time.Time
	for _, e := range entries {
		for _, item := range e.Items {
			if _, ok := selectedHistoryField([]domain.FieldDef{def}, item); !ok {
				continue
			}
			parsed, err := parseJiraHistoryTime(e.Created)
			if err == nil && (best == nil || parsed.After(bestTime) || parsed.Equal(bestTime)) {
				candidate := JiraFieldLastChange{FieldID: def.ID, Field: def.Name, Created: e.Created, HistoryID: e.ID, From: item.From, To: item.To}
				best = &candidate
				bestTime = parsed
			}
		}
	}
	return best
}
func digestStaleness(r *JiraEpicDigestResult) JiraDigestStaleness {
	out := JiraDigestStaleness{Reasons: []string{}}
	if r.StatusField == nil {
		out.Reasons = append(out.Reasons, "status field not configured")
		return out
	}
	if source, ok := r.Sources["status-field"]; ok && !source.Complete {
		out.Reasons = append(out.Reasons, source.Warning)
		return out
	}
	if r.StatusField.LastChange == nil {
		out.Reasons = append(out.Reasons, "status field has no dated change in the selected period")
		return out
	}
	base, err := parseJiraHistoryTime(r.StatusField.LastChange.Created)
	if err != nil {
		out.Reasons = append(out.Reasons, "status field timestamp is unsupported")
		return out
	}
	out.Evaluated = true
	out.StatusFieldUpdated = r.StatusField.LastChange.Created
	latest := base
	if r.Children != nil {
		for _, row := range r.Children.List.Rows {
			raw := renderFieldValue(row.Values["updated"])
			if t, e := parseJiraHistoryTime(raw); e == nil && inDigestPeriod(t, r.Period) && t.After(base) {
				out.NewerChildUpdates++
				if t.After(latest) {
					latest = t
					out.LatestEvidenceAt = raw
				}
			}
		}
	}
	for _, c := range r.Comments {
		if t, e := parseJiraHistoryTime(c.Created); e == nil && inDigestPeriod(t, r.Period) && t.After(base) {
			out.NewerComments++
			if t.After(latest) {
				latest = t
				out.LatestEvidenceAt = c.Created
			}
		}
	}
	out.Stale = out.NewerChildUpdates > 0 || out.NewerComments > 0
	evidenceIncomplete := false
	for _, name := range []string{"children", "comments"} {
		if source, ok := r.Sources[name]; ok && !source.Complete {
			evidenceIncomplete = true
		}
	}
	if !out.Stale && evidenceIncomplete {
		out.Evaluated = false
		out.Reasons = append(out.Reasons, "selected child/comment evidence is incomplete")
		return out
	}
	if out.NewerChildUpdates > 0 {
		out.Reasons = append(out.Reasons, fmt.Sprintf("%d child update(s) are newer than the status field", out.NewerChildUpdates))
	}
	if out.NewerComments > 0 {
		out.Reasons = append(out.Reasons, fmt.Sprintf("%d comment(s) are newer than the status field", out.NewerComments))
	}
	if !out.Stale {
		out.Reasons = append(out.Reasons, "no selected child/comment evidence is newer than the status field")
	}
	return out
}

func digestBoundedText(value string) (string, bool) {
	if len(value) <= jiraDigestTextCap {
		return value, false
	}
	end := jiraDigestTextCap
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}
func digestLimit(value, def int) int {
	if value <= 0 {
		return def
	}
	return min(value, def)
}
func digestTruncationWarning(v bool) string {
	if v {
		return "digest text cap reached"
	}
	return ""
}
func digestCapWarning(v bool, s string) string {
	if v {
		return s
	}
	return ""
}
func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}
func tailHistory(v []domain.ChangelogEntry, n int) ([]domain.ChangelogEntry, bool) {
	truncated := false
	if len(v) <= n {
		v = append([]domain.ChangelogEntry(nil), v...)
	} else {
		v = append([]domain.ChangelogEntry(nil), v[len(v)-n:]...)
		truncated = true
	}
	for i := range v {
		v[i].Items = append([]domain.ChangelogItem(nil), v[i].Items...)
		for j := range v[i].Items {
			var clipped bool
			v[i].Items[j].From, clipped = digestBoundedText(v[i].Items[j].From)
			truncated = truncated || clipped
			v[i].Items[j].To, clipped = digestBoundedText(v[i].Items[j].To)
			truncated = truncated || clipped
		}
	}
	return v, truncated
}
func tailComments(v []domain.Comment, n int) ([]domain.Comment, bool) {
	truncated := false
	if len(v) <= n {
		v = append([]domain.Comment(nil), v...)
	} else {
		v = append([]domain.Comment(nil), v[len(v)-n:]...)
		truncated = true
	}
	for i := range v {
		var clipped bool
		v[i].Body, clipped = digestBoundedText(v[i].Body)
		truncated = truncated || clipped
		v[i].BodyStorage = ""
	}
	return v, truncated
}
func filterDigestComments(v []domain.Comment, period JiraDigestPeriod) ([]domain.Comment, bool) {
	if period.Since == "" {
		return v, false
	}
	out := make([]domain.Comment, 0, len(v))
	incomplete := false
	for _, comment := range v {
		parsed, err := parseJiraHistoryTime(comment.Created)
		if err != nil {
			incomplete = true
			continue
		}
		if inDigestPeriod(parsed, period) {
			out = append(out, comment)
		}
	}
	return out, incomplete
}
func inDigestPeriod(t time.Time, p JiraDigestPeriod) bool {
	if p.Since == "" {
		return true
	}
	start, _ := parseJiraHistoryBoundary(p.Since, false)
	end, _ := parseJiraHistoryBoundary(p.Until, true)
	return !t.Before(start.time) && t.Before(end.time)
}

func JiraEpicDigestMarkdown(r *JiraEpicDigestResult) string {
	if r == nil {
		return ""
	}
	rows := [][]string{}
	keys := make([]string, 0, len(r.Sources))
	for k := range r.Sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := r.Sources[k]
		rows = append(rows, []string{k, fmt.Sprint(s.Complete), fmt.Sprint(s.Count), s.Warning})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\nStatus: %s\n\n", r.Epic.Key, r.Epic.Summary, r.Epic.Status)
	b.WriteString(MarkdownTable([]string{"Source", "Complete", "Count", "Warning"}, rows))
	if r.StatusField != nil {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", r.StatusField.Name, r.StatusField.Value)
	}
	if r.Children != nil {
		statusRows := [][]string{}
		names := make([]string, 0, len(r.Children.ByStatus))
		for n := range r.Children.ByStatus {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			statusRows = append(statusRows, []string{n, fmt.Sprint(r.Children.ByStatus[n])})
		}
		b.WriteString("\n## Children by status\n\n" + MarkdownTable([]string{"Status", "Count"}, statusRows))
	}
	return b.String()
}
