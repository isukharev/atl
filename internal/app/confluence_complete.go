package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	confluenceCompletePullService    = "confluence"
	confluenceCompletePullBatch      = 25
	confluenceCompletePullMaxIDs     = 1_000_000
	confluenceCompletePullMaxIDBytes = 256
)

// CompletePullResult qualifies the exact selector snapshot consumed by one
// complete pull. Pages contains only bodies fetched by this invocation;
// Completed includes the durable prefix recovered from a previous invocation.
type CompletePullResult struct {
	SelectorSHA256   string `json:"selector_sha256"`
	SelectionSHA256  string `json:"selection_sha256"`
	Source           string `json:"source"`
	Complete         bool   `json:"complete"`
	Total            int    `json:"total"`
	Completed        int    `json:"completed"`
	Remaining        int    `json:"remaining"`
	CheckpointActive bool   `json:"checkpoint_active"`
	ViewMigrations   int    `json:"view_migrations,omitempty"`
}

type completePullBinding struct {
	Assets           bool             `json:"assets"`
	Comments         bool             `json:"comments"`
	Render           mirror.ViewState `json:"render"`
	ExpandJiraMacros bool             `json:"expand_jira_macros"`
	JiraView         string           `json:"jira_view,omitempty"`
	JiraMacroColumns []string         `json:"jira_macro_columns,omitempty"`
}

type confluenceCompleteSelection struct {
	checkpoint mirror.CompletePullCheckpoint
	nextIndex  int
	savedIndex int
	result     *CompletePullResult
}

func confluenceCompleteHashJSON(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func completePullOptionsHash(cfg *config.Config, o PullOpts, rs RenderSettings) (string, error) {
	binding := completePullBinding{
		Assets: o.Assets, Comments: o.Comments, Render: viewStateOf(rs), ExpandJiraMacros: rs.ExpandJiraMacros,
	}
	if rs.ExpandJiraMacros {
		var views map[string]config.JiraListView
		if cfg != nil {
			views = cfg.JiraListViews
		}
		columns, name, err := config.ResolveJiraListView(views, o.JiraView, config.JiraListSourceConfluenceMacro)
		if err != nil {
			return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
		}
		binding.JiraView = name
		binding.JiraMacroColumns = columns
	}
	return confluenceCompleteHashJSON(binding)
}

func completePullSelector(o PullOpts) (selector, query string, err error) {
	selector, err = confluenceSelectorForMode(o, "complete")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(o.CQL) != "" {
		query = "(" + selector + ") and type = page"
	} else {
		query = selector
	}
	return selector, query, nil
}

func (s *ConfluenceService) prepareCompletePull(ctx context.Context, m *mirror.Mirror, o PullOpts, rs RenderSettings) (*confluenceCompleteSelection, error) {
	selector, query, err := completePullSelector(o)
	if err != nil {
		return nil, err
	}
	selectorSHA256 := selectorHash(selector)
	optionsSHA256, err := completePullOptionsHash(s.cfg, o, rs)
	if err != nil {
		return nil, err
	}
	checkpoint, found, err := m.CompletePullCheckpoint(selectorSHA256)
	if err != nil {
		return nil, err
	}
	if found && !o.RestartComplete {
		if checkpoint.Service != confluenceCompletePullService || checkpoint.SelectorSHA256 != selectorSHA256 {
			return nil, fmt.Errorf("%w: complete-pull checkpoint does not match its selector", domain.ErrCheckFailed)
		}
		if checkpoint.OptionsSHA256 != optionsSHA256 {
			return nil, fmt.Errorf("%w: complete-pull options changed since the checkpoint; rerun with the original assets/comments/render/Jira-view settings or pass --restart-complete after preserving local edits", domain.ErrCheckFailed)
		}
		selectionSHA256, hashErr := confluenceCompleteHashJSON(checkpoint.IDs)
		if hashErr != nil || selectionSHA256 != checkpoint.SelectionSHA256 || !sort.StringsAreSorted(checkpoint.IDs) {
			return nil, fmt.Errorf("%w: complete-pull checkpoint selection identity is invalid", domain.ErrCheckFailed)
		}
		remaining := checkpoint.IDs[checkpoint.NextIndex:]
		migrations, preflightErr := preflightConfluenceOverwrite(m, remaining)
		if preflightErr != nil {
			return nil, preflightErr
		}
		return newCompleteSelection(checkpoint, "resumed", migrations), nil
	}

	searcher, ok := s.store.(domain.CompletePageSearcher)
	if !ok {
		return nil, fmt.Errorf("%w: backend cannot qualify search completeness for complete pull", domain.ErrCheckFailed)
	}
	first, err := collectCompletePullIDs(ctx, searcher, query, o.MaxPages)
	if err != nil {
		return nil, err
	}
	second, err := collectCompletePullIDs(ctx, searcher, query, o.MaxPages)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(first, second) {
		return nil, fmt.Errorf("%w: complete-pull selection changed during pagination; retry after the backend settles (no checkpoint was replaced)", domain.ErrCheckFailed)
	}
	migrations, err := preflightConfluenceOverwrite(m, second)
	if err != nil {
		return nil, err
	}
	selectionSHA256, err := confluenceCompleteHashJSON(second)
	if err != nil {
		return nil, err
	}
	checkpoint = mirror.CompletePullCheckpoint{
		Service: confluenceCompletePullService, SelectorSHA256: selectorSHA256,
		OptionsSHA256: optionsSHA256, SelectionSHA256: selectionSHA256, IDs: second,
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	source := "new"
	if found {
		source = "restarted"
	}
	return newCompleteSelection(checkpoint, source, migrations), nil
}

func newCompleteSelection(checkpoint mirror.CompletePullCheckpoint, source string, migrations int) *confluenceCompleteSelection {
	total := len(checkpoint.IDs)
	result := &CompletePullResult{
		SelectorSHA256: checkpoint.SelectorSHA256, SelectionSHA256: checkpoint.SelectionSHA256,
		Source: source, Total: total, Completed: checkpoint.NextIndex,
		Remaining: total - checkpoint.NextIndex, CheckpointActive: true, ViewMigrations: migrations,
	}
	return &confluenceCompleteSelection{checkpoint: checkpoint, nextIndex: checkpoint.NextIndex, savedIndex: checkpoint.NextIndex, result: result}
}

func collectCompletePullIDs(ctx context.Context, searcher domain.CompletePageSearcher, query string, maxPages int) ([]string, error) {
	cursor := ""
	seenCursors := map[string]bool{}
	seenIDs := map[string]bool{}
	for {
		if seenCursors[cursor] {
			return nil, fmt.Errorf("%w: complete-pull search repeated cursor %q", domain.ErrCheckFailed, cursor)
		}
		seenCursors[cursor] = true
		page, err := searcher.SearchComplete(ctx, query, 100, cursor)
		if err != nil {
			return nil, err
		}
		for _, hit := range page.Results {
			if strings.TrimSpace(hit.ID) == "" {
				return nil, fmt.Errorf("%w: complete-pull search result omitted page id", domain.ErrCheckFailed)
			}
			if len(hit.ID) > confluenceCompletePullMaxIDBytes {
				return nil, fmt.Errorf("%w: complete-pull page id exceeds %d bytes", domain.ErrCheckFailed, confluenceCompletePullMaxIDBytes)
			}
			if seenIDs[hit.ID] {
				return nil, fmt.Errorf("%w: complete-pull search repeated page id %q", domain.ErrCheckFailed, hit.ID)
			}
			seenIDs[hit.ID] = true
			if maxPages > 0 && len(seenIDs) > maxPages {
				return nil, fmt.Errorf("%w: complete-pull selection exceeded --max-pages=%d; raise the explicit cap and retry (no checkpoint was written)", domain.ErrCheckFailed, maxPages)
			}
			if len(seenIDs) > confluenceCompletePullMaxIDs {
				return nil, fmt.Errorf("%w: complete-pull selection exceeds the %d-identity local safety limit; narrow the selector", domain.ErrCheckFailed, confluenceCompletePullMaxIDs)
			}
		}
		if page.Next == "" {
			if !page.Complete {
				reason := page.PartialReason
				if reason == "" {
					reason = "backend did not prove terminal search completeness"
				}
				return nil, fmt.Errorf("%w: incomplete complete-pull selection: %s (no checkpoint was written)", domain.ErrCheckFailed, reason)
			}
			break
		}
		cursor = page.Next
	}
	ids := make([]string, 0, len(seenIDs))
	for id := range seenIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (selection *confluenceCompleteSelection) advance() {
	selection.nextIndex++
	selection.result.Completed = selection.nextIndex
	selection.result.Remaining = selection.result.Total - selection.nextIndex
}

func (selection *confluenceCompleteSelection) shouldCheckpoint() bool {
	return selection.nextIndex-selection.savedIndex >= confluenceCompletePullBatch
}

func (selection *confluenceCompleteSelection) save(m *mirror.Mirror) error {
	selection.checkpoint.NextIndex = selection.nextIndex
	if err := m.SaveCompletePullCheckpoint(selection.checkpoint); err != nil {
		return err
	}
	selection.savedIndex = selection.nextIndex
	return nil
}
