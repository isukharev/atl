package mcpserver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func validatedStructureViewInput(in JiraStructureViewInput) ([]string, int, int, app.StructureFolderSelector, error) {
	if in.StructureID <= 0 {
		return nil, 0, 0, app.StructureFolderSelector{}, fmt.Errorf("%w: structure_id must be positive", domain.ErrUsage)
	}
	selector := app.StructureFolderSelector{
		FolderID: strings.TrimSpace(in.FolderID), FolderRow: in.FolderRow, FolderPath: strings.TrimSpace(in.FolderPath),
	}
	if len(selector.FolderID) > jiraStructureFolderIDMaxBytes || len(selector.FolderPath) > jiraStructureFolderPathMaxBytes {
		return nil, 0, 0, app.StructureFolderSelector{}, fmt.Errorf("%w: Structure folder selector is too long", domain.ErrUsage)
	}
	selectorCount := 0
	if selector.FolderID != "" {
		selectorCount++
	}
	if selector.FolderRow != 0 {
		selectorCount++
	}
	if selector.FolderPath != "" {
		selectorCount++
	}
	if selectorCount > 1 || selector.FolderRow < 0 {
		return nil, 0, 0, app.StructureFolderSelector{}, fmt.Errorf("%w: folder_id, folder_row, and folder_path are mutually exclusive and folder_row must be positive", domain.ErrUsage)
	}
	if selector.FolderPath != "" {
		if _, err := normalizedStructureFolderPath(selector.FolderPath); err != nil {
			return nil, 0, 0, app.StructureFolderSelector{}, err
		}
	}
	maxRows, err := boundedDefault(in.MaxRows, jiraStructureViewDefaultMaxRows, jiraStructureViewMaxMaxRows, "max_rows")
	if err != nil {
		return nil, 0, 0, app.StructureFolderSelector{}, err
	}
	maxBytes, err := boundedBytes(in.MaxBytes, jiraStructureViewDefaultMaxBytes,
		jiraStructureViewMinMaxBytes, jiraStructureViewMaxMaxBytes)
	if err != nil {
		return nil, 0, 0, app.StructureFolderSelector{}, err
	}
	fields := in.Fields
	if len(fields) == 0 {
		fields = []string{"key", "summary", "status", "assignee"}
	}
	if len(fields) > jiraStructureViewMaxFields {
		return nil, 0, 0, app.StructureFolderSelector{}, fmt.Errorf("%w: fields must contain at most %d Jira field ids", domain.ErrUsage, jiraStructureViewMaxFields)
	}
	normalized := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || len(field) > jiraStructureFieldIDMaxBytes || field == "position" || field == "id" || strings.Contains(field, ".") {
			return nil, 0, 0, app.StructureFolderSelector{}, fmt.Errorf("%w: fields must contain Jira field ids only", domain.ErrUsage)
		}
		if _, exists := seen[field]; exists {
			return nil, 0, 0, app.StructureFolderSelector{}, fmt.Errorf("%w: fields must be unique", domain.ErrUsage)
		}
		seen[field] = struct{}{}
		normalized = append(normalized, field)
	}
	return normalized, maxRows, maxBytes, selector, nil
}

func validatedExpectedStructureForestVersion(in JiraStructureViewInput) (*domain.StructureVersion, error) {
	signatureSet := in.ExpectedForestSignature != nil
	versionSet := in.ExpectedForestVersion != nil
	if signatureSet != versionSet {
		return nil, fmt.Errorf("%w: expected_forest_signature and expected_forest_version must be supplied together", domain.ErrUsage)
	}
	if !signatureSet {
		return nil, nil
	}
	if *in.ExpectedForestSignature == 0 || *in.ExpectedForestVersion < 1 {
		return nil, fmt.Errorf("%w: expected_forest_signature must be nonzero and expected_forest_version must be positive", domain.ErrUsage)
	}
	return &domain.StructureVersion{Signature: *in.ExpectedForestSignature, Version: *in.ExpectedForestVersion}, nil
}

func validateStructureView(snapshot *app.StructureSnapshot, structureID int64, fields []string, maxRows int, selector app.StructureFolderSelector, expectedForestVersion *domain.StructureVersion) error {
	if snapshot == nil || snapshot.SchemaVersion != 1 || snapshot.Structure.ID != structureID || strings.TrimSpace(snapshot.Structure.Name) == "" ||
		snapshot.ForestVersionGated != (expectedForestVersion != nil) ||
		(expectedForestVersion != nil && snapshot.ForestVersion != *expectedForestVersion) ||
		snapshot.RowCount != len(snapshot.Rows) || snapshot.RowCount > maxRows || snapshot.IssueCount < 0 ||
		snapshot.Projection.Kind != "jira-fields-v1" || snapshot.Projection.BrowserViewReproduced || !reflect.DeepEqual(snapshot.Projection.Attributes, fields) {
		return fmt.Errorf("%w: Structure view is not reconciled", domain.ErrCheckFailed)
	}
	wantSelection := selector.FolderID != "" || selector.FolderRow != 0 || selector.FolderPath != ""
	if wantSelection != (snapshot.Selection != nil) {
		return fmt.Errorf("%w: Structure subtree selection is not reconciled", domain.ErrCheckFailed)
	}
	if snapshot.Selection != nil {
		switch {
		case selector.FolderID != "" && (snapshot.Selection.Kind != "folder-id" || snapshot.Selection.FolderID != selector.FolderID):
			return fmt.Errorf("%w: Structure folder selection is not reconciled", domain.ErrCheckFailed)
		case selector.FolderRow != 0 && (snapshot.Selection.Kind != "folder-row" || snapshot.Selection.RowID != selector.FolderRow):
			return fmt.Errorf("%w: Structure folder selection is not reconciled", domain.ErrCheckFailed)
		case selector.FolderPath != "":
			wanted, err := normalizedStructureFolderPath(selector.FolderPath)
			if err != nil || snapshot.Selection.Kind != "folder-path" || normalizedStructureSelectionPath(snapshot.Selection.Path) != wanted {
				return fmt.Errorf("%w: Structure folder selection is not reconciled", domain.ErrCheckFailed)
			}
		}
	}
	rows := make(map[int64]app.StructureSnapshotRow, len(snapshot.Rows))
	issueIDs := make(map[string]struct{})
	for _, row := range snapshot.Rows {
		if row.RowID <= 0 || row.Depth < 0 || strings.TrimSpace(row.ItemType) == "" || strings.TrimSpace(row.ItemID) == "" {
			return fmt.Errorf("%w: Structure row identity is invalid", domain.ErrCheckFailed)
		}
		if _, duplicate := rows[row.RowID]; duplicate {
			return fmt.Errorf("%w: Structure row ids are not unique", domain.ErrCheckFailed)
		}
		rows[row.RowID] = row
		if row.ItemType == "issue" {
			issueIDs[row.ItemID] = struct{}{}
		}
		if len(row.Values) != len(fields) {
			return fmt.Errorf("%w: Structure row projection is not reconciled", domain.ErrCheckFailed)
		}
		for _, field := range fields {
			if _, exists := row.Values[field]; !exists {
				return fmt.Errorf("%w: Structure row projection is not reconciled", domain.ErrCheckFailed)
			}
		}
	}
	if snapshot.Selection != nil {
		if len(snapshot.Rows) == 0 {
			return fmt.Errorf("%w: Structure subtree root is not reconciled", domain.ErrCheckFailed)
		}
		root := snapshot.Rows[0]
		if root.RowID != snapshot.Selection.RowID || root.ItemID != snapshot.Selection.FolderID ||
			!strings.EqualFold(strings.TrimSpace(root.ItemType), "folder") || root.RelativeDepth == nil || *root.RelativeDepth != 0 {
			return fmt.Errorf("%w: Structure subtree root is not reconciled", domain.ErrCheckFailed)
		}
		for index, row := range snapshot.Rows {
			if row.RelativeDepth == nil || index == 0 && *row.RelativeDepth != 0 || index > 0 && *row.RelativeDepth <= 0 {
				return fmt.Errorf("%w: Structure subtree depth is not reconciled", domain.ErrCheckFailed)
			}
		}
	}
	if snapshot.IssueCount != len(issueIDs) {
		return fmt.Errorf("%w: Structure issue count is not reconciled", domain.ErrCheckFailed)
	}
	inaccessible := make(map[int64]struct{}, len(snapshot.InaccessibleRows))
	for _, rowID := range snapshot.InaccessibleRows {
		row, exists := rows[rowID]
		if !exists || row.Accessible {
			return fmt.Errorf("%w: Structure inaccessible rows are not reconciled", domain.ErrCheckFailed)
		}
		if _, duplicate := inaccessible[rowID]; duplicate {
			return fmt.Errorf("%w: Structure inaccessible rows are not unique", domain.ErrCheckFailed)
		}
		inaccessible[rowID] = struct{}{}
	}
	for _, row := range snapshot.Rows {
		_, listed := inaccessible[row.RowID]
		if !row.Accessible && !listed {
			return fmt.Errorf("%w: Structure accessibility is not reconciled", domain.ErrCheckFailed)
		}
	}
	if (snapshot.Complete && len(inaccessible) != 0) || (!snapshot.Complete && len(inaccessible) == 0 && len(snapshot.Warnings) == 0) {
		return fmt.Errorf("%w: Structure completeness is not reconciled", domain.ErrCheckFailed)
	}
	return nil
}

func normalizedStructureFolderPath(path string) (string, error) {
	parts := strings.Split(path, "/")
	normalized := make([]string, len(parts))
	for i, part := range parts {
		part = strings.Join(strings.Fields(part), " ")
		if part == "" {
			return "", fmt.Errorf("%w: folder_path contains an empty segment", domain.ErrUsage)
		}
		normalized[i] = strings.ToLower(part)
	}
	return strings.Join(normalized, "/"), nil
}

func normalizedStructureSelectionPath(parts []string) string {
	normalized := make([]string, len(parts))
	for i, part := range parts {
		normalized[i] = strings.ToLower(strings.Join(strings.Fields(part), " "))
		if normalized[i] == "" {
			return ""
		}
	}
	return strings.Join(normalized, "/")
}

func boundedStructureOutput(value *app.StructureSnapshot, maxBytes int) error {
	if err := availableResult(value, "Structure result"); err != nil {
		return err
	}
	return boundedOutput(value, maxBytes,
		"encode Structure result",
		"Structure result exceeds max_bytes; select an exact subtree or raise the bound")
}

// boundedStructureMetadataOutput stays explicit: its bound is a fixed
// projection ceiling on a closed metadata record, not a caller-supplied
// max_bytes, so an oversize result is a failed check with nothing for the
// client to raise and deliberately does not carry domain.ErrOutputLimit.
func boundedStructureMetadataOutput(value *app.StructureMetadataResult) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode Structure metadata", domain.ErrCheckFailed)
	}
	if len(encoded) > jiraStructureMetadataMaxBytes {
		return fmt.Errorf("%w: Structure metadata exceeds the output bound", domain.ErrCheckFailed)
	}
	return nil
}

func validatedJiraIssueRefsInput(in JiraIssueRefsInput) (app.JiraIssueRefsOpts, int, error) {
	key := strings.TrimSpace(in.Key)
	jql := strings.TrimSpace(in.JQL)
	if (key == "") == (jql == "") {
		return app.JiraIssueRefsOpts{}, 0, fmt.Errorf("%w: supply exactly one of key or jql", domain.ErrUsage)
	}
	if key != "" && in.Limit != 0 {
		return app.JiraIssueRefsOpts{}, 0, fmt.Errorf("%w: limit is valid only with jql", domain.ErrUsage)
	}
	if jql != "" && (in.Limit < 1 || in.Limit > jiraIssueRefsMaxIssues) {
		return app.JiraIssueRefsOpts{}, 0, fmt.Errorf("%w: jql mode requires limit from 1 to %d", domain.ErrUsage, jiraIssueRefsMaxIssues)
	}
	if len(in.Fields) > jiraIssueRefsMaxFields {
		return app.JiraIssueRefsOpts{}, 0, fmt.Errorf("%w: fields must contain at most %d technical ids", domain.ErrUsage, jiraIssueRefsMaxFields)
	}
	fields := make([]string, 0, len(in.Fields))
	seen := make(map[string]struct{}, len(in.Fields))
	for _, field := range in.Fields {
		if !validJiraIssueRefsFieldID(field) {
			return app.JiraIssueRefsOpts{}, 0, fmt.Errorf("%w: fields must contain exact technical Jira field ids", domain.ErrUsage)
		}
		if _, duplicate := seen[field]; duplicate {
			return app.JiraIssueRefsOpts{}, 0, fmt.Errorf("%w: fields must be unique", domain.ErrUsage)
		}
		seen[field] = struct{}{}
		fields = append(fields, field)
	}
	if !app.JiraTechnicalFieldIDs(fields) {
		return app.JiraIssueRefsOpts{}, 0, fmt.Errorf("%w: fields must contain exact technical Jira field ids", domain.ErrUsage)
	}
	maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
	if err != nil {
		return app.JiraIssueRefsOpts{}, 0, err
	}
	return app.JiraIssueRefsOpts{Key: key, JQL: jql, Fields: fields, Limit: in.Limit}, maxBytes, nil
}

func validJiraIssueRefsFieldID(field string) bool {
	if field == "" || field != strings.TrimSpace(field) || len([]byte(field)) > jiraStructureFieldIDMaxBytes {
		return false
	}
	for _, char := range field {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validateJiraIssueRefsView(view *app.JiraIssueRefsView, opts app.JiraIssueRefsOpts) error {
	if view == nil || view.SchemaVersion != 1 || view.Count != len(view.Issues) ||
		view.Selection.Count != view.Count || view.Summary.IssueCount != view.Count ||
		!view.Summary.CountMatchesIssues || !view.Summary.SelectionCountMatchesIssues ||
		!view.Summary.ReferenceCountMatchesKinds || !view.Summary.IssueSummariesReconciled ||
		!view.Summary.CompleteMatchesInputs || !view.Summary.TruncatedMatchesInputs {
		return fmt.Errorf("%w: Jira issue reference summary is not reconciled", domain.ErrCheckFailed)
	}
	if opts.Key != "" {
		if view.Selection.Mode != "key" || view.Selection.Limit != 0 || view.Count != 1 ||
			!view.Selection.Complete || view.Selection.Truncated || view.Selection.Warning != "" {
			return fmt.Errorf("%w: Jira issue reference key selection is not reconciled", domain.ErrCheckFailed)
		}
	} else if view.Selection.Mode != "jql" || view.Selection.Limit != opts.Limit || view.Count > opts.Limit {
		return fmt.Errorf("%w: Jira issue reference JQL selection is not reconciled", domain.ErrCheckFailed)
	}
	if !validJiraIssueRefsSelectionWarning(view.Selection) {
		return fmt.Errorf("%w: Jira issue reference selection warning is not recognized", domain.ErrCheckFailed)
	}

	referenceKinds := map[string]int{}
	sourceValues := map[string]int{}
	completeIssues, incompleteIssues := 0, 0
	completeSources, incompleteSources, truncatedSources := 0, 0, 0
	references, sources := 0, 0
	seenKeys := make(map[string]struct{}, len(view.Issues))
	anyIssueTruncated := false
	allIssuesComplete := true
	for _, issue := range view.Issues {
		if strings.TrimSpace(issue.Key) == "" {
			return fmt.Errorf("%w: Jira issue reference key is unavailable", domain.ErrCheckFailed)
		}
		if _, duplicate := seenKeys[issue.Key]; duplicate {
			return fmt.Errorf("%w: Jira issue reference keys are not unique", domain.ErrCheckFailed)
		}
		seenKeys[issue.Key] = struct{}{}
		summary := issue.ReferenceSummary
		if summary.ReferenceCount < 0 || summary.SourceCount < 0 ||
			!summary.ReferenceCountMatchesKinds || !summary.CompleteMatchesSources || !summary.TruncatedMatchesSources ||
			summary.ReferenceCount != sumNonnegativeCounts(summary.ReferenceKindCounts) ||
			summary.SourceCount != len(issue.Sources) ||
			summary.SourceCount != sumSourceClassCounts(summary) {
			return fmt.Errorf("%w: per-issue reference summary is not reconciled", domain.ErrCheckFailed)
		}
		issueComplete := true
		issueTruncated := false
		issueCompleteSources, issueIncompleteSources, issueTruncatedSources := 0, 0, 0
		for name, source := range issue.Sources {
			sourceCount, sourceCountExists := summary.SourceValueCounts[name]
			if !validJiraIssueRefsSourceName(name, opts.Fields) || source.Count < 0 || !sourceCountExists ||
				sourceCount != source.Count || !validJiraIssueRefsSourceWarning(name, source) {
				return fmt.Errorf("%w: Jira issue reference sources are not reconciled", domain.ErrCheckFailed)
			}
			sources++
			sourceValues[name] += source.Count
			if source.Complete {
				completeSources++
				issueCompleteSources++
			} else {
				incompleteSources++
				issueIncompleteSources++
				issueComplete = false
			}
			if source.TextTruncated {
				truncatedSources++
				issueTruncatedSources++
				issueTruncated = true
			}
		}
		if len(summary.SourceValueCounts) != len(issue.Sources) ||
			summary.CompleteSourceCount != issueCompleteSources ||
			summary.IncompleteSourceCount != issueIncompleteSources ||
			summary.TruncatedSourceCount != issueTruncatedSources ||
			issue.Complete != issueComplete || issue.Truncated != issueTruncated {
			return fmt.Errorf("%w: Jira issue reference source qualification is not reconciled", domain.ErrCheckFailed)
		}
		references += summary.ReferenceCount
		for kind, count := range summary.ReferenceKindCounts {
			if !app.JiraPlanningReferenceKind(kind) || count < 0 {
				return fmt.Errorf("%w: Jira issue reference kind counts are invalid", domain.ErrCheckFailed)
			}
			referenceKinds[kind] += count
		}
		if issue.Complete {
			completeIssues++
		} else {
			incompleteIssues++
			allIssuesComplete = false
		}
		anyIssueTruncated = anyIssueTruncated || issue.Truncated
	}
	summary := view.Summary
	if summary.CompleteIssueCount != completeIssues || summary.IncompleteIssueCount != incompleteIssues ||
		summary.ReferenceCount != references || !reflect.DeepEqual(summary.ReferenceKindCounts, referenceKinds) ||
		summary.SourceCount != sources || !reflect.DeepEqual(summary.SourceValueCounts, sourceValues) ||
		summary.CompleteSourceCount != completeSources || summary.IncompleteSourceCount != incompleteSources ||
		summary.TruncatedSourceCount != truncatedSources ||
		view.Complete != (view.Selection.Complete && allIssuesComplete) ||
		view.Truncated != (view.Selection.Truncated || anyIssueTruncated) {
		return fmt.Errorf("%w: top-level Jira issue reference summary is not reconciled", domain.ErrCheckFailed)
	}
	expectedWarnings := make([]string, 0, 2)
	if view.Selection.Warning != "" {
		expectedWarnings = append(expectedWarnings, view.Selection.Warning)
	}
	if incompleteIssues > 0 {
		expectedWarnings = append(expectedWarnings, fmt.Sprintf(app.JiraIssueRefsWarningIncompleteSourcesFormat, incompleteIssues))
	}
	if !slices.Equal(view.Warnings, expectedWarnings) {
		return fmt.Errorf("%w: Jira issue reference warnings are not reconciled", domain.ErrCheckFailed)
	}
	return nil
}

func validJiraIssueRefsSelectionWarning(selection app.JiraIssueRefsSelectionView) bool {
	switch selection.Warning {
	case "":
		return selection.Complete && !selection.Truncated
	case app.JiraIssueRefsWarningSelectionLimit:
		return !selection.Complete && selection.Truncated
	case app.JiraIssueRefsWarningPaginationNoProgress,
		app.JiraIssueRefsWarningPaginationRepeated:
		return !selection.Complete && !selection.Truncated
	default:
		return false
	}
}

func validJiraIssueRefsSourceWarning(name string, source app.JiraIssueRefsSourceView) bool {
	if source.Complete {
		return source.Warning == "" && !source.TextTruncated
	}
	switch source.Warning {
	case app.JiraIssueRefsWarningSourceTextCap:
		return source.TextTruncated
	case app.JiraIssueRefsWarningCommentsPartial:
		return name == "comments" && !source.TextTruncated
	case app.JiraIssueRefsWarningCommentsPartial + "; " + app.JiraIssueRefsWarningSourceTextCap:
		return name == "comments" && source.TextTruncated
	case app.JiraIssueRefsWarningFieldAbsent:
		return strings.HasPrefix(name, "field.") && !source.TextTruncated
	default:
		return false
	}
}

func validJiraIssueRefsSourceName(name string, fields []string) bool {
	if name == "comments" || name == "description" {
		return true
	}
	if !strings.HasPrefix(name, "field.") {
		return false
	}
	field := strings.TrimPrefix(name, "field.")
	return slices.Contains(fields, field)
}

func sumNonnegativeCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		if count < 0 {
			return -1
		}
		total += count
	}
	return total
}

func sumSourceClassCounts(summary app.JiraIssueReferenceSummary) int {
	if summary.CompleteSourceCount < 0 || summary.IncompleteSourceCount < 0 || summary.TruncatedSourceCount < 0 {
		return -1
	}
	return summary.CompleteSourceCount + summary.IncompleteSourceCount
}

func boundedJiraEvidenceBytes(value int) (int, error) {
	return boundedBytes(value, jiraEvidenceDefaultMaxBytes, jiraEvidenceMinMaxBytes, jiraEvidenceMaxMaxBytes)
}

func boundedJiraEvidenceOutput(value any, maxBytes int) error {
	if err := availableResult(value, "Jira evidence result"); err != nil {
		return err
	}
	return boundedOutput(value, maxBytes,
		"encode Jira evidence result",
		"Jira evidence result exceeds max_bytes; narrow the selection or raise the bound")
}
