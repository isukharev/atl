package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

// JiraIssueRefsViewSchemaVersion is the released bounded MCP projection.
const JiraIssueRefsViewSchemaVersion = 1

const (
	jiraIssueRefsWarningSelectionLimit       = "issue selection stopped at the configured limit"
	jiraIssueRefsWarningPaginationNoProgress = "issue selection pagination made no forward progress"
	jiraIssueRefsWarningPaginationRepeated   = "issue selection pagination repeated its cursor"
	jiraIssueRefsWarningSourceTextCap        = "source text cap reached"
	jiraIssueRefsWarningCommentsPartial      = "complete comment collection unavailable; embedded comments may be partial"
	jiraIssueRefsWarningFieldAbsent          = "requested field absent from issue snapshot"
)

// JiraIssueRefsResult is the evaluator-owned public wire contract emitted by
// `atl jira issue refs -o json`. It deliberately duplicates the released JSON
// shape instead of importing product application types.
type JiraIssueRefsResult struct {
	Key       string                 `json:"key,omitempty"`
	JQL       string                 `json:"jql,omitempty"`
	Count     int                    `json:"count"`
	Complete  bool                   `json:"complete"`
	Truncated bool                   `json:"truncated,omitempty"`
	Selection JiraIssueRefsSelection `json:"selection"`
	Summary   JiraIssueRefsSummary   `json:"summary"`
	Warnings  []string               `json:"warnings,omitempty"`
	Issues    []JiraIssueRefsIssue   `json:"issues"`
}

// JiraIssueRefsSelection qualifies the issue selection independently from the
// evidence collected for each issue.
type JiraIssueRefsSelection struct {
	Mode      string `json:"mode"`
	Count     int    `json:"count"`
	Limit     int    `json:"limit,omitempty"`
	Complete  bool   `json:"complete"`
	Truncated bool   `json:"truncated,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

// JiraIssueRefsSummary contains the released aggregate reconciliation facts.
type JiraIssueRefsSummary struct {
	IssueCount                  int            `json:"issue_count"`
	CompleteIssueCount          int            `json:"complete_issue_count"`
	IncompleteIssueCount        int            `json:"incomplete_issue_count"`
	ReferenceCount              int            `json:"reference_count"`
	ReferenceKindCounts         map[string]int `json:"reference_kind_counts"`
	SourceCount                 int            `json:"source_count"`
	SourceValueCounts           map[string]int `json:"source_value_counts"`
	CompleteSourceCount         int            `json:"complete_source_count"`
	IncompleteSourceCount       int            `json:"incomplete_source_count"`
	TruncatedSourceCount        int            `json:"truncated_source_count"`
	CountMatchesIssues          bool           `json:"count_matches_issues"`
	SelectionCountMatchesIssues bool           `json:"selection_count_matches_issues"`
	ReferenceCountMatchesKinds  bool           `json:"reference_count_matches_kinds"`
	IssueSummariesReconciled    bool           `json:"issue_summaries_reconciled"`
	CompleteMatchesInputs       bool           `json:"complete_matches_inputs"`
	TruncatedMatchesInputs      bool           `json:"truncated_matches_inputs"`
}

// JiraIssueRefsSource qualifies one inspected reference source.
type JiraIssueRefsSource struct {
	Complete      bool   `json:"complete"`
	Count         int    `json:"count"`
	TextTruncated bool   `json:"text_truncated,omitempty"`
	Warning       string `json:"warning,omitempty"`
}

// JiraIssueReferenceSummary reconciles one issue's source and reference facts.
type JiraIssueReferenceSummary struct {
	ReferenceCount             int            `json:"reference_count"`
	ReferenceKindCounts        map[string]int `json:"reference_kind_counts"`
	SourceCount                int            `json:"source_count"`
	SourceValueCounts          map[string]int `json:"source_value_counts"`
	CompleteSourceCount        int            `json:"complete_source_count"`
	IncompleteSourceCount      int            `json:"incomplete_source_count"`
	TruncatedSourceCount       int            `json:"truncated_source_count"`
	ReferenceCountMatchesKinds bool           `json:"reference_count_matches_kinds"`
	CompleteMatchesSources     bool           `json:"complete_matches_sources"`
	TruncatedMatchesSources    bool           `json:"truncated_matches_sources"`
}

// JiraIssueRefsIssue is one full CLI row, including narrative and raw refs.
type JiraIssueRefsIssue struct {
	Key              string                         `json:"key"`
	Summary          string                         `json:"summary,omitempty"`
	Type             string                         `json:"type,omitempty"`
	Complete         bool                           `json:"complete"`
	Truncated        bool                           `json:"truncated,omitempty"`
	Sources          map[string]JiraIssueRefsSource `json:"sources"`
	ReferenceSummary JiraIssueReferenceSummary      `json:"reference_summary"`
	Warnings         []string                       `json:"warnings,omitempty"`
	Refs             []JiraIssueRef                 `json:"refs"`
}

// JiraIssueRef is one raw URL and its closed reference kind.
type JiraIssueRef struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

// JiraIssueRefsView is the evaluator-owned schema-v1 MCP projection. It has no
// raw URLs, query echo, summary, type, or other issue narrative.
type JiraIssueRefsView struct {
	SchemaVersion int                      `json:"schema_version"`
	Count         int                      `json:"count"`
	Complete      bool                     `json:"complete"`
	Truncated     bool                     `json:"truncated,omitempty"`
	Selection     JiraIssueRefsSelection   `json:"selection"`
	Summary       JiraIssueRefsSummary     `json:"summary"`
	Warnings      []string                 `json:"warnings,omitempty"`
	Issues        []JiraIssueRefsIssueView `json:"issues"`
}

// JiraIssueRefsIssueView is one narrative-free MCP evidence row.
type JiraIssueRefsIssueView struct {
	Key              string                         `json:"key"`
	Complete         bool                           `json:"complete"`
	Truncated        bool                           `json:"truncated,omitempty"`
	Sources          map[string]JiraIssueRefsSource `json:"sources"`
	ReferenceSummary JiraIssueReferenceSummary      `json:"reference_summary"`
}

// DecodeJiraIssueRefsResult strictly decodes and reconciles one CLI result.
func DecodeJiraIssueRefsResult(r io.Reader) (JiraIssueRefsResult, error) {
	var result JiraIssueRefsResult
	if err := decodeJiraReferenceWire(r, &result, false); err != nil {
		return JiraIssueRefsResult{}, err
	}
	if err := result.validate(); err != nil {
		return JiraIssueRefsResult{}, fmt.Errorf("validate Jira issue refs result: %w", err)
	}
	return result, nil
}

// DecodeJiraIssueRefsView strictly decodes and reconciles one MCP view.
func DecodeJiraIssueRefsView(r io.Reader) (JiraIssueRefsView, error) {
	var view JiraIssueRefsView
	if err := decodeJiraReferenceWire(r, &view, true); err != nil {
		return JiraIssueRefsView{}, err
	}
	if err := view.validate(); err != nil {
		return JiraIssueRefsView{}, fmt.Errorf("validate Jira issue refs view: %w", err)
	}
	return view, nil
}

func decodeJiraReferenceWire(r io.Reader, dst any, view bool) error {
	limited := &io.LimitedReader{R: r, N: maxContractBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read Jira issue reference wire: %w", err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("issue reference wire exceeds %d bytes", maxContractBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return fmt.Errorf("decode Jira issue reference wire: %w", err)
	}
	if err := validateJiraReferenceMemberSets(data, view); err != nil {
		return fmt.Errorf("decode Jira issue reference wire: %w", err)
	}
	if err := decodeStrict(bytes.NewReader(data), dst); err != nil {
		return fmt.Errorf("decode Jira issue reference wire: %w", err)
	}
	return nil
}

func validateJiraReferenceMemberSets(data []byte, view bool) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	required := []string{"count", "complete", "selection", "summary", "issues"}
	optional := []string{"truncated", "warnings"}
	if view {
		required = append(required, "schema_version")
	} else {
		optional = append(optional, "key", "jql")
	}
	if err := requireJiraReferenceMembers(root, "result", required, optional); err != nil {
		return err
	}
	if err := rejectJSONNulls(data, "result"); err != nil {
		return err
	}
	if err := requireNonzeroOptionalJiraReferenceMembers(root, "result", optional); err != nil {
		return err
	}

	var selection map[string]json.RawMessage
	if err := json.Unmarshal(root["selection"], &selection); err != nil {
		return fmt.Errorf("selection: %w", err)
	}
	if err := requireJiraReferenceMembers(selection, "selection", []string{"mode", "count", "complete"}, []string{"limit", "truncated", "warning"}); err != nil {
		return err
	}
	if err := requireNonzeroOptionalJiraReferenceMembers(selection, "selection", []string{"limit", "truncated", "warning"}); err != nil {
		return err
	}
	if err := validateJiraReferenceSummaryMembers(root["summary"], "summary"); err != nil {
		return err
	}
	var issues []json.RawMessage
	if err := json.Unmarshal(root["issues"], &issues); err != nil {
		return fmt.Errorf("issues: %w", err)
	}
	for index, raw := range issues {
		var issue map[string]json.RawMessage
		if err := json.Unmarshal(raw, &issue); err != nil {
			return fmt.Errorf("issue[%d]: %w", index, err)
		}
		requiredIssue := []string{"key", "complete", "sources", "reference_summary"}
		optionalIssue := []string{"truncated"}
		if !view {
			requiredIssue = append(requiredIssue, "refs")
			optionalIssue = append(optionalIssue, "summary", "type", "warnings")
		}
		if err := requireJiraReferenceMembers(issue, fmt.Sprintf("issue[%d]", index), requiredIssue, optionalIssue); err != nil {
			return err
		}
		if err := requireNonzeroOptionalJiraReferenceMembers(issue, fmt.Sprintf("issue[%d]", index), optionalIssue); err != nil {
			return err
		}
		var sources map[string]json.RawMessage
		if err := json.Unmarshal(issue["sources"], &sources); err != nil {
			return fmt.Errorf("issue[%d].sources: %w", index, err)
		}
		for name, sourceRaw := range sources {
			var source map[string]json.RawMessage
			if err := json.Unmarshal(sourceRaw, &source); err != nil {
				return fmt.Errorf("issue[%d].sources[%q]: %w", index, name, err)
			}
			if err := requireJiraReferenceMembers(source, fmt.Sprintf("issue[%d].sources[%q]", index, name), []string{"complete", "count"}, []string{"text_truncated", "warning"}); err != nil {
				return err
			}
			if err := requireNonzeroOptionalJiraReferenceMembers(source, fmt.Sprintf("issue[%d].sources[%q]", index, name), []string{"text_truncated", "warning"}); err != nil {
				return err
			}
		}
		if err := validateJiraReferenceSummaryMembers(issue["reference_summary"], fmt.Sprintf("issue[%d].reference_summary", index)); err != nil {
			return err
		}
		if !view {
			var refs []map[string]json.RawMessage
			if err := json.Unmarshal(issue["refs"], &refs); err != nil {
				return fmt.Errorf("issue[%d].refs: %w", index, err)
			}
			for refIndex, ref := range refs {
				if err := requireExactJSONMembers(ref, fmt.Sprintf("issue[%d].refs[%d]", index, refIndex), []string{"url", "kind"}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateJiraReferenceSummaryMembers(raw json.RawMessage, owner string) error {
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(raw, &summary); err != nil {
		return fmt.Errorf("%s: %w", owner, err)
	}
	members := []string{
		"reference_count", "reference_kind_counts", "source_count", "source_value_counts",
		"complete_source_count", "incomplete_source_count", "truncated_source_count",
		"reference_count_matches_kinds",
	}
	if owner == "summary" {
		members = append(members, "issue_count", "complete_issue_count", "incomplete_issue_count",
			"count_matches_issues", "selection_count_matches_issues", "issue_summaries_reconciled",
			"complete_matches_inputs", "truncated_matches_inputs")
	} else {
		members = append(members, "complete_matches_sources", "truncated_matches_sources")
	}
	return requireExactJSONMembers(summary, owner, members)
}

func requireJiraReferenceMembers(document map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if _, ok := document[name]; !ok {
			return fmt.Errorf("%s is missing required member %q", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name := range document {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func requireNonzeroOptionalJiraReferenceMembers(document map[string]json.RawMessage, owner string, optional []string) error {
	for _, name := range optional {
		raw, present := document[name]
		if !present {
			continue
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("%s.%s: %w", owner, name, err)
		}
		empty := false
		switch typed := value.(type) {
		case bool:
			empty = !typed
		case string:
			empty = typed == ""
		case json.Number:
			empty = typed.String() == "0"
		case []any:
			empty = len(typed) == 0
		case map[string]any:
			empty = len(typed) == 0
		}
		if empty {
			return fmt.Errorf("%s optional member %q must be omitted when empty", owner, name)
		}
	}
	return nil
}

func rejectJSONNulls(data []byte, owner string) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var visit func(any) bool
	visit = func(current any) bool {
		if current == nil {
			return true
		}
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				if visit(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if visit(item) {
					return true
				}
			}
		}
		return false
	}
	if visit(value) {
		return fmt.Errorf("%s contains null where absence and null have different wire semantics", owner)
	}
	return nil
}

func (r JiraIssueRefsResult) validate() error {
	if err := validateJiraIssueRefsCommon(r.Count, r.Complete, r.Truncated, r.Selection, r.Summary, r.Warnings, referenceIssuesFromResult(r.Issues)); err != nil {
		return err
	}
	switch r.Selection.Mode {
	case "key":
		if strings.TrimSpace(r.Key) == "" || r.JQL != "" || r.Selection.Limit != 0 || r.Count != 1 {
			return fmt.Errorf("key selection echo is not reconciled")
		}
		if len(r.Issues) != 1 || r.Issues[0].Key != r.Key {
			return fmt.Errorf("key selection does not match the emitted issue")
		}
	case "jql":
		if strings.TrimSpace(r.JQL) == "" || r.Key != "" || r.Selection.Limit < 1 || r.Count > r.Selection.Limit {
			return fmt.Errorf("JQL selection echo is not reconciled")
		}
	default:
		return fmt.Errorf("selection mode %q is unsupported", r.Selection.Mode)
	}
	for _, issue := range r.Issues {
		expectedIssueWarnings := []string{}
		sourceNames := make([]string, 0, len(issue.Sources))
		for name := range issue.Sources {
			sourceNames = append(sourceNames, name)
		}
		slices.Sort(sourceNames)
		for _, name := range sourceNames {
			source := issue.Sources[name]
			if source.Warning != "" {
				expectedIssueWarnings = append(expectedIssueWarnings, "source "+name+": "+source.Warning)
			}
		}
		if !slices.Equal(issue.Warnings, expectedIssueWarnings) {
			return fmt.Errorf("issue %q warnings do not match its incomplete sources", issue.Key)
		}
		counts := map[string]int{}
		seenURLs := map[string]struct{}{}
		previousURL := ""
		for _, ref := range issue.Refs {
			parsed, err := url.Parse(ref.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || !jiraReferenceKind(ref.Kind) {
				return fmt.Errorf("issue %q contains an invalid reference", issue.Key)
			}
			if _, duplicate := seenURLs[ref.URL]; duplicate {
				return fmt.Errorf("issue %q contains duplicate references", issue.Key)
			}
			if previousURL != "" && ref.URL < previousURL {
				return fmt.Errorf("issue %q references are not sorted", issue.Key)
			}
			seenURLs[ref.URL] = struct{}{}
			previousURL = ref.URL
			counts[ref.Kind]++
		}
		if issue.ReferenceSummary.ReferenceCount != len(issue.Refs) || !reflect.DeepEqual(counts, issue.ReferenceSummary.ReferenceKindCounts) {
			return fmt.Errorf("issue %q raw references do not match its summary", issue.Key)
		}
	}
	return nil
}

func (v JiraIssueRefsView) validate() error {
	if v.SchemaVersion != JiraIssueRefsViewSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", v.SchemaVersion)
	}
	if v.Selection.Mode != "key" && v.Selection.Mode != "jql" {
		return fmt.Errorf("selection mode %q is unsupported", v.Selection.Mode)
	}
	if v.Selection.Mode == "key" && (v.Selection.Limit != 0 || v.Count != 1) {
		return fmt.Errorf("key selection is not reconciled")
	}
	if v.Selection.Mode == "jql" && (v.Selection.Limit < 1 || v.Count > v.Selection.Limit) {
		return fmt.Errorf("JQL selection is not reconciled")
	}
	return validateJiraIssueRefsCommon(v.Count, v.Complete, v.Truncated, v.Selection, v.Summary, v.Warnings, referenceIssuesFromView(v.Issues))
}

type jiraReferenceIssueFacts struct {
	key       string
	complete  bool
	truncated bool
	sources   map[string]JiraIssueRefsSource
	summary   JiraIssueReferenceSummary
}

func referenceIssuesFromResult(issues []JiraIssueRefsIssue) []jiraReferenceIssueFacts {
	out := make([]jiraReferenceIssueFacts, 0, len(issues))
	for _, issue := range issues {
		out = append(out, jiraReferenceIssueFacts{issue.Key, issue.Complete, issue.Truncated, issue.Sources, issue.ReferenceSummary})
	}
	return out
}

func referenceIssuesFromView(issues []JiraIssueRefsIssueView) []jiraReferenceIssueFacts {
	out := make([]jiraReferenceIssueFacts, 0, len(issues))
	for _, issue := range issues {
		out = append(out, jiraReferenceIssueFacts{issue.Key, issue.Complete, issue.Truncated, issue.Sources, issue.ReferenceSummary})
	}
	return out
}

func validateJiraIssueRefsCommon(count int, complete, truncated bool, selection JiraIssueRefsSelection, summary JiraIssueRefsSummary, warnings []string, issues []jiraReferenceIssueFacts) error {
	if count < 0 || count != len(issues) || selection.Count != count || summary.IssueCount != count ||
		!summary.CountMatchesIssues || !summary.SelectionCountMatchesIssues || !summary.ReferenceCountMatchesKinds ||
		!summary.IssueSummariesReconciled || !summary.CompleteMatchesInputs || !summary.TruncatedMatchesInputs {
		return fmt.Errorf("top-level counts and reconciliation flags disagree")
	}
	if !validJiraReferenceSelectionWarning(selection) {
		return fmt.Errorf("selection warning and completeness disagree")
	}

	referenceKinds := map[string]int{}
	sourceValues := map[string]int{}
	completeIssues, incompleteIssues := 0, 0
	completeSources, incompleteSources, truncatedSources := 0, 0, 0
	references, sources := 0, 0
	seenKeys := map[string]struct{}{}
	allIssuesComplete := true
	anyIssueTruncated := false
	for _, issue := range issues {
		if strings.TrimSpace(issue.key) == "" || strings.TrimSpace(issue.key) != issue.key {
			return fmt.Errorf("issue key is empty or not whitespace-normalized")
		}
		if _, duplicate := seenKeys[issue.key]; duplicate {
			return fmt.Errorf("issue keys are not unique")
		}
		seenKeys[issue.key] = struct{}{}
		summary := issue.summary
		if summary.ReferenceCount < 0 || summary.SourceCount < 0 || !summary.ReferenceCountMatchesKinds ||
			!summary.CompleteMatchesSources || !summary.TruncatedMatchesSources ||
			summary.ReferenceCount != nonnegativeJiraReferenceCounts(summary.ReferenceKindCounts) ||
			summary.SourceCount != len(issue.sources) {
			return fmt.Errorf("issue %q reference summary is not reconciled", issue.key)
		}
		issueCompleteSources, issueIncompleteSources, issueTruncatedSources := 0, 0, 0
		for name, source := range issue.sources {
			countValue, exists := summary.SourceValueCounts[name]
			if !validJiraReferenceSourceName(name) || source.Count < 0 || !exists || countValue != source.Count ||
				!validJiraReferenceSourceWarning(name, source) {
				return fmt.Errorf("issue %q source %q is not reconciled", issue.key, name)
			}
			sources++
			sourceValues[name] += source.Count
			if source.Complete {
				completeSources++
				issueCompleteSources++
			} else {
				incompleteSources++
				issueIncompleteSources++
			}
			if source.TextTruncated {
				truncatedSources++
				issueTruncatedSources++
			}
		}
		if len(summary.SourceValueCounts) != len(issue.sources) || summary.CompleteSourceCount != issueCompleteSources ||
			summary.IncompleteSourceCount != issueIncompleteSources || summary.TruncatedSourceCount != issueTruncatedSources ||
			issue.complete != (issueIncompleteSources == 0) || issue.truncated != (issueTruncatedSources > 0) {
			return fmt.Errorf("issue %q source qualification is not reconciled", issue.key)
		}
		references += summary.ReferenceCount
		for kind, value := range summary.ReferenceKindCounts {
			if !jiraReferenceKind(kind) || value < 0 {
				return fmt.Errorf("issue %q reference kind counts are invalid", issue.key)
			}
			referenceKinds[kind] += value
		}
		if issue.complete {
			completeIssues++
		} else {
			incompleteIssues++
			allIssuesComplete = false
		}
		anyIssueTruncated = anyIssueTruncated || issue.truncated
	}
	if summary.CompleteIssueCount != completeIssues || summary.IncompleteIssueCount != incompleteIssues ||
		summary.ReferenceCount != references || !reflect.DeepEqual(summary.ReferenceKindCounts, referenceKinds) ||
		summary.SourceCount != sources || !reflect.DeepEqual(summary.SourceValueCounts, sourceValues) ||
		summary.CompleteSourceCount != completeSources || summary.IncompleteSourceCount != incompleteSources ||
		summary.TruncatedSourceCount != truncatedSources || complete != (selection.Complete && allIssuesComplete) ||
		truncated != (selection.Truncated || anyIssueTruncated) {
		return fmt.Errorf("aggregate counts and completeness are not reconciled")
	}
	expectedWarnings := []string{}
	if selection.Warning != "" {
		expectedWarnings = append(expectedWarnings, selection.Warning)
	}
	if incompleteIssues > 0 {
		expectedWarnings = append(expectedWarnings, fmt.Sprintf("%d issue reference source set(s) incomplete", incompleteIssues))
	}
	if !slices.Equal(warnings, expectedWarnings) {
		return fmt.Errorf("top-level warnings are not reconciled")
	}
	return nil
}

func validJiraReferenceSelectionWarning(selection JiraIssueRefsSelection) bool {
	switch selection.Warning {
	case "":
		return selection.Complete && !selection.Truncated
	case jiraIssueRefsWarningSelectionLimit:
		return !selection.Complete && selection.Truncated
	case jiraIssueRefsWarningPaginationNoProgress, jiraIssueRefsWarningPaginationRepeated:
		return !selection.Complete && !selection.Truncated
	default:
		return false
	}
}

func validJiraReferenceSourceWarning(name string, source JiraIssueRefsSource) bool {
	if source.Complete {
		return source.Warning == "" && !source.TextTruncated
	}
	switch source.Warning {
	case jiraIssueRefsWarningSourceTextCap:
		return source.TextTruncated
	case jiraIssueRefsWarningCommentsPartial:
		return name == "comments" && !source.TextTruncated
	case jiraIssueRefsWarningCommentsPartial + "; " + jiraIssueRefsWarningSourceTextCap:
		return name == "comments" && source.TextTruncated
	case jiraIssueRefsWarningFieldAbsent:
		return strings.HasPrefix(name, "field.") && !source.TextTruncated
	default:
		return false
	}
}

func validJiraReferenceSourceName(name string) bool {
	if name == "comments" || name == "description" {
		return true
	}
	field := strings.TrimPrefix(name, "field.")
	return field != name && field != "" && strings.TrimSpace(field) == field
}

func jiraReferenceKind(kind string) bool {
	switch kind {
	case "chat", "design", "doc", "jira", "link":
		return true
	default:
		return false
	}
}

func nonnegativeJiraReferenceCounts(values map[string]int) int {
	if values == nil {
		return -1
	}
	total := 0
	for _, value := range values {
		if value < 0 {
			return -1
		}
		total += value
	}
	return total
}
