package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	confluenceJiraMacroSchema         = 1
	confluenceJiraMacroLimit          = 1000
	confluenceJiraMacroQueriesPerPage = 20
	confluenceJiraMacroRowsPerPage    = 2000
	confluenceJiraMacroSidecarMax     = 32 << 20
)

type confluenceJiraMacroEntry struct {
	Index int        `json:"index"`
	List  *IssueList `json:"list"`
}

type confluenceJiraMacroSidecar struct {
	SchemaVersion int                        `json:"schema_version"`
	PageID        string                     `json:"page_id"`
	MacroHash     string                     `json:"macro_hash"`
	Entries       []confluenceJiraMacroEntry `json:"entries"`
}

func confluenceJiraMacroPath(dir, slug string) string {
	return filepath.Join(dir, slug+".jira-macros.json")
}

func jiraMacroDescriptorHash(descriptors []mirror.JiraMacroDescriptor) string {
	b, _ := json.Marshal(descriptors)
	return mirror.Hash(b)
}

func (s *ConfluenceService) validateConfluenceJiraView(view string, expand bool) error {
	if strings.TrimSpace(view) == "" {
		return nil
	}
	if !expand {
		return fmt.Errorf("%w: --jira-view cannot be combined with disabled Jira macro expansion", domain.ErrUsage)
	}
	var views map[string]config.JiraListView
	if s != nil && s.cfg != nil {
		views = s.cfg.JiraListViews
	}
	if _, _, err := config.ResolveJiraListView(views, view, config.JiraListSourceConfluenceMacro); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	return nil
}

func (s *ConfluenceService) resolveConfluenceJiraMacros(ctx context.Context, pageID string, node *csf.Node, view string) (*confluenceJiraMacroSidecar, []string) {
	descriptors := mirror.JiraMacroDescriptors(node)
	if len(descriptors) == 0 {
		return nil, nil
	}
	s.jiraReadOnce.Do(func() {
		if s.jiraRead == nil && s.jiraReadFactory != nil {
			s.jiraRead, s.jiraReadReason = s.jiraReadFactory()
			s.jiraReadFactory = nil
		}
	})
	if s.jiraRead == nil {
		reason := s.jiraReadReason
		if reason == "" {
			reason = "Jira read access is not configured"
		}
		return nil, []string{fmt.Sprintf("render: %d Jira query macro(s) kept as placeholders: %s", len(descriptors), reason)}
	}
	jiraService := &JiraService{tr: s.jiraRead, cfg: s.cfg}
	sidecar := &confluenceJiraMacroSidecar{SchemaVersion: confluenceJiraMacroSchema, PageID: pageID, MacroHash: jiraMacroDescriptorHash(descriptors), Entries: []confluenceJiraMacroEntry{}}
	warnings := []string{}
	failed := 0
	reportedFailures := 0
	remainingRows := confluenceJiraMacroRowsPerPage
	processed := 0
	for descriptorPosition, descriptor := range descriptors {
		if processed >= confluenceJiraMacroQueriesPerPage || remainingRows <= 0 {
			warnings = append(warnings, fmt.Sprintf("render: %d Jira query macro(s) omitted by the page safety cap; placeholders retained", len(descriptors)-descriptorPosition))
			break
		}
		processed++
		columns := normalizeConfluenceJiraMacroColumns(descriptor.Columns)
		limit := descriptor.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > confluenceJiraMacroLimit {
			limit = confluenceJiraMacroLimit
		}
		if limit > remainingRows {
			limit = remainingRows
		}
		list, err := collectConfluenceJiraMacro(ctx, jiraService, descriptor.JQL, columns, view, limit)
		if err != nil {
			failed++
			if reportedFailures < 5 {
				warnings = append(warnings, fmt.Sprintf("render: Jira query macro %d could not be resolved; placeholder retained", descriptor.Index+1))
				reportedFailures++
			}
			continue
		}
		sidecar.Entries = append(sidecar.Entries, confluenceJiraMacroEntry{Index: descriptor.Index, List: list})
		remainingRows -= len(list.Rows)
	}
	if failed > reportedFailures {
		warnings = append(warnings, fmt.Sprintf("render: %d additional Jira query macro failures omitted", failed-reportedFailures))
	}
	if len(sidecar.Entries) == 0 {
		return nil, warnings
	}
	return sidecar, warnings
}

func collectConfluenceJiraMacro(ctx context.Context, service *JiraService, jql string, columns []string, view string, limit int) (*IssueList, error) {
	var aggregate *IssueList
	cursor := ""
	for total := 0; total < limit; {
		pageSize := min(100, limit-total)
		page, err := service.searchIssueListSourceView(ctx, jql, columns, view, config.JiraListSourceConfluenceMacro, pageSize, cursor)
		if err != nil {
			return nil, err
		}
		pageRows := page.Rows
		if aggregate == nil {
			aggregate = page
			aggregate.Rows = []IssueListRow{}
		}
		for _, row := range pageRows {
			row.Position = len(aggregate.Rows)
			aggregate.Rows = append(aggregate.Rows, row)
		}
		total += len(pageRows)
		if page.Page.NextCursor == nil || len(pageRows) == 0 {
			aggregate.Page = IssueListPage{Count: len(aggregate.Rows), Complete: true, Truncated: false, NextCursor: nil}
			return aggregate, nil
		}
		cursor = *page.Page.NextCursor
	}
	if aggregate == nil {
		return nil, fmt.Errorf("empty Jira macro result")
	}
	aggregate.Page.Count = len(aggregate.Rows)
	aggregate.Page.Complete = false
	aggregate.Page.Truncated = true
	aggregate.Page.NextCursor = nil // sidecar snapshots are refreshed as a unit
	return aggregate, nil
}

func normalizeConfluenceJiraMacroColumns(columns []string) []string {
	aliases := map[string]string{
		"type": "issuetype", "due": "duedate", "due date": "duedate",
		"issue type": "issuetype", "issue key": "key",
	}
	out := make([]string, 0, len(columns))
	seen := map[string]bool{}
	for _, raw := range columns {
		column := strings.TrimSpace(raw)
		if alias := aliases[strings.ToLower(column)]; alias != "" {
			column = alias
		}
		if column != "" && !seen[column] {
			seen[column] = true
			out = append(out, column)
		}
	}
	return out
}

func writeConfluenceJiraMacroSidecar(root, dir, slug string, sidecar *confluenceJiraMacroSidecar) error {
	path := confluenceJiraMacroPath(dir, slug)
	if sidecar == nil {
		if err := safepath.RemoveWithin(root, path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > confluenceJiraMacroSidecarMax {
		return fmt.Errorf("jira macro sidecar exceeds %d-byte safety cap", confluenceJiraMacroSidecarMax)
	}
	return safepath.WriteFileWithin(root, path, append(b, '\n'), 0o600)
}

func readConfluenceJiraMacroSidecar(root, dir, slug, pageID string, node *csf.Node) (*confluenceJiraMacroSidecar, error) {
	b, err := safepath.ReadFileWithinLimit(root, confluenceJiraMacroPath(dir, slug), confluenceJiraMacroSidecarMax)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sidecar confluenceJiraMacroSidecar
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sidecar); err != nil || decoder.Decode(new(any)) != io.EOF || sidecar.SchemaVersion != confluenceJiraMacroSchema || sidecar.PageID != pageID {
		return nil, fmt.Errorf("invalid Jira macro sidecar")
	}
	descriptors := mirror.JiraMacroDescriptors(node)
	if sidecar.MacroHash != jiraMacroDescriptorHash(descriptors) {
		return nil, fmt.Errorf("stale Jira macro sidecar")
	}
	validIndices := make(map[int]bool, len(descriptors))
	for _, descriptor := range descriptors {
		validIndices[descriptor.Index] = true
	}
	seenIndices := map[int]bool{}
	rows := 0
	for _, entry := range sidecar.Entries {
		if !validIndices[entry.Index] || seenIndices[entry.Index] || entry.List == nil || entry.List.SchemaVersion != 1 {
			return nil, fmt.Errorf("invalid Jira macro sidecar entry")
		}
		seenIndices[entry.Index] = true
		rows += len(entry.List.Rows)
		if rows > confluenceJiraMacroRowsPerPage {
			return nil, fmt.Errorf("jira macro sidecar exceeds row safety cap")
		}
	}
	return &sidecar, nil
}

func confluenceJiraMacroViews(sidecar *confluenceJiraMacroSidecar) []mirror.JiraMacroView {
	if sidecar == nil {
		return nil
	}
	views := make([]mirror.JiraMacroView, 0, len(sidecar.Entries))
	for _, entry := range sidecar.Entries {
		if entry.List == nil {
			continue
		}
		views = append(views, mirror.JiraMacroView{Index: entry.Index, Markdown: IssueListMarkdown(entry.List, true), Complete: entry.List.Page.Complete, Truncated: entry.List.Page.Truncated})
	}
	return views
}

func addConfluenceJiraMacrosFromSidecar(opts *mirror.MDViewOpts, root, dir, slug, pageID string, node *csf.Node) error {
	sidecar, err := readConfluenceJiraMacroSidecar(root, dir, slug, pageID, node)
	if err != nil {
		return err
	}
	opts.JiraMacros = confluenceJiraMacroViews(sidecar)
	return nil
}

// confMDViewOptsFromSidecars is the single constructor for a durable
// Confluence view backed by recorded local sidecars. Keeping comments, page
// fields, and Jira-query snapshots together prevents one writer path from
// accidentally emitting a different pristine prefix/suffix than apply later
// reconstructs.
func confMDViewOptsFromSidecars(rs RenderSettings, page *domain.Resource, comments []domain.Comment, root, dir, slug, pageID string, node *csf.Node) (mirror.MDViewOpts, error) {
	opts := confMDViewOpts(rs, page, comments)
	if len(mirror.JiraMacroDescriptors(node)) == 0 {
		// A sidecar cannot be meaningful once the native page contains no Jira
		// query macro. Ignore that generated orphan without mutating here: this
		// constructor is also used by dry-run apply. Pull and a successful
		// loss-approved apply retire the file on their explicit mutation paths.
		return opts, nil
	}
	if err := addConfluenceJiraMacrosFromSidecar(&opts, root, dir, slug, pageID, node); err != nil {
		return opts, err
	}
	return opts, nil
}
