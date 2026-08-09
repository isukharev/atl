package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/scmref"
)

const jiraInverseReferencePageSize = 100

var inverseReferenceFieldID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

// ConfluencePageReferenceResolver resolves one qualified page reference.
// Concrete backend construction belongs to the outer composition root.
type ConfluencePageReferenceResolver interface {
	ResolvePageReference(context.Context, string) (*ConfluencePageResolution, error)
}

type inverseReferenceTarget struct {
	domain     domain.JiraInverseReferenceTarget
	confluence struct {
		baseURL string
		pageID  string
	}
	gitlab scmref.GitLabProject
}

func validInverseReferenceTargetSyntax(kind domain.JiraInverseReferenceTargetKind, raw string) bool {
	switch kind {
	case domain.JiraInverseReferenceTargetGitLabProject:
		_, ok := scmref.ParseGitLabProject(raw)
		return ok
	case domain.JiraInverseReferenceTargetConfluencePage:
		if isOpaquePageID(raw) {
			return true
		}
		reference, err := url.Parse(raw)
		if err != nil || reference.User != nil || !validInverseReferenceURLComponents(reference) {
			return false
		}
		if reference.IsAbs() {
			return (reference.Scheme == "https" || reference.Scheme == "http") && reference.Host != ""
		}
		return reference.Host == "" && strings.HasPrefix(reference.Path, "/")
	default:
		return false
	}
}

func (s *JiraService) resolveInverseReferenceTarget(ctx context.Context, opts JiraInverseReferenceOptions) (context.Context, domain.JiraInverseReferenceTarget, JiraInverseReferenceTargetResult, inverseReferenceTarget, error) {
	var target inverseReferenceTarget
	opaqueIdentity := ""
	target.domain.Kind = opts.TargetKind
	switch opts.TargetKind {
	case domain.JiraInverseReferenceTargetGitLabProject:
		project, ok := scmref.ParseGitLabProject(opts.Target)
		if !ok {
			return ctx, domain.JiraInverseReferenceTarget{}, JiraInverseReferenceTargetResult{}, target, inverseReferenceUsage("GitLab target is not an exact project URL")
		}
		target.gitlab = project
		target.domain.Value = "https://" + project.Host + "/" + project.ProjectPath
		opaqueIdentity = target.domain.Value
	case domain.JiraInverseReferenceTargetConfluencePage:
		baseURL := strings.TrimSpace(s.inverseConfluenceBaseURL)
		if baseURL == "" && s.cfg != nil {
			baseURL = strings.TrimSpace(s.cfg.ConfluenceURL)
		}
		resolution, needsNetwork, err := resolveConfluenceReferenceOffline(baseURL, opts.Target)
		if err != nil {
			return ctx, domain.JiraInverseReferenceTarget{}, JiraInverseReferenceTargetResult{}, target, err
		}
		if needsNetwork {
			resolver, _ := s.inverseConfluenceReferenceResolver()
			if resolver == nil {
				return ctx, domain.JiraInverseReferenceTarget{}, JiraInverseReferenceTargetResult{}, target,
					fmt.Errorf("%w: configured Confluence reference resolver is unavailable", domain.ErrConfig)
			}
			resolution, err = resolver.ResolvePageReference(ctx, opts.Target)
			if err != nil {
				return ctx, domain.JiraInverseReferenceTarget{}, JiraInverseReferenceTargetResult{}, target, redactInverseReferenceTargetError(err)
			}
		}
		if resolution == nil || !isOpaquePageID(resolution.ID) {
			return ctx, domain.JiraInverseReferenceTarget{}, JiraInverseReferenceTargetResult{}, target,
				fmt.Errorf("%w: Confluence target resolution was malformed", domain.ErrCheckFailed)
		}
		ctx = resolution.Context(ctx)
		target.confluence.baseURL, target.confluence.pageID = baseURL, resolution.ID
		target.domain.Value = resolution.ID
		opaqueIdentity = canonicalInverseReferenceConfluenceOrigin(baseURL) + "\x00" + resolution.ID
	default:
		return ctx, domain.JiraInverseReferenceTarget{}, JiraInverseReferenceTargetResult{}, target, inverseReferenceUsage("target kind is invalid")
	}
	targetID := graphHash(string(target.domain.Kind) + "\x00" + opaqueIdentity)
	return ctx, target.domain, JiraInverseReferenceTargetResult{Kind: target.domain.Kind, OpaqueID: targetID}, target, nil
}

func canonicalInverseReferenceConfluenceOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/")
}

func (s *JiraService) inverseConfluenceReferenceResolver() (ConfluencePageReferenceResolver, string) {
	if s == nil {
		return nil, "not_configured"
	}
	s.inverseConfluenceOnce.Do(func() {
		if s.inverseConfluence != nil || s.inverseConfluenceFactory == nil {
			return
		}
		s.inverseConfluence, s.inverseConfluenceReason = s.inverseConfluenceFactory()
	})
	if s.inverseConfluence == nil && s.inverseConfluenceReason == "" {
		return nil, "not_configured"
	}
	return s.inverseConfluence, s.inverseConfluenceReason
}

func resolveConfluenceReferenceOffline(baseRaw, reference string) (*ConfluencePageResolution, bool, error) {
	baseRaw = strings.TrimSpace(baseRaw)
	if containsControl(baseRaw) {
		return nil, false, fmt.Errorf("%w: configured Confluence origin is required", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(baseRaw); err != nil {
		return nil, false, fmt.Errorf("%w: configured Confluence origin is required", domain.ErrConfig)
	}
	base, err := url.Parse(baseRaw)
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil ||
		base.Opaque != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" ||
		(base.Scheme != "https" && base.Scheme != "http") || !validInverseReferenceURLComponents(base) {
		return nil, false, fmt.Errorf("%w: configured Confluence origin is required", domain.ErrConfig)
	}
	reference = strings.TrimSpace(reference)
	if isOpaquePageID(reference) {
		return &ConfluencePageResolution{ID: reference, Kind: "id"}, false, nil
	}
	u, err := url.Parse(reference)
	if err != nil || u.User != nil || !validInverseReferenceURLComponents(u) {
		return nil, false, inverseReferenceUsage("Confluence target is malformed")
	}
	abs := u.IsAbs()
	if abs {
		if !sameGraphOrigin(u, baseRaw) {
			return nil, false, inverseReferenceUsage("Confluence target is outside the configured origin")
		}
	} else if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return nil, false, inverseReferenceUsage("Confluence target is not a supported reference")
	}
	refPath, ok := confluenceReferencePath(base, u, abs)
	if !ok {
		return nil, false, inverseReferenceUsage("Confluence target is outside the configured context path")
	}
	if id, kind, directErr := directConfluencePageID(refPath, u.Query()); directErr != nil {
		return nil, false, inverseReferenceUsage("Confluence target has invalid page coordinates")
	} else if id != "" {
		return &ConfluencePageResolution{ID: id, Kind: kind, untrusted: true}, false, nil
	}
	if _, _, display := confluenceDisplayReference(refPath); display || isConfluenceShortReference(refPath) {
		return nil, true, nil
	}
	return nil, false, inverseReferenceUsage("Confluence target is not a supported reference")
}

func validInverseReferenceURLComponents(value *url.URL) bool {
	if value == nil {
		return false
	}
	for _, escaped := range []string{value.EscapedPath(), value.EscapedFragment()} {
		decoded, err := url.PathUnescape(escaped)
		if err != nil || containsControl(decoded) {
			return false
		}
	}
	query, err := url.ParseQuery(value.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range query {
		if containsControl(key) {
			return false
		}
		for _, current := range values {
			if containsControl(current) {
				return false
			}
		}
	}
	return true
}

func redactInverseReferenceTargetError(err error) error {
	switch {
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
		return domain.ErrReadAttemptBudgetExhausted
	case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		return domain.ErrReadResponseBudgetExhausted
	case errors.Is(err, domain.ErrAuth):
		return fmt.Errorf("%w: Confluence target resolution was not authorized", domain.ErrAuth)
	case errors.Is(err, domain.ErrForbidden):
		return fmt.Errorf("%w: Confluence target resolution was forbidden", domain.ErrForbidden)
	case errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("%w: Confluence target was not found", domain.ErrNotFound)
	case errors.Is(err, domain.ErrConfig):
		return fmt.Errorf("%w: Confluence target resolver is not configured", domain.ErrConfig)
	case errors.Is(err, domain.ErrUsage):
		return inverseReferenceUsage("Confluence target is invalid")
	default:
		return fmt.Errorf("%w: Confluence target resolution failed", domain.ErrCheckFailed)
	}
}

type inverseReferenceSelectionStats struct {
	candidates int
	scanned    int
	pass       int
	pageStart  int
}

func selectInverseReferenceCandidates(ctx context.Context, selector domain.JiraInverseReferenceSelector, target domain.JiraInverseReferenceTarget, opts JiraInverseReferenceOptions) ([]domain.JiraInverseReferenceIssueIdentity, JiraInverseReferencePhase, inverseReferenceSelectionStats) {
	if opts.Mode == domain.JiraInverseReferenceModeFast {
		issues, total, phase, passStats := runInverseReferencePass(ctx, selector, target, opts, fastInverseReferenceJQL(target, opts), false)
		stats := inverseReferenceSelectionStats{candidates: total, scanned: passStats.scanned, pass: 1, pageStart: passStats.pageStart}
		if stats.candidates < len(issues) {
			stats.candidates = len(issues)
		}
		if phase.Reason == "" || phase.Complete {
			phase = JiraInverseReferencePhase{Complete: false, Reason: JiraInverseReferenceReasonModeFast}
		}
		return issues, phase, stats
	}
	jql := exhaustiveInverseReferenceJQL(opts.ScopeJQL)
	first, firstTotal, firstPhase, firstStats := runInverseReferencePass(ctx, selector, target, opts, jql, true)
	stats := inverseReferenceSelectionStats{candidates: firstTotal, scanned: firstStats.scanned, pass: 1, pageStart: firstStats.pageStart}
	if stats.candidates < len(first) {
		stats.candidates = len(first)
	}
	if !firstPhase.Complete {
		return first, firstPhase, stats
	}
	second, secondTotal, secondPhase, secondStats := runInverseReferencePass(ctx, selector, target, opts, jql, true)
	stats.scanned += secondStats.scanned
	stats.pass, stats.pageStart = 2, secondStats.pageStart
	if secondTotal > stats.candidates {
		stats.candidates = secondTotal
	}
	if !secondPhase.Complete {
		return first, secondPhase, stats
	}
	if firstTotal != secondTotal || !sameInverseReferenceIdentitySet(first, second) {
		union, observed := unionInverseReferenceIdentities(first, second, opts.MaxIssues)
		if observed > stats.candidates {
			stats.candidates = observed
		}
		return union, JiraInverseReferencePhase{Reason: JiraInverseReferenceReasonSelectionDrift}, stats
	}
	return first, JiraInverseReferencePhase{Complete: true}, stats
}

type inverseReferencePassStats struct {
	scanned   int
	pageStart int
}

func runInverseReferencePass(ctx context.Context, selector domain.JiraInverseReferenceSelector, target domain.JiraInverseReferenceTarget, opts JiraInverseReferenceOptions, jql string, enforceIssueCap bool) ([]domain.JiraInverseReferenceIssueIdentity, int, JiraInverseReferencePhase, inverseReferencePassStats) {
	out := []domain.JiraInverseReferenceIssueIdentity{}
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	start, expectedTotal := 0, -1
	stats := inverseReferencePassStats{}
	for {
		remaining := opts.MaxIssues - len(out)
		if remaining <= 0 {
			return out, expectedTotal, JiraInverseReferencePhase{Reason: JiraInverseReferenceReasonIssueLimit}, stats
		}
		pageSize := jiraInverseReferencePageSize
		if pageSize > remaining {
			pageSize = remaining
		}
		stats.pageStart = start
		page, err := selector.SelectInverseReferencePage(ctx, domain.JiraInverseReferenceSelection{
			Target: target, Mode: opts.Mode, Sources: opts.Sources, JQL: jql,
			Order: domain.JiraInverseReferenceOrderAscending, StartAt: start, MaxResults: pageSize,
		})
		if err != nil {
			return out, expectedTotal, JiraInverseReferencePhase{Reason: classifyInverseReferenceSelectionError(err)}, stats
		}
		stats.scanned += len(page.Issues)
		if page.StartAt != start || page.MaxResults <= 0 || page.MaxResults > pageSize || page.Total < 0 ||
			len(page.Issues) > page.MaxResults || page.StartAt > page.Total || page.StartAt+len(page.Issues) > page.Total {
			return out, expectedTotal, JiraInverseReferencePhase{Reason: JiraInverseReferenceReasonMalformedResponse}, stats
		}
		if expectedTotal < 0 {
			expectedTotal = page.Total
		} else if page.Total != expectedTotal {
			return out, expectedTotal, JiraInverseReferencePhase{Reason: JiraInverseReferenceReasonSelectionDrift}, stats
		}
		for _, issue := range page.Issues {
			if !validInverseReferenceIssue(issue) || seenIDs[issue.ID] || seenKeys[issue.Key] {
				return out, expectedTotal, JiraInverseReferencePhase{Reason: JiraInverseReferenceReasonMalformedResponse}, stats
			}
			seenIDs[issue.ID], seenKeys[issue.Key] = true, true
			out = append(out, issue)
		}
		next := page.StartAt + len(page.Issues)
		if next == page.Total {
			return out, expectedTotal, JiraInverseReferencePhase{Complete: !enforceIssueCap || page.Total <= opts.MaxIssues}, stats
		}
		if len(page.Issues) == 0 || next <= start {
			return out, expectedTotal, JiraInverseReferencePhase{Reason: JiraInverseReferenceReasonMalformedResponse}, stats
		}
		if enforceIssueCap && page.Total > opts.MaxIssues && len(out) >= opts.MaxIssues {
			return out, expectedTotal, JiraInverseReferencePhase{Reason: JiraInverseReferenceReasonIssueLimit}, stats
		}
		start = next
	}
}

func exhaustiveInverseReferenceJQL(scope string) string { return "(" + scope + ") ORDER BY key ASC" }

func fastInverseReferenceJQL(target domain.JiraInverseReferenceTarget, opts JiraInverseReferenceOptions) string {
	clauses := []string{`text ~ "\"` + escapeInverseReferenceJQLString(target.Value) + `\""`}
	if sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceDevelopment) {
		clauses = append(clauses,
			"development[pullrequests].all > 0", "development[commits].all > 0", "development[branches].all > 0")
	}
	return "(" + opts.ScopeJQL + ") AND (" + strings.Join(clauses, " OR ") + ") ORDER BY key ASC"
}

func escapeInverseReferenceJQLString(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`)
}

func classifyInverseReferenceSelectionError(err error) JiraInverseReferenceCompletenessReason {
	switch {
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
		return JiraInverseReferenceReasonRequestLimit
	case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		return JiraInverseReferenceReasonByteLimit
	case errors.Is(err, domain.ErrCheckFailed):
		return JiraInverseReferenceReasonMalformedResponse
	default:
		return JiraInverseReferenceReasonRequestFailed
	}
}

func sameInverseReferenceIdentitySet(left, right []domain.JiraInverseReferenceIssueIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	l, r := append([]domain.JiraInverseReferenceIssueIdentity(nil), left...), append([]domain.JiraInverseReferenceIssueIdentity(nil), right...)
	sortInverseReferenceIdentities(l)
	sortInverseReferenceIdentities(r)
	for index := range l {
		if l[index] != r[index] {
			return false
		}
	}
	return true
}

func unionInverseReferenceIdentities(left, right []domain.JiraInverseReferenceIssueIdentity, limit int) ([]domain.JiraInverseReferenceIssueIdentity, int) {
	byID := map[string]domain.JiraInverseReferenceIssueIdentity{}
	for _, issue := range append(append([]domain.JiraInverseReferenceIssueIdentity(nil), left...), right...) {
		if existing, ok := byID[issue.ID]; !ok || issue.Key < existing.Key {
			byID[issue.ID] = issue
		}
	}
	out := make([]domain.JiraInverseReferenceIssueIdentity, 0, len(byID))
	for _, issue := range byID {
		out = append(out, issue)
	}
	sortInverseReferenceIdentities(out)
	observed := len(out)
	if observed > limit {
		out = out[:limit]
	}
	return out, observed
}

func sortInverseReferenceIdentities(issues []domain.JiraInverseReferenceIssueIdentity) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Key != issues[j].Key {
			return issues[i].Key < issues[j].Key
		}
		return issues[i].ID < issues[j].ID
	})
}

func validInverseReferenceIssue(issue domain.JiraInverseReferenceIssueIdentity) bool {
	return len(issue.Key) <= 128 && graphNumericIDPattern.MatchString(issue.ID) && graphJiraKeyPattern.FindString(issue.Key) == issue.Key
}

func validInverseReferenceFieldID(value string) bool {
	return inverseReferenceFieldID.MatchString(value)
}

func sourceSelected(sources []domain.JiraInverseReferenceSource, wanted domain.JiraInverseReferenceSource) bool {
	for _, source := range sources {
		if source == wanted {
			return true
		}
	}
	return false
}

func containsControl(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, current := range value {
		if unicode.IsControl(current) || current == utf8.RuneError {
			return true
		}
	}
	return false
}

func containsControlExceptWhitespace(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, current := range value {
		if current == utf8.RuneError || unicode.IsControl(current) && current != '\n' && current != '\r' && current != '\t' {
			return true
		}
	}
	return false
}

func unquotedJQLOrderBy(value string) (bool, bool) {
	words := []string{}
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, strings.ToLower(current.String()))
			current.Reset()
		}
	}
	for _, char := range value {
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			switch char {
			case '\\':
				escaped = true
			case quote:
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			flush()
			quote = char
			continue
		}
		if unicode.IsLetter(char) {
			current.WriteRune(char)
		} else {
			flush()
		}
	}
	flush()
	if quote != 0 || escaped {
		return false, false
	}
	for index := 0; index+1 < len(words); index++ {
		if words[index] == "order" && words[index+1] == "by" {
			return true, true
		}
	}
	return false, true
}
