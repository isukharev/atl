package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/scmref"
)

const (
	inverseReferenceMaxCollectionRows = 10_000
	inverseReferenceMaxValueBytes     = 64 << 10
	confluenceRemoteApplicationType   = "com.atlassian.confluence"
)

var inverseReferenceLiteralURLPattern = regexp.MustCompile("(?i)https?://[^\\s\\p{Z}\\p{Pe}\\p{Pf}<>\"')\\]}*`]+")

func verifyInverseReferenceCandidates(ctx context.Context, tracker domain.Tracker, snapshotReader domain.JiraInverseReferenceSnapshotReader, target inverseReferenceTarget, opts JiraInverseReferenceOptions, selected []domain.JiraInverseReferenceIssueIdentity, result *JiraInverseReferenceResult) error {
	issues := append([]domain.JiraInverseReferenceIssueIdentity(nil), selected...)
	sortInverseReferenceIdentities(issues)
	matchKeys := map[string]bool{}
	for issueIndex, issue := range issues {
		if err := ctx.Err(); err != nil {
			return err
		}
		outcomes := make(map[domain.JiraInverseReferenceSource]domain.JiraInverseReferenceSourceOutcome, len(opts.Sources))
		issueMatches := []JiraInverseReferenceResultMatch{}
		if sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceDescription) ||
			sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceFields) ||
			sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceProperties) {
			if err := collectInverseReferenceSnapshot(ctx, snapshotReader, issue, target, opts, outcomes, &issueMatches); err != nil {
				return err
			}
		}
		for _, source := range opts.Sources {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, exists := outcomes[source]; exists {
				continue
			}
			var outcome domain.JiraInverseReferenceSourceOutcome
			var matches []JiraInverseReferenceResultMatch
			var readErr error
			switch source {
			case domain.JiraInverseReferenceSourceComments:
				outcome, matches, readErr = collectInverseReferenceComments(ctx, tracker, issue, target)
			case domain.JiraInverseReferenceSourceWorklogs:
				outcome, matches, readErr = collectInverseReferenceWorklogs(ctx, tracker, issue, target)
			case domain.JiraInverseReferenceSourceRemoteLinks:
				outcome, matches, readErr = collectInverseReferenceRemoteLinks(ctx, tracker, issue, target)
			case domain.JiraInverseReferenceSourceDevelopment:
				outcome, matches, readErr = collectInverseReferenceDevelopment(ctx, tracker, issue, target)
			default:
				outcome = domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceUnsupported, Reason: domain.JiraInverseReferenceReasonNotSupported}
			}
			if readErr != nil {
				return readErr
			}
			outcomes[source] = outcome
			issueMatches = append(issueMatches, matches...)
		}
		orderedOutcomes := make([]domain.JiraInverseReferenceSourceOutcome, 0, len(opts.Sources))
		indeterminate := false
		for _, source := range opts.Sources {
			outcome, ok := outcomes[source]
			if !ok {
				outcome = domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceSkipped, Reason: domain.JiraInverseReferenceReasonRequestFailed}
			}
			orderedOutcomes = append(orderedOutcomes, outcome)
			if outcome.Status != domain.JiraInverseReferenceSourceComplete && outcome.Status != domain.JiraInverseReferenceSourceEmpty {
				indeterminate = true
				if result.Selection.Complete && result.Frontier.Phase == "verification" && result.Frontier.Source == "" {
					result.Frontier = JiraInverseReferenceFrontier{Phase: "verification", VerifiedIssues: issueIndex, Source: source, SourceReason: outcome.Reason}
				}
			}
		}
		status := domain.JiraInverseReferenceNotMatched
		if len(issueMatches) > 0 {
			status = domain.JiraInverseReferenceMatched
		} else if indeterminate {
			status = domain.JiraInverseReferenceIndeterminate
		}
		result.issueResults = append(result.issueResults, jiraInverseReferenceIssueResult{identity: issue, status: status, sources: orderedOutcomes})
		for _, match := range issueMatches {
			key := match.IssueKey + "\x00" + string(match.Source) + "\x00" + string(match.Relation) + "\x00" + match.TechnicalFieldID
			if !matchKeys[key] {
				matchKeys[key] = true
				result.Matches = append(result.Matches, match)
			}
		}
	}
	sort.Slice(result.Matches, func(i, j int) bool {
		left, right := result.Matches[i], result.Matches[j]
		if left.IssueKey != right.IssueKey {
			return left.IssueKey < right.IssueKey
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Relation != right.Relation {
			return left.Relation < right.Relation
		}
		return left.TechnicalFieldID < right.TechnicalFieldID
	})
	return ctx.Err()
}

func collectInverseReferenceSnapshot(ctx context.Context, reader domain.JiraInverseReferenceSnapshotReader, issue domain.JiraInverseReferenceIssueIdentity, target inverseReferenceTarget, opts JiraInverseReferenceOptions, outcomes map[domain.JiraInverseReferenceSource]domain.JiraInverseReferenceSourceOutcome, matches *[]JiraInverseReferenceResultMatch) error {
	fieldIDs := append([]string(nil), opts.Fields...)
	if sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceDescription) && !inverseReferenceStringSelected(fieldIDs, "description") {
		fieldIDs = append(fieldIDs, "description")
	}
	sort.Strings(fieldIDs)
	snapshot, err := reader.ReadInverseReferenceSnapshot(ctx, domain.JiraInverseReferenceSnapshotRequest{
		Issue: issue, FieldIDs: fieldIDs, IncludeProperties: sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceProperties),
	})
	if err != nil {
		if cancelErr := inverseReferenceContextError(ctx, err); cancelErr != nil {
			return cancelErr
		}
		for _, source := range []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceDescription, domain.JiraInverseReferenceSourceFields, domain.JiraInverseReferenceSourceProperties} {
			if sourceSelected(opts.Sources, source) {
				outcome := classifyInverseReferenceSourceError(err)
				outcome.Source = source
				outcomes[source] = outcome
			}
		}
		return nil
	}
	if snapshot.Issue != issue {
		for _, source := range []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceDescription, domain.JiraInverseReferenceSourceFields, domain.JiraInverseReferenceSourceProperties} {
			if sourceSelected(opts.Sources, source) {
				outcomes[source] = malformedInverseReferenceOutcome(source)
			}
		}
		return nil
	}
	byField := make(map[string]domain.JiraInverseReferenceFieldSnapshot, len(snapshot.Fields))
	requestedFields := make(map[string]bool, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		requestedFields[fieldID] = true
	}
	duplicateField := false
	for _, field := range snapshot.Fields {
		if !validInverseReferenceFieldID(field.FieldID) || !requestedFields[field.FieldID] || byField[field.FieldID].FieldID != "" {
			duplicateField = true
			continue
		}
		byField[field.FieldID] = field
	}
	if sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceDescription) {
		outcome, fieldMatches := matchInverseReferenceFields(issue, target, []string{"description"}, byField, duplicateField, domain.JiraInverseReferenceSourceDescription)
		outcomes[outcome.Source] = outcome
		*matches = append(*matches, fieldMatches...)
	}
	if sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceFields) {
		outcome, fieldMatches := matchInverseReferenceFields(issue, target, opts.Fields, byField, duplicateField, domain.JiraInverseReferenceSourceFields)
		outcomes[outcome.Source] = outcome
		*matches = append(*matches, fieldMatches...)
	}
	if sourceSelected(opts.Sources, domain.JiraInverseReferenceSourceProperties) {
		outcome, propertyMatches := matchInverseReferenceProperties(issue, target, snapshot.Properties)
		outcomes[outcome.Source] = outcome
		*matches = append(*matches, propertyMatches...)
	}
	return nil
}

func inverseReferenceStringSelected(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func matchInverseReferenceFields(issue domain.JiraInverseReferenceIssueIdentity, target inverseReferenceTarget, requested []string, fields map[string]domain.JiraInverseReferenceFieldSnapshot, malformed bool, source domain.JiraInverseReferenceSource) (domain.JiraInverseReferenceSourceOutcome, []JiraInverseReferenceResultMatch) {
	outcome := domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceComplete}
	matches := []JiraInverseReferenceResultMatch{}
	nonempty := false
	missing := false
	budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	for _, fieldID := range requested {
		field, ok := fields[fieldID]
		if !ok || !field.Present {
			missing = true
			continue
		}
		matched, hasValue, invalid := matchInverseReferenceRawJSON(field.Value, target, budget)
		nonempty = nonempty || hasValue
		malformed = malformed || invalid
		if matched {
			matches = append(matches, literalInverseReferenceMatch(issue, source, fieldID))
		}
	}
	switch {
	case malformed || budget.Clipped:
		outcome = malformedInverseReferenceOutcome(source)
	case missing:
		outcome.Status, outcome.Reason = domain.JiraInverseReferenceSourcePartial, domain.JiraInverseReferenceReasonFieldMissing
	case !nonempty:
		outcome.Status = domain.JiraInverseReferenceSourceEmpty
	}
	setInverseReferenceMatchesComplete(matches, outcome)
	return outcome, matches
}

func matchInverseReferenceProperties(issue domain.JiraInverseReferenceIssueIdentity, target inverseReferenceTarget, properties []domain.JiraInverseReferencePropertySnapshot) (domain.JiraInverseReferenceSourceOutcome, []JiraInverseReferenceResultMatch) {
	source := domain.JiraInverseReferenceSourceProperties
	outcome := domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceComplete}
	matches := []JiraInverseReferenceResultMatch{}
	if properties == nil || len(properties) > jiraInverseReferenceMaxFields {
		return malformedInverseReferenceOutcome(source), matches
	}
	if len(properties) == 0 {
		outcome.Status = domain.JiraInverseReferenceSourceEmpty
		return outcome, matches
	}
	properties = append([]domain.JiraInverseReferencePropertySnapshot(nil), properties...)
	sort.Slice(properties, func(i, j int) bool { return properties[i].Key < properties[j].Key })
	seen, malformed := map[string]bool{}, false
	budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	for _, property := range properties {
		if property.Key == "" || len(property.Key) > 256 || containsControl(property.Key) || seen[property.Key] {
			malformed = true
			continue
		}
		seen[property.Key] = true
		matched, _, invalid := matchInverseReferenceRawJSON(property.Value, target, budget)
		malformed = malformed || invalid
		if matched {
			matches = append(matches, literalInverseReferenceMatch(issue, source, ""))
		}
	}
	if malformed || budget.Clipped {
		outcome = malformedInverseReferenceOutcome(source)
	}
	setInverseReferenceMatchesComplete(matches, outcome)
	return outcome, matches
}

func collectInverseReferenceComments(ctx context.Context, tracker domain.Tracker, issue domain.JiraInverseReferenceIssueIdentity, target inverseReferenceTarget) (domain.JiraInverseReferenceSourceOutcome, []JiraInverseReferenceResultMatch, error) {
	source := domain.JiraInverseReferenceSourceComments
	comments, err := tracker.ListComments(ctx, issue.Key)
	if err != nil {
		if cancelErr := inverseReferenceContextError(ctx, err); cancelErr != nil {
			return domain.JiraInverseReferenceSourceOutcome{}, nil, cancelErr
		}
		outcome := classifyInverseReferenceSourceError(err)
		outcome.Source = source
		return outcome, nil, nil
	}
	if comments == nil || len(comments) > inverseReferenceMaxCollectionRows {
		return malformedInverseReferenceOutcome(source), nil, nil
	}
	if len(comments) == 0 {
		return domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceEmpty}, nil, nil
	}
	seen, malformed, matched := map[string]bool{}, false, false
	budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	for _, comment := range comments {
		if !graphNumericIDPattern.MatchString(comment.ID) || seen[comment.ID] {
			malformed = true
			continue
		}
		seen[comment.ID] = true
		if strings.ContainsRune(comment.Body, '\uFFFD') {
			malformed = true
		}
		walkGraphValue(comment.Body, "", true, budget, func(value any, _ string, _ bool) {
			if text, ok := value.(string); ok && inverseReferenceLiteralMatches(text, target) {
				matched = true
			}
		})
	}
	outcome := domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceComplete}
	if malformed || budget.Clipped {
		outcome = malformedInverseReferenceOutcome(source)
	}
	matches := inverseReferenceOptionalLiteralMatch(issue, source, matched)
	setInverseReferenceMatchesComplete(matches, outcome)
	return outcome, matches, nil
}

func collectInverseReferenceWorklogs(ctx context.Context, tracker domain.Tracker, issue domain.JiraInverseReferenceIssueIdentity, target inverseReferenceTarget) (domain.JiraInverseReferenceSourceOutcome, []JiraInverseReferenceResultMatch, error) {
	source := domain.JiraInverseReferenceSourceWorklogs
	reader, ok := tracker.(domain.IssueWorklogReader)
	if !ok {
		return unsupportedInverseReferenceOutcome(source), nil, nil
	}
	inventory, err := reader.ListIssueWorklogs(ctx, issue.Key)
	if err != nil {
		if cancelErr := inverseReferenceContextError(ctx, err); cancelErr != nil {
			return domain.JiraInverseReferenceSourceOutcome{}, nil, cancelErr
		}
		outcome := classifyInverseReferenceSourceError(err)
		outcome.Source = source
		return outcome, nil, nil
	}
	if inventory == nil || inventory.Worklogs == nil || !inventory.Complete || inventory.Total < 0 || inventory.Total != len(inventory.Worklogs) || len(inventory.Worklogs) > inverseReferenceMaxCollectionRows {
		return malformedInverseReferenceOutcome(source), nil, nil
	}
	if inventory.Total == 0 {
		return domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceEmpty}, nil, nil
	}
	seen, malformed, matched := map[string]bool{}, false, false
	budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	for _, worklog := range inventory.Worklogs {
		if !graphNumericIDPattern.MatchString(worklog.ID) || seen[worklog.ID] {
			malformed = true
			continue
		}
		seen[worklog.ID] = true
		if strings.ContainsRune(worklog.Comment, '\uFFFD') {
			malformed = true
		}
		walkGraphValue(worklog.Comment, "", true, budget, func(value any, _ string, _ bool) {
			if text, ok := value.(string); ok && inverseReferenceLiteralMatches(text, target) {
				matched = true
			}
		})
	}
	outcome := domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceComplete}
	if malformed || budget.Clipped {
		outcome = malformedInverseReferenceOutcome(source)
	}
	matches := inverseReferenceOptionalLiteralMatch(issue, source, matched)
	setInverseReferenceMatchesComplete(matches, outcome)
	return outcome, matches, nil
}

func collectInverseReferenceRemoteLinks(ctx context.Context, tracker domain.Tracker, issue domain.JiraInverseReferenceIssueIdentity, target inverseReferenceTarget) (domain.JiraInverseReferenceSourceOutcome, []JiraInverseReferenceResultMatch, error) {
	source := domain.JiraInverseReferenceSourceRemoteLinks
	reader, ok := tracker.(domain.JiraRemoteLinkReader)
	if !ok {
		return unsupportedInverseReferenceOutcome(source), nil, nil
	}
	inventory, err := reader.ReadIssueRemoteLinks(ctx, issue.Key)
	if err != nil {
		if cancelErr := inverseReferenceContextError(ctx, err); cancelErr != nil {
			return domain.JiraInverseReferenceSourceOutcome{}, nil, cancelErr
		}
		outcome := classifyInverseReferenceSourceError(err)
		outcome.Source = source
		return outcome, nil, nil
	}
	if inventory.Total < 0 || inventory.Unsupported < 0 || inventory.Total != len(inventory.Links)+inventory.Unsupported || inventory.Total > inverseReferenceMaxCollectionRows {
		return malformedInverseReferenceOutcome(source), nil, nil
	}
	if inventory.Total == 0 {
		return domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceEmpty}, nil, nil
	}
	seen, malformed := map[string]bool{}, inventory.Unsupported > 0
	matches := []JiraInverseReferenceResultMatch{}
	for _, link := range inventory.Links {
		if !graphNumericIDPattern.MatchString(link.ID) || seen[link.ID] {
			malformed = true
			continue
		}
		seen[link.ID] = true
		if len(link.ObjectURL) > graphURLMaxBytes || len(link.GlobalID) > graphURLMaxBytes ||
			len(link.ApplicationType) > 256 || len(link.Relationship) > 1024 || len(link.ObjectTitle) > 4096 {
			malformed = true
			continue
		}
		switch target.domain.Kind {
		case domain.JiraInverseReferenceTargetGitLabProject:
			if project, ok := inverseReferenceGitLabLiteralProject(link.ObjectURL); ok && project == target.gitlab {
				matches = append(matches, literalInverseReferenceMatch(issue, source, ""))
			}
		case domain.JiraInverseReferenceTargetConfluencePage:
			pageID, local, valid := inverseReferenceConfluenceURLPageID(target.confluence.baseURL, link.ObjectURL)
			if link.ApplicationType == confluenceRemoteApplicationType {
				if !valid || !local {
					malformed = true
					continue
				}
				if link.GlobalID == "" {
					if pageID == target.confluence.pageID {
						matches = append(matches, structuredInverseReferenceFallbackMatch(issue, source))
					}
					continue
				}
				globalPageID, globalValid := inverseReferenceGlobalPageID(link.GlobalID)
				if !globalValid || pageID != globalPageID {
					malformed = true
					continue
				}
				if pageID == target.confluence.pageID {
					matches = append(matches, structuredInverseReferenceMatch(issue, source))
				}
			} else if valid && local && pageID == target.confluence.pageID {
				matches = append(matches, literalInverseReferenceMatch(issue, source, ""))
			}
		}
	}
	outcome := domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceComplete}
	if malformed {
		outcome = malformedInverseReferenceOutcome(source)
	}
	setInverseReferenceMatchesComplete(matches, outcome)
	return outcome, matches, nil
}

func collectInverseReferenceDevelopment(ctx context.Context, tracker domain.Tracker, issue domain.JiraInverseReferenceIssueIdentity, target inverseReferenceTarget) (domain.JiraInverseReferenceSourceOutcome, []JiraInverseReferenceResultMatch, error) {
	source := domain.JiraInverseReferenceSourceDevelopment
	reader, ok := tracker.(domain.JiraDevelopmentReader)
	if !ok {
		return unsupportedInverseReferenceOutcome(source), nil, nil
	}
	if target.domain.Kind != domain.JiraInverseReferenceTargetGitLabProject {
		return domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceEmpty}, nil, nil
	}
	inventory, err := reader.ReadIssueDevelopment(ctx, issue.ID)
	if err != nil {
		if cancelErr := inverseReferenceContextError(ctx, err); cancelErr != nil {
			return domain.JiraInverseReferenceSourceOutcome{}, nil, cancelErr
		}
		outcome := classifyInverseReferenceSourceError(err)
		outcome.Source = source
		return outcome, nil, nil
	}
	projection, valid := validateJiraDevelopmentInventory(inventory)
	if !valid {
		return malformedInverseReferenceOutcome(source), nil, nil
	}
	matched := false
	for _, project := range projection.projects {
		matched = matched || project.Host == target.gitlab.Host && project.ProjectPath == target.gitlab.ProjectPath
	}
	for _, commit := range projection.commits {
		matched = matched || commit.Host == target.gitlab.Host && commit.ProjectPath == target.gitlab.ProjectPath
	}
	for _, branch := range projection.branches {
		matched = matched || branch.Host == target.gitlab.Host && branch.ProjectPath == target.gitlab.ProjectPath
	}
	for _, mergeRequest := range projection.mrs {
		matched = matched || mergeRequest.Host == target.gitlab.Host && mergeRequest.ProjectPath == target.gitlab.ProjectPath
	}
	outcome := domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceComplete}
	if len(projection.projects)+len(projection.commits)+len(projection.branches)+len(projection.mrs) == 0 {
		outcome.Status = domain.JiraInverseReferenceSourceEmpty
	}
	if !matched {
		return outcome, nil, nil
	}
	match := JiraInverseReferenceResultMatch{IssueKey: issue.Key, Relation: JiraInverseReferenceRelationDevelopment,
		Direction: JiraInverseReferenceDirectionIssueToTarget, Source: source,
		Stability: domain.ArtifactStabilityExperimentalAPI, Confidence: "exact", Complete: true}
	return outcome, []JiraInverseReferenceResultMatch{match}, nil
}

func matchInverseReferenceRawJSON(raw json.RawMessage, target inverseReferenceTarget, budget *graphExtractBudget) (bool, bool, bool) {
	if len(raw) == 0 || len(raw) > inverseReferenceMaxValueBytes {
		return false, false, true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false, false, true
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false, false, true
	}
	matched := false
	walkInverseReferenceValue(value, "", true, budget, func(current any, _ string, _ bool) {
		if text, ok := current.(string); ok && inverseReferenceLiteralMatches(text, target) {
			matched = true
		}
	})
	return matched, !inverseReferenceJSONEmpty(value), budget.Clipped
}

func inverseReferenceJSONEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func inverseReferenceLiteralMatches(text string, target inverseReferenceTarget) bool {
	for _, span := range inverseReferenceLiteralURLPattern.FindAllStringIndex(text, -1) {
		raw := strings.TrimRight(text[span[0]:span[1]], ".,;:!?")
		switch target.domain.Kind {
		case domain.JiraInverseReferenceTargetGitLabProject:
			if project, ok := inverseReferenceGitLabLiteralProject(raw); ok && project == target.gitlab {
				return true
			}
		case domain.JiraInverseReferenceTargetConfluencePage:
			pageID, local, valid := inverseReferenceConfluenceURLPageID(target.confluence.baseURL, raw)
			if valid && local && pageID == target.confluence.pageID {
				return true
			}
		}
	}
	return false
}

func inverseReferenceGitLabLiteralProject(raw string) (scmref.GitLabProject, bool) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, utf8.RuneError) {
		return scmref.GitLabProject{}, false
	}
	candidate, err := url.Parse(raw)
	if err != nil || !validInverseReferenceURLComponents(candidate) {
		return scmref.GitLabProject{}, false
	}
	candidate.RawQuery = ""
	candidate.ForceQuery = false
	candidate.Fragment = ""
	candidate.RawFragment = ""
	return scmref.ParseGitLabReference(candidate.String())
}

func inverseReferenceConfluenceURLPageID(baseURL, raw string) (string, bool, bool) {
	base, baseErr := url.Parse(baseURL)
	candidate, err := url.Parse(raw)
	if baseErr != nil || err != nil || !candidate.IsAbs() || candidate.User != nil || candidate.Host == "" ||
		!validInverseReferenceURLComponents(candidate) {
		return "", false, false
	}
	if !sameGraphOrigin(candidate, baseURL) {
		return "", false, true
	}
	refPath, ok := confluenceReferencePath(base, candidate, true)
	if !ok {
		return "", true, false
	}
	id, _, directErr := directConfluencePageID(refPath, candidate.Query())
	if directErr != nil || id == "" {
		return "", true, false
	}
	return id, true, true
}

func inverseReferenceGlobalPageID(globalID string) (string, bool) {
	if globalID == "" || len(globalID) > graphURLMaxBytes {
		return "", false
	}
	values, err := url.ParseQuery(globalID)
	if err != nil {
		return "", false
	}
	pageIDs := values["pageId"]
	if len(pageIDs) != 1 || !isDecimalID(pageIDs[0]) {
		return "", false
	}
	return pageIDs[0], true
}

func literalInverseReferenceMatch(issue domain.JiraInverseReferenceIssueIdentity, source domain.JiraInverseReferenceSource, fieldID string) JiraInverseReferenceResultMatch {
	return JiraInverseReferenceResultMatch{IssueKey: issue.Key, Relation: JiraInverseReferenceRelationLiteral,
		Direction: JiraInverseReferenceDirectionIssueToTarget, Source: source, TechnicalFieldID: fieldID,
		Stability: domain.ArtifactStabilityHeuristic, Confidence: "high"}
}

func structuredInverseReferenceMatch(issue domain.JiraInverseReferenceIssueIdentity, source domain.JiraInverseReferenceSource) JiraInverseReferenceResultMatch {
	return JiraInverseReferenceResultMatch{IssueKey: issue.Key, Relation: JiraInverseReferenceRelationStructuredRemoteLink,
		Direction: JiraInverseReferenceDirectionIssueToTarget, Source: source,
		Stability: domain.ArtifactStabilityPublicAPI, Confidence: "exact"}
}

func structuredInverseReferenceFallbackMatch(issue domain.JiraInverseReferenceIssueIdentity, source domain.JiraInverseReferenceSource) JiraInverseReferenceResultMatch {
	match := structuredInverseReferenceMatch(issue, source)
	match.Confidence = "high"
	return match
}

func inverseReferenceOptionalLiteralMatch(issue domain.JiraInverseReferenceIssueIdentity, source domain.JiraInverseReferenceSource, matched bool) []JiraInverseReferenceResultMatch {
	if !matched {
		return nil
	}
	return []JiraInverseReferenceResultMatch{literalInverseReferenceMatch(issue, source, "")}
}

func setInverseReferenceMatchesComplete(matches []JiraInverseReferenceResultMatch, outcome domain.JiraInverseReferenceSourceOutcome) {
	complete := outcome.Status == domain.JiraInverseReferenceSourceComplete || outcome.Status == domain.JiraInverseReferenceSourceEmpty
	for index := range matches {
		matches[index].Complete = complete
	}
}

func inverseReferenceContextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func malformedInverseReferenceOutcome(source domain.JiraInverseReferenceSource) domain.JiraInverseReferenceSourceOutcome {
	return domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourcePartial, Reason: domain.JiraInverseReferenceReasonMalformed}
}

func unsupportedInverseReferenceOutcome(source domain.JiraInverseReferenceSource) domain.JiraInverseReferenceSourceOutcome {
	return domain.JiraInverseReferenceSourceOutcome{Source: source, Status: domain.JiraInverseReferenceSourceUnsupported, Reason: domain.JiraInverseReferenceReasonNotSupported}
}
