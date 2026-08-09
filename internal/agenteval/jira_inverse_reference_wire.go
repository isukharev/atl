package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

const (
	JiraInverseReferenceViewSchemaVersion = 1
	jiraInverseReferenceMaxIssues         = 5000
	jiraInverseReferenceMaxRequests       = 25000
	jiraInverseReferenceMaxResponseBytes  = int64(256 << 20)
)

var (
	jiraInverseReferenceOpaqueID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	jiraInverseReferenceIssueKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)
	jiraInverseReferenceFieldID  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
)

// JiraInverseReferenceView is the evaluator-owned schema-v1 projection
// emitted by `atl jira issue reference search -o json`. It intentionally has
// no input target, JQL, URL, source text, user identity, application name, or
// backend coordinate.
type JiraInverseReferenceView struct {
	SchemaVersion     int                                `json:"schema_version"`
	Target            JiraInverseReferenceTargetView     `json:"target"`
	Mode              string                             `json:"mode"`
	Sources           []string                           `json:"sources"`
	EffectiveFieldIDs []string                           `json:"effective_field_ids"`
	TargetResolution  JiraInverseReferencePhaseView      `json:"target_resolution"`
	Selection         JiraInverseReferencePhaseView      `json:"selection"`
	Verification      JiraInverseReferencePhaseView      `json:"verification"`
	Counts            JiraInverseReferenceCountsView     `json:"counts"`
	SourceCounts      []JiraInverseReferenceSourceCounts `json:"source_counts"`
	Matches           []JiraInverseReferenceMatchView    `json:"matches"`
	Frontier          JiraInverseReferenceFrontierView   `json:"frontier"`
	Reconciliation    JiraInverseReferenceReconciliation `json:"reconciliation"`
	Usage             JiraInverseReferenceUsageView      `json:"usage"`
	Complete          bool                               `json:"complete"`
	AbsenceProven     bool                               `json:"absence_proven"`
}

type JiraInverseReferenceTargetView struct {
	Kind     string `json:"kind"`
	OpaqueID string `json:"opaque_id"`
}

type JiraInverseReferencePhaseView struct {
	Complete bool   `json:"complete"`
	Reason   string `json:"reason,omitempty"`
}

type JiraInverseReferenceCountsView struct {
	SelectedIssues  int `json:"selected_issues"`
	CandidateIssues int `json:"candidate_issues"`
	ScannedIssues   int `json:"scanned_issues"`
	VerifiedIssues  int `json:"verified_issues"`
	MatchedIssues   int `json:"matched_issues"`
	Matches         int `json:"matches"`
}

type JiraInverseReferenceSourceCounts struct {
	Source      string                            `json:"source"`
	Complete    int                               `json:"complete"`
	Empty       int                               `json:"empty"`
	Partial     int                               `json:"partial"`
	Forbidden   int                               `json:"forbidden"`
	Unsupported int                               `json:"unsupported"`
	Skipped     int                               `json:"skipped"`
	Total       int                               `json:"total"`
	Reconciled  bool                              `json:"reconciled"`
	Reasons     []JiraInverseReferenceReasonCount `json:"reasons"`
}

type JiraInverseReferenceReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type JiraInverseReferenceMatchView struct {
	IssueKey         string `json:"issue_key"`
	Relation         string `json:"relation"`
	Direction        string `json:"direction"`
	Source           string `json:"source"`
	TechnicalFieldID string `json:"technical_field_id,omitempty"`
	Stability        string `json:"stability"`
	Confidence       string `json:"confidence"`
	Complete         bool   `json:"complete"`
}

type JiraInverseReferenceFrontierView struct {
	Phase          string `json:"phase"`
	Pass           int    `json:"pass,omitempty"`
	PageStart      int    `json:"page_start,omitempty"`
	VerifiedIssues int    `json:"verified_issues"`
	Source         string `json:"source,omitempty"`
	SourceReason   string `json:"source_reason,omitempty"`
}

type JiraInverseReferenceReconciliation struct {
	Counts  bool `json:"counts"`
	Sources bool `json:"sources"`
	Matches bool `json:"matches"`
	Usage   bool `json:"usage"`
}

type JiraInverseReferenceUsageView struct {
	MaxIssues        int   `json:"max_issues"`
	MaxRequests      int   `json:"max_requests"`
	Requests         int   `json:"requests"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
	ResponseBytes    int64 `json:"response_bytes"`
	Reconciled       bool  `json:"reconciled"`
}

// DecodeJiraInverseReferenceView strictly decodes and reconciles one bounded
// released inverse-reference result without importing product application or
// domain types into the evaluator module.
func DecodeJiraInverseReferenceView(r io.Reader) (JiraInverseReferenceView, error) {
	limited := &io.LimitedReader{R: r, N: maxContractBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return JiraInverseReferenceView{}, fmt.Errorf("read Jira inverse-reference wire: %w", err)
	}
	if limited.N <= 0 {
		return JiraInverseReferenceView{}, fmt.Errorf("jira inverse-reference wire exceeds %d bytes", maxContractBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return JiraInverseReferenceView{}, fmt.Errorf("decode Jira inverse-reference wire: %w", err)
	}
	if err := validateJiraInverseReferenceMembers(data); err != nil {
		return JiraInverseReferenceView{}, fmt.Errorf("decode Jira inverse-reference wire: %w", err)
	}
	var view JiraInverseReferenceView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return JiraInverseReferenceView{}, fmt.Errorf("decode Jira inverse-reference wire: %w", err)
	}
	if err := view.validate(); err != nil {
		return JiraInverseReferenceView{}, fmt.Errorf("validate Jira inverse-reference wire: %w", err)
	}
	return view, nil
}

func validateJiraInverseReferenceMembers(data []byte) error {
	root, err := jiraInverseReferenceObject(data, "result")
	if err != nil {
		return err
	}
	if err := jiraInverseReferenceMembers(root, "result", []string{
		"schema_version", "target", "mode", "sources", "effective_field_ids", "target_resolution",
		"selection", "verification", "counts", "source_counts", "matches", "frontier",
		"reconciliation", "usage", "complete", "absence_proven",
	}, nil); err != nil {
		return err
	}
	if err := rejectJSONNulls(data, "result"); err != nil {
		return err
	}
	target, err := jiraInverseReferenceNestedObject(root["target"], "result.target")
	if err != nil {
		return err
	}
	if err := jiraInverseReferenceMembers(target, "result.target", []string{"kind", "opaque_id"}, nil); err != nil {
		return err
	}
	for _, name := range []string{"target_resolution", "selection", "verification"} {
		phase, err := jiraInverseReferenceNestedObject(root[name], "result."+name)
		if err != nil {
			return err
		}
		if err := jiraInverseReferenceMembers(phase, "result."+name, []string{"complete"}, []string{"reason"}); err != nil {
			return err
		}
	}
	counts, err := jiraInverseReferenceNestedObject(root["counts"], "result.counts")
	if err != nil {
		return err
	}
	if err := jiraInverseReferenceMembers(counts, "result.counts", []string{
		"selected_issues", "candidate_issues", "scanned_issues", "verified_issues", "matched_issues", "matches",
	}, nil); err != nil {
		return err
	}
	if err := jiraInverseReferenceArray(root["source_counts"], "result.source_counts", func(item map[string]json.RawMessage, owner string) error {
		if err := jiraInverseReferenceMembers(item, owner, []string{
			"source", "complete", "empty", "partial", "forbidden", "unsupported", "skipped", "total", "reconciled", "reasons",
		}, nil); err != nil {
			return err
		}
		return jiraInverseReferenceArray(item["reasons"], owner+".reasons", func(reason map[string]json.RawMessage, reasonOwner string) error {
			return jiraInverseReferenceMembers(reason, reasonOwner, []string{"reason", "count"}, nil)
		})
	}); err != nil {
		return err
	}
	if err := jiraInverseReferenceArray(root["matches"], "result.matches", func(item map[string]json.RawMessage, owner string) error {
		return jiraInverseReferenceMembers(item, owner, []string{
			"issue_key", "relation", "direction", "source", "stability", "confidence", "complete",
		}, []string{"technical_field_id"})
	}); err != nil {
		return err
	}
	frontier, err := jiraInverseReferenceNestedObject(root["frontier"], "result.frontier")
	if err != nil {
		return err
	}
	if err := jiraInverseReferenceMembers(frontier, "result.frontier", []string{"phase", "verified_issues"}, []string{"pass", "page_start", "source", "source_reason"}); err != nil {
		return err
	}
	reconciliation, err := jiraInverseReferenceNestedObject(root["reconciliation"], "result.reconciliation")
	if err != nil {
		return err
	}
	if err := jiraInverseReferenceMembers(reconciliation, "result.reconciliation", []string{"counts", "sources", "matches", "usage"}, nil); err != nil {
		return err
	}
	usage, err := jiraInverseReferenceNestedObject(root["usage"], "result.usage")
	if err != nil {
		return err
	}
	return jiraInverseReferenceMembers(usage, "result.usage", []string{
		"max_issues", "max_requests", "requests", "max_response_bytes", "response_bytes", "reconciled",
	}, nil)
}

func jiraInverseReferenceObject(data []byte, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func jiraInverseReferenceNestedObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	return jiraInverseReferenceObject(raw, owner)
}

func jiraInverseReferenceMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range append(append([]string(nil), required...), optional...) {
		allowed[name] = true
	}
	for _, name := range required {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("%s.%s is required", owner, name)
		}
	}
	for name := range object {
		if !allowed[name] {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func jiraInverseReferenceArray(raw json.RawMessage, owner string, validate func(map[string]json.RawMessage, string) error) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s must be a non-null array", owner)
	}
	for index, raw := range values {
		itemOwner := fmt.Sprintf("%s[%d]", owner, index)
		item, err := jiraInverseReferenceNestedObject(raw, itemOwner)
		if err != nil {
			return err
		}
		if err := validate(item, itemOwner); err != nil {
			return err
		}
	}
	return nil
}

func (v JiraInverseReferenceView) validate() error {
	if v.SchemaVersion != JiraInverseReferenceViewSchemaVersion ||
		!jiraInverseReferenceOneOf(v.Target.Kind, "confluence_page", "gitlab_project") ||
		!jiraInverseReferenceOpaqueID.MatchString(v.Target.OpaqueID) ||
		!jiraInverseReferenceOneOf(v.Mode, "exhaustive", "fast") {
		return fmt.Errorf("schema, target, or mode is outside the closed contract")
	}
	if len(v.Sources) == 0 || len(v.Sources) > 7 || !slices.IsSorted(v.Sources) || slices.Contains(v.Sources, "") {
		return fmt.Errorf("sources are not one sorted non-empty closed set")
	}
	for index, source := range v.Sources {
		if !validJiraInverseReferenceSource(source) || index > 0 && source == v.Sources[index-1] {
			return fmt.Errorf("source %q is outside the closed contract", source)
		}
	}
	if !slices.IsSorted(v.EffectiveFieldIDs) {
		return fmt.Errorf("effective_field_ids are not sorted")
	}
	for index, fieldID := range v.EffectiveFieldIDs {
		if !jiraInverseReferenceFieldID.MatchString(fieldID) || index > 0 && fieldID == v.EffectiveFieldIDs[index-1] {
			return fmt.Errorf("effective_field_ids contain an invalid or duplicate member")
		}
	}
	if slices.Contains(v.Sources, "fields") != (len(v.EffectiveFieldIDs) > 0) {
		return fmt.Errorf("fields source does not reconcile with effective_field_ids")
	}
	for name, phase := range map[string]JiraInverseReferencePhaseView{
		"target_resolution": v.TargetResolution, "selection": v.Selection, "verification": v.Verification,
	} {
		if phase.Complete == (phase.Reason != "") || phase.Reason != "" && !validJiraInverseReferencePhaseReason(phase.Reason) {
			return fmt.Errorf("%s completeness and reason do not reconcile", name)
		}
	}
	if !v.TargetResolution.Complete {
		return fmt.Errorf("an emitted result must have a resolved target")
	}
	if v.Usage.MaxIssues <= 0 || v.Usage.MaxIssues > jiraInverseReferenceMaxIssues ||
		v.Usage.MaxRequests <= 0 || v.Usage.MaxRequests > jiraInverseReferenceMaxRequests ||
		v.Usage.MaxResponseBytes <= 0 || v.Usage.MaxResponseBytes > jiraInverseReferenceMaxResponseBytes {
		return fmt.Errorf("usage limits are outside the released bounds")
	}
	counts := v.Counts
	if anyNegative(counts.SelectedIssues, counts.CandidateIssues, counts.ScannedIssues, counts.VerifiedIssues, counts.MatchedIssues, counts.Matches) ||
		counts.CandidateIssues < counts.SelectedIssues || counts.ScannedIssues < counts.SelectedIssues ||
		counts.VerifiedIssues != counts.SelectedIssues || counts.MatchedIssues > counts.VerifiedIssues || counts.Matches != len(v.Matches) {
		return fmt.Errorf("counts do not reconcile")
	}
	if v.Mode == "fast" && v.Selection.Complete {
		return fmt.Errorf("fast selection cannot be complete")
	}
	if v.Mode == "exhaustive" && v.Selection.Complete &&
		(counts.CandidateIssues != counts.SelectedIssues || counts.ScannedIssues != 2*counts.SelectedIssues) {
		return fmt.Errorf("complete exhaustive selection proof does not reconcile")
	}
	if len(v.SourceCounts) != len(v.Sources) {
		return fmt.Errorf("source_counts do not cover sources")
	}
	incompleteSourceOutcomes := 0
	for index, counts := range v.SourceCounts {
		if counts.Source != v.Sources[index] || anyNegative(counts.Complete, counts.Empty, counts.Partial, counts.Forbidden, counts.Unsupported, counts.Skipped, counts.Total) {
			return fmt.Errorf("source_counts[%d] is invalid", index)
		}
		if anyGreaterThan(v.Counts.SelectedIssues, counts.Complete, counts.Empty, counts.Partial, counts.Forbidden, counts.Unsupported, counts.Skipped, counts.Total) {
			return fmt.Errorf("source_counts[%d] exceeds selected issues", index)
		}
		total := counts.Complete + counts.Empty + counts.Partial + counts.Forbidden + counts.Unsupported + counts.Skipped
		if counts.Total != total || counts.Reconciled != (total == v.Counts.SelectedIssues) || !counts.Reconciled {
			return fmt.Errorf("source_counts[%d] does not reconcile", index)
		}
		reasonTotal := 0
		for reasonIndex, reason := range counts.Reasons {
			if !validJiraInverseReferenceSourceReason(reason.Reason) || reason.Count <= 0 || reason.Count > v.Counts.SelectedIssues ||
				reasonIndex > 0 && reason.Reason <= counts.Reasons[reasonIndex-1].Reason {
				return fmt.Errorf("source_counts[%d].reasons is outside the closed contract", index)
			}
			reasonTotal += reason.Count
		}
		if reasonTotal != counts.Partial+counts.Forbidden+counts.Unsupported+counts.Skipped {
			return fmt.Errorf("source_counts[%d].reasons does not reconcile", index)
		}
		incompleteSourceOutcomes += counts.Partial + counts.Forbidden + counts.Unsupported + counts.Skipped
	}
	if v.Verification.Complete != (incompleteSourceOutcomes == 0) {
		return fmt.Errorf("verification completeness does not reconcile with source outcomes")
	}
	matchedKeys := map[string]bool{}
	seenMatches := map[string]bool{}
	for index, match := range v.Matches {
		if !jiraInverseReferenceIssueKey.MatchString(match.IssueKey) ||
			!jiraInverseReferenceOneOf(match.Relation, "structured_remote_link", "development_association", "literal_mention") ||
			match.Direction != "issue_to_target" || !slices.Contains(v.Sources, match.Source) ||
			!jiraInverseReferenceOneOf(match.Stability, "public_api", "experimental_api", "heuristic") ||
			!jiraInverseReferenceOneOf(match.Confidence, "exact", "high") {
			return fmt.Errorf("matches[%d] is outside the closed contract", index)
		}
		if !validJiraInverseReferenceMatchTuple(match) {
			return fmt.Errorf("matches[%d] relation tuple is outside the closed contract", index)
		}
		if match.Source == "fields" && !jiraInverseReferenceFieldID.MatchString(match.TechnicalFieldID) ||
			match.Source == "description" && match.TechnicalFieldID != "description" ||
			match.Source != "fields" && match.Source != "description" && match.TechnicalFieldID != "" {
			return fmt.Errorf("matches[%d].technical_field_id is not source-qualified", index)
		}
		key := strings.Join([]string{match.IssueKey, match.Relation, match.Source, match.TechnicalFieldID}, "\x00")
		if seenMatches[key] {
			return fmt.Errorf("matches contains a duplicate")
		}
		seenMatches[key], matchedKeys[match.IssueKey] = true, true
	}
	if counts.MatchedIssues != len(matchedKeys) {
		return fmt.Errorf("matched_issues does not reconcile with matches")
	}
	if !jiraInverseReferenceOneOf(v.Frontier.Phase, "selection", "verification", "complete") || v.Frontier.Pass < 0 ||
		v.Frontier.PageStart < 0 || v.Frontier.VerifiedIssues < 0 || v.Frontier.VerifiedIssues > counts.VerifiedIssues ||
		v.Frontier.Source != "" && (!validJiraInverseReferenceSource(v.Frontier.Source) || !slices.Contains(v.Sources, v.Frontier.Source)) ||
		v.Frontier.SourceReason != "" && !validJiraInverseReferenceSourceReason(v.Frontier.SourceReason) ||
		(v.Frontier.Source == "") != (v.Frontier.SourceReason == "") {
		return fmt.Errorf("frontier is outside the closed contract")
	}
	if v.Frontier.Phase == "complete" && (v.Frontier.Pass != 0 || v.Frontier.PageStart != 0 || v.Frontier.Source != "" || v.Frontier.SourceReason != "" || v.Frontier.VerifiedIssues != counts.VerifiedIssues) {
		return fmt.Errorf("complete frontier retains partial progress")
	}
	if !v.Reconciliation.Counts || !v.Reconciliation.Sources || !v.Reconciliation.Matches || !v.Reconciliation.Usage {
		return fmt.Errorf("reconciliation contains a false invariant")
	}
	if v.Usage.Requests < 0 || v.Usage.Requests > v.Usage.MaxRequests ||
		v.Usage.ResponseBytes < 0 || v.Usage.ResponseBytes > v.Usage.MaxResponseBytes ||
		v.Usage.MaxIssues < counts.SelectedIssues || !v.Usage.Reconciled {
		return fmt.Errorf("usage does not reconcile")
	}
	wantComplete := v.Mode == "exhaustive" && v.TargetResolution.Complete && v.Selection.Complete && v.Verification.Complete && v.Usage.Reconciled
	if v.Complete != wantComplete || v.AbsenceProven != (v.Complete && len(v.Matches) == 0) {
		return fmt.Errorf("top-level completeness or absence proof does not reconcile")
	}
	return nil
}

func validJiraInverseReferenceSource(source string) bool {
	return jiraInverseReferenceOneOf(source, "comments", "description", "development", "fields", "properties", "remote_links", "worklogs")
}

func validJiraInverseReferencePhaseReason(reason string) bool {
	return jiraInverseReferenceOneOf(reason, "mode_fast", "request_limit", "byte_limit", "issue_limit", "request_failed", "malformed_response", "selection_drift", "source_incomplete")
}

func validJiraInverseReferenceSourceReason(reason string) bool {
	return jiraInverseReferenceOneOf(reason, "request_failed", "request_limit", "byte_limit", "malformed_response", "field_missing", "not_permitted", "not_supported", "mode_fast")
}

func validJiraInverseReferenceMatchTuple(match JiraInverseReferenceMatchView) bool {
	switch match.Relation {
	case "structured_remote_link":
		return match.Source == "remote_links" && match.Stability == "public_api" &&
			jiraInverseReferenceOneOf(match.Confidence, "exact", "high")
	case "development_association":
		return match.Source == "development" && match.Stability == "experimental_api" && match.Confidence == "exact"
	case "literal_mention":
		return jiraInverseReferenceOneOf(match.Source, "comments", "description", "fields", "properties", "remote_links", "worklogs") &&
			match.Stability == "heuristic" && match.Confidence == "high"
	default:
		return false
	}
}

func jiraInverseReferenceOneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}

func anyNegative(values ...int) bool {
	for _, value := range values {
		if value < 0 {
			return true
		}
	}
	return false
}

func anyGreaterThan(maximum int, values ...int) bool {
	for _, value := range values {
		if value > maximum {
			return true
		}
	}
	return false
}
