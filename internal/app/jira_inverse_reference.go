package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraInverseReferenceSchemaVersion    = 1
	jiraInverseReferenceMaxTargetBytes   = 2048
	jiraInverseReferenceMaxJQLBytes      = 16 << 10
	jiraInverseReferenceMaxFields        = 128
	jiraInverseReferenceMaxIssues        = 5000
	jiraInverseReferenceMaxRequests      = 25000
	jiraInverseReferenceMaxResponseBytes = int64(256 << 20)
)

// JiraInverseReferenceOptions is the complete bounded input contract for one
// inverse-reference search. Every limit is explicit so no invocation can fall
// back to an unbounded scan.
type JiraInverseReferenceOptions struct {
	Target           string
	TargetKind       domain.JiraInverseReferenceTargetKind
	ScopeJQL         string
	Mode             domain.JiraInverseReferenceMode
	Sources          []domain.JiraInverseReferenceSource
	Fields           []string
	MaxIssues        int
	MaxRequests      int
	MaxResponseBytes int64
}

// JiraInverseReferenceCompletenessReason is a closed, content-free reason for
// an incomplete selection or verification phase.
type JiraInverseReferenceCompletenessReason string

const (
	JiraInverseReferenceReasonModeFast          JiraInverseReferenceCompletenessReason = "mode_fast"
	JiraInverseReferenceReasonRequestLimit      JiraInverseReferenceCompletenessReason = "request_limit"
	JiraInverseReferenceReasonByteLimit         JiraInverseReferenceCompletenessReason = "byte_limit"
	JiraInverseReferenceReasonIssueLimit        JiraInverseReferenceCompletenessReason = "issue_limit"
	JiraInverseReferenceReasonRequestFailed     JiraInverseReferenceCompletenessReason = "request_failed"
	JiraInverseReferenceReasonMalformedResponse JiraInverseReferenceCompletenessReason = "malformed_response"
	JiraInverseReferenceReasonSelectionDrift    JiraInverseReferenceCompletenessReason = "selection_drift"
	JiraInverseReferenceReasonSourceIncomplete  JiraInverseReferenceCompletenessReason = "source_incomplete"
)

// JiraInverseReferencePhase qualifies one independently provable stage.
type JiraInverseReferencePhase struct {
	Complete bool                                   `json:"complete"`
	Reason   JiraInverseReferenceCompletenessReason `json:"reason,omitempty"`
}

// JiraInverseReferenceTargetResult exposes only a one-way opaque identity.
type JiraInverseReferenceTargetResult struct {
	Kind     domain.JiraInverseReferenceTargetKind `json:"kind"`
	OpaqueID string                                `json:"opaque_id"`
}

// JiraInverseReferenceRelation is the closed relation vocabulary.
type JiraInverseReferenceRelation string

const (
	JiraInverseReferenceRelationStructuredRemoteLink JiraInverseReferenceRelation = "structured_remote_link"
	JiraInverseReferenceRelationDevelopment          JiraInverseReferenceRelation = "development_association"
	JiraInverseReferenceRelationLiteral              JiraInverseReferenceRelation = "literal_mention"
)

const JiraInverseReferenceDirectionIssueToTarget = "issue_to_target"

// JiraInverseReferenceMatch is one deduplicated, content-free observation.
type JiraInverseReferenceResultMatch struct {
	IssueKey         string                            `json:"issue_key"`
	Relation         JiraInverseReferenceRelation      `json:"relation"`
	Direction        string                            `json:"direction"`
	Source           domain.JiraInverseReferenceSource `json:"source"`
	TechnicalFieldID string                            `json:"technical_field_id,omitempty"`
	Stability        domain.ArtifactGraphStability     `json:"stability"`
	Confidence       string                            `json:"confidence"`
	Complete         bool                              `json:"complete"`
}

type jiraInverseReferenceIssueResult struct {
	identity domain.JiraInverseReferenceIssueIdentity
	status   domain.JiraInverseReferenceMatchStatus
	sources  []domain.JiraInverseReferenceSourceOutcome
}

// JiraInverseReferenceSourceCounts reconciles every requested issue/source
// outcome without exposing values read from Jira.
type JiraInverseReferenceSourceCounts struct {
	Source      domain.JiraInverseReferenceSource `json:"source"`
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

// JiraInverseReferenceReasonCount qualifies incomplete source outcomes using
// only the domain's closed static vocabulary.
type JiraInverseReferenceReasonCount struct {
	Reason domain.JiraInverseReferenceReason `json:"reason"`
	Count  int                               `json:"count"`
}

// JiraInverseReferenceCounts is the deterministic aggregate projection.
// ScannedIssues counts physical issue identity rows returned across selection
// passes, so a complete exhaustive search scans exactly twice SelectedIssues.
type JiraInverseReferenceCounts struct {
	SelectedIssues  int `json:"selected_issues"`
	CandidateIssues int `json:"candidate_issues"`
	ScannedIssues   int `json:"scanned_issues"`
	VerifiedIssues  int `json:"verified_issues"`
	MatchedIssues   int `json:"matched_issues"`
	Matches         int `json:"matches"`
}

// JiraInverseReferenceFrontier is a bounded, content-free progress marker for
// partial results. No resource identity or query text is retained.
type JiraInverseReferenceFrontier struct {
	Phase          string                            `json:"phase"`
	Pass           int                               `json:"pass,omitempty"`
	PageStart      int                               `json:"page_start,omitempty"`
	VerifiedIssues int                               `json:"verified_issues"`
	Source         domain.JiraInverseReferenceSource `json:"source,omitempty"`
	SourceReason   domain.JiraInverseReferenceReason `json:"source_reason,omitempty"`
}

// JiraInverseReferenceReconciliation makes aggregate invariants mechanical.
type JiraInverseReferenceReconciliation struct {
	Counts  bool `json:"counts"`
	Sources bool `json:"sources"`
	Matches bool `json:"matches"`
	Usage   bool `json:"usage"`
}

// JiraInverseReferenceUsage emits the physical transport budget ledger.
type JiraInverseReferenceUsage struct {
	MaxIssues        int   `json:"max_issues"`
	MaxRequests      int   `json:"max_requests"`
	Requests         int   `json:"requests"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
	ResponseBytes    int64 `json:"response_bytes"`
	Reconciled       bool  `json:"reconciled"`
}

// JiraInverseReferenceResult is schema-v1, deterministic and content-free.
type JiraInverseReferenceResult struct {
	SchemaVersion     int                                 `json:"schema_version"`
	Target            JiraInverseReferenceTargetResult    `json:"target"`
	Mode              domain.JiraInverseReferenceMode     `json:"mode"`
	Sources           []domain.JiraInverseReferenceSource `json:"sources"`
	EffectiveFieldIDs []string                            `json:"effective_field_ids"`
	TargetResolution  JiraInverseReferencePhase           `json:"target_resolution"`
	Selection         JiraInverseReferencePhase           `json:"selection"`
	Verification      JiraInverseReferencePhase           `json:"verification"`
	Counts            JiraInverseReferenceCounts          `json:"counts"`
	SourceCounts      []JiraInverseReferenceSourceCounts  `json:"source_counts"`
	Matches           []JiraInverseReferenceResultMatch   `json:"matches"`
	Frontier          JiraInverseReferenceFrontier        `json:"frontier"`
	Reconciliation    JiraInverseReferenceReconciliation  `json:"reconciliation"`
	Usage             JiraInverseReferenceUsage           `json:"usage"`
	Complete          bool                                `json:"complete"`
	AbsenceProven     bool                                `json:"absence_proven"`
	issueResults      []jiraInverseReferenceIssueResult
}

// NormalizeJiraInverseReferenceOptions performs pure fail-fast validation. It
// does not inspect config, construct a service, or contact either backend.
func NormalizeJiraInverseReferenceOptions(opts JiraInverseReferenceOptions) (JiraInverseReferenceOptions, error) {
	opts.Target = strings.TrimSpace(opts.Target)
	opts.ScopeJQL = strings.TrimSpace(opts.ScopeJQL)
	if !domain.ValidJiraInverseReferenceTargetKind(opts.TargetKind) {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("target kind is required")
	}
	if opts.Target == "" || len(opts.Target) > jiraInverseReferenceMaxTargetBytes || containsControl(opts.Target) {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("target is outside the supported bounds")
	}
	if opts.ScopeJQL == "" || len(opts.ScopeJQL) > jiraInverseReferenceMaxJQLBytes || containsControlExceptWhitespace(opts.ScopeJQL) {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("scope JQL is outside the supported bounds")
	}
	if hasOrder, valid := unquotedJQLOrderBy(opts.ScopeJQL); !valid || hasOrder {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("scope JQL must be a predicate without ORDER BY")
	}
	if !domain.ValidJiraInverseReferenceMode(opts.Mode) {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("search mode is required")
	}
	if len(opts.Sources) == 0 || len(opts.Sources) > 7 {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("at least one bounded source is required")
	}
	seenSources := make(map[domain.JiraInverseReferenceSource]bool, len(opts.Sources))
	for _, source := range opts.Sources {
		if !domain.ValidJiraInverseReferenceSource(source) || seenSources[source] {
			return JiraInverseReferenceOptions{}, inverseReferenceUsage("source selection is invalid")
		}
		seenSources[source] = true
	}
	opts.Sources = append([]domain.JiraInverseReferenceSource(nil), opts.Sources...)
	sort.Slice(opts.Sources, func(i, j int) bool { return opts.Sources[i] < opts.Sources[j] })
	if len(opts.Fields) > jiraInverseReferenceMaxFields {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("field selection exceeds the supported bound")
	}
	if seenSources[domain.JiraInverseReferenceSourceFields] != (len(opts.Fields) > 0) {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("fields source requires exact technical field ids")
	}
	seenFields := make(map[string]bool, len(opts.Fields))
	for _, fieldID := range opts.Fields {
		if !validInverseReferenceFieldID(fieldID) || seenFields[fieldID] {
			return JiraInverseReferenceOptions{}, inverseReferenceUsage("technical field id selection is invalid")
		}
		seenFields[fieldID] = true
	}
	opts.Fields = append([]string(nil), opts.Fields...)
	sort.Strings(opts.Fields)
	if opts.MaxIssues <= 0 || opts.MaxIssues > jiraInverseReferenceMaxIssues ||
		opts.MaxRequests <= 0 || opts.MaxRequests > jiraInverseReferenceMaxRequests ||
		opts.MaxResponseBytes <= 0 || opts.MaxResponseBytes > jiraInverseReferenceMaxResponseBytes {
		return JiraInverseReferenceOptions{}, inverseReferenceUsage("search limits are outside the supported bounds")
	}
	return opts, nil
}

// SearchInverseReferences resolves one target, selects candidates and verifies
// every requested source under one shared single-attempt read budget.
func (s *JiraService) SearchInverseReferences(ctx context.Context, raw JiraInverseReferenceOptions) (*JiraInverseReferenceResult, error) {
	opts, err := NormalizeJiraInverseReferenceOptions(raw)
	if err != nil {
		return nil, err
	}
	if s == nil || s.tr == nil {
		return nil, fmt.Errorf("%w: Jira inverse-reference reader is not configured", domain.ErrConfig)
	}
	selector, ok := s.tr.(domain.JiraInverseReferenceSelector)
	if !ok {
		return nil, fmt.Errorf("%w: Jira inverse-reference selection is not supported", domain.ErrConfig)
	}
	snapshotReader, ok := s.tr.(domain.JiraInverseReferenceSnapshotReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira inverse-reference snapshots are not supported", domain.ErrConfig)
	}
	budget, budgetErr := domain.NewReadBudget(opts.MaxRequests, opts.MaxResponseBytes)
	if budgetErr != nil {
		return nil, inverseReferenceUsage("search limits are invalid")
	}
	readCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)))
	target, targetResult, targetMeta, err := s.resolveInverseReferenceTarget(readCtx, opts)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	result := newJiraInverseReferenceResult(opts, targetResult)
	selected, selection, selectionStats := selectInverseReferenceCandidates(readCtx, selector, target, opts)
	result.Selection = selection
	result.Counts.SelectedIssues = len(selected)
	result.Counts.CandidateIssues = selectionStats.candidates
	result.Counts.ScannedIssues = selectionStats.scanned
	if !selection.Complete {
		result.Frontier = JiraInverseReferenceFrontier{Phase: "selection", Pass: selectionStats.pass, PageStart: selectionStats.pageStart}
	} else {
		result.Frontier = JiraInverseReferenceFrontier{Phase: "verification"}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	verifyInverseReferenceCandidates(readCtx, s.tr, snapshotReader, targetMeta, opts, selected, result)
	usage := budget.Usage()
	result.Usage.Requests = usage.Attempts
	result.Usage.ResponseBytes = usage.ResponseBytes
	finalizeJiraInverseReferenceResult(result)
	return result, nil
}

// RenderJiraInverseReferencesText renders only the content-free match table.
func RenderJiraInverseReferencesText(result *JiraInverseReferenceResult) (string, error) {
	if result == nil || result.SchemaVersion != jiraInverseReferenceSchemaVersion {
		return "", fmt.Errorf("%w: inverse-reference result schema is invalid", domain.ErrCheckFailed)
	}
	matches := append([]JiraInverseReferenceResultMatch(nil), result.Matches...)
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].IssueKey != matches[j].IssueKey {
			return matches[i].IssueKey < matches[j].IssueKey
		}
		if matches[i].Source != matches[j].Source {
			return matches[i].Source < matches[j].Source
		}
		if matches[i].Relation != matches[j].Relation {
			return matches[i].Relation < matches[j].Relation
		}
		return matches[i].TechnicalFieldID < matches[j].TechnicalFieldID
	})
	rows := make([][]string, 0, len(matches))
	for _, match := range matches {
		rows = append(rows, []string{match.IssueKey, string(match.Relation), string(match.Source), match.Confidence, fmt.Sprint(match.Complete)})
	}
	return MarkdownTable([]string{"KEY", "RELATION", "SOURCE", "CONFIDENCE", "COMPLETE"}, rows), nil
}

func newJiraInverseReferenceResult(opts JiraInverseReferenceOptions, target JiraInverseReferenceTargetResult) *JiraInverseReferenceResult {
	counts := make([]JiraInverseReferenceSourceCounts, len(opts.Sources))
	for index, source := range opts.Sources {
		counts[index].Source = source
		counts[index].Reasons = []JiraInverseReferenceReasonCount{}
	}
	return &JiraInverseReferenceResult{
		SchemaVersion: jiraInverseReferenceSchemaVersion, Target: target, Mode: opts.Mode,
		Sources:           append([]domain.JiraInverseReferenceSource(nil), opts.Sources...),
		EffectiveFieldIDs: append([]string{}, opts.Fields...), TargetResolution: JiraInverseReferencePhase{Complete: true},
		SourceCounts: counts, Matches: []JiraInverseReferenceResultMatch{}, Frontier: JiraInverseReferenceFrontier{Phase: "selection"},
		Usage:        JiraInverseReferenceUsage{MaxIssues: opts.MaxIssues, MaxRequests: opts.MaxRequests, MaxResponseBytes: opts.MaxResponseBytes},
		issueResults: []jiraInverseReferenceIssueResult{},
	}
}

func finalizeJiraInverseReferenceResult(result *JiraInverseReferenceResult) {
	result.Counts.VerifiedIssues = len(result.issueResults)
	result.Counts.Matches = len(result.Matches)
	matched := map[string]bool{}
	verificationComplete := true
	for _, issue := range result.issueResults {
		if issue.status == domain.JiraInverseReferenceMatched {
			matched[issue.identity.Key] = true
		}
		for _, outcome := range issue.sources {
			for index := range result.SourceCounts {
				if result.SourceCounts[index].Source != outcome.Source {
					continue
				}
				switch outcome.Status {
				case domain.JiraInverseReferenceSourceComplete:
					result.SourceCounts[index].Complete++
				case domain.JiraInverseReferenceSourceEmpty:
					result.SourceCounts[index].Empty++
				case domain.JiraInverseReferenceSourcePartial:
					result.SourceCounts[index].Partial++
					verificationComplete = false
				case domain.JiraInverseReferenceSourceForbidden:
					result.SourceCounts[index].Forbidden++
					verificationComplete = false
				case domain.JiraInverseReferenceSourceUnsupported:
					result.SourceCounts[index].Unsupported++
					verificationComplete = false
				case domain.JiraInverseReferenceSourceSkipped:
					result.SourceCounts[index].Skipped++
					verificationComplete = false
				default:
					verificationComplete = false
				}
				if outcome.Reason != "" {
					found := false
					for reasonIndex := range result.SourceCounts[index].Reasons {
						if result.SourceCounts[index].Reasons[reasonIndex].Reason == outcome.Reason {
							result.SourceCounts[index].Reasons[reasonIndex].Count++
							found = true
							break
						}
					}
					if !found {
						result.SourceCounts[index].Reasons = append(result.SourceCounts[index].Reasons, JiraInverseReferenceReasonCount{Reason: outcome.Reason, Count: 1})
					}
				}
				break
			}
		}
	}
	result.Counts.MatchedIssues = len(matched)
	result.Verification.Complete = verificationComplete && len(result.issueResults) == result.Counts.SelectedIssues
	if !result.Verification.Complete && result.Verification.Reason == "" {
		result.Verification.Reason = JiraInverseReferenceReasonSourceIncomplete
	}
	sourceReconciled := len(result.SourceCounts) == len(result.Sources)
	for index := range result.SourceCounts {
		counts := &result.SourceCounts[index]
		sort.Slice(counts.Reasons, func(i, j int) bool { return counts.Reasons[i].Reason < counts.Reasons[j].Reason })
		counts.Total = counts.Complete + counts.Empty + counts.Partial + counts.Forbidden + counts.Unsupported + counts.Skipped
		reasonTotal := 0
		for _, reason := range counts.Reasons {
			reasonTotal += reason.Count
		}
		incompleteTotal := counts.Partial + counts.Forbidden + counts.Unsupported + counts.Skipped
		counts.Reconciled = counts.Total == result.Counts.SelectedIssues && reasonTotal == incompleteTotal
		sourceReconciled = sourceReconciled && counts.Reconciled
	}
	scanReconciled := result.Counts.ScannedIssues >= result.Counts.SelectedIssues
	if result.Selection.Complete {
		scanReconciled = result.Counts.CandidateIssues == result.Counts.SelectedIssues &&
			result.Counts.ScannedIssues == result.Counts.SelectedIssues*2
	}
	result.Reconciliation.Counts = result.Counts.SelectedIssues >= 0 && result.Counts.SelectedIssues <= result.Usage.MaxIssues &&
		result.Counts.CandidateIssues >= result.Counts.SelectedIssues && result.Counts.ScannedIssues >= result.Counts.SelectedIssues &&
		result.Counts.VerifiedIssues == len(result.issueResults) && result.Counts.VerifiedIssues == result.Counts.SelectedIssues && scanReconciled
	result.Reconciliation.Sources = sourceReconciled
	result.Reconciliation.Matches = result.Counts.Matches == len(result.Matches) && result.Counts.MatchedIssues == len(matched)
	result.Reconciliation.Usage = result.Usage.Requests >= 0 && result.Usage.Requests <= result.Usage.MaxRequests &&
		result.Usage.ResponseBytes >= 0 && result.Usage.ResponseBytes <= result.Usage.MaxResponseBytes
	result.Usage.Reconciled = result.Reconciliation.Counts && result.Reconciliation.Sources &&
		result.Reconciliation.Matches && result.Reconciliation.Usage
	result.Complete = result.Mode == domain.JiraInverseReferenceModeExhaustive && result.TargetResolution.Complete &&
		result.Selection.Complete && result.Verification.Complete && result.Usage.Reconciled
	result.AbsenceProven = result.Complete && len(result.Matches) == 0
	if result.Complete {
		result.Frontier = JiraInverseReferenceFrontier{Phase: "complete", VerifiedIssues: result.Counts.VerifiedIssues}
	} else if result.Frontier.Phase == "" {
		result.Frontier = JiraInverseReferenceFrontier{Phase: "verification", VerifiedIssues: result.Counts.VerifiedIssues}
	}
}

func inverseReferenceUsage(message string) error {
	return fmt.Errorf("%w: %s", domain.ErrUsage, message)
}

func classifyInverseReferenceSourceError(err error) domain.JiraInverseReferenceSourceOutcome {
	out := domain.JiraInverseReferenceSourceOutcome{Status: domain.JiraInverseReferenceSourcePartial}
	switch {
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
		out.Reason = domain.JiraInverseReferenceReasonRequestLimit
	case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		out.Reason = domain.JiraInverseReferenceReasonByteLimit
	case errors.Is(err, domain.ErrAuth), errors.Is(err, domain.ErrForbidden):
		out.Status, out.Reason = domain.JiraInverseReferenceSourceForbidden, domain.JiraInverseReferenceReasonNotPermitted
	case errors.Is(err, domain.ErrNotFound):
		out.Status, out.Reason = domain.JiraInverseReferenceSourceUnsupported, domain.JiraInverseReferenceReasonNotSupported
	case errors.Is(err, domain.ErrCheckFailed):
		out.Reason = domain.JiraInverseReferenceReasonMalformed
	default:
		out.Reason = domain.JiraInverseReferenceReasonRequestFailed
	}
	return out
}
