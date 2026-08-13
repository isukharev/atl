package confluence

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	confluenceCorpusMetadataPageSize = 100
	confluenceCorpusMetadataMaxPages = 100_000
)

const confluenceCorpusMetadataExpand = "version,space,ancestors,metadata.labels,restrictions.read.restrictions.user,restrictions.read.restrictions.group"

type qualifiedEmbeddedPage[T any] struct {
	Results    *[]T `json:"results"`
	Start      *int `json:"start"`
	Limit      *int `json:"limit"`
	Size       *int `json:"size"`
	TotalCount *int `json:"totalCount"`
	TotalSize  *int `json:"totalSize"`
	Links      *struct {
		Next string `json:"next"`
	} `json:"_links"`
}

type confluenceCorpusMetadataPage struct {
	Results    *[]content `json:"results"`
	Start      *int       `json:"start"`
	Limit      *int       `json:"limit"`
	Size       *int       `json:"size"`
	TotalCount *int       `json:"totalCount"`
	TotalSize  *int       `json:"totalSize"`
	Links      *struct {
		Next string `json:"next"`
	} `json:"_links"`
}

// ReadConfluenceCorpusMetadata performs one bounded whole-space metadata pass.
// It never expands or downloads a page body. Continuation URLs are evidence
// only; every request remains relative to the configured backend origin.
func (cf *Confluence) ReadConfluenceCorpusMetadata(ctx context.Context, space string, maxPages int) (domain.ConfluenceCorpusMetadataInventory, error) {
	if !safeConfluenceCorpusValue(space) || maxPages <= 0 || maxPages > confluenceCorpusMetadataMaxPages {
		return domain.ConfluenceCorpusMetadataInventory{}, fmt.Errorf("%w: invalid Confluence corpus metadata selection", domain.ErrUsage)
	}
	inventory := domain.ConfluenceCorpusMetadataInventory{Rows: []domain.ConfluenceCorpusMetadata{}}
	seen := make(map[string]struct{}, maxPages)
	cursor := confluencePageCursor{}
	terminalProbe := false
	totalObserved, totalPresent, observedTotal := false, false, 0
	for {
		remaining := maxPages - len(inventory.Rows)
		if remaining <= 0 && !terminalProbe {
			return inventory, nil
		}
		limit := confluenceCorpusMetadataPageSize
		if terminalProbe {
			limit = 1
		} else if remaining < limit {
			limit = remaining
		}
		probingTerminal := terminalProbe
		terminalProbe = false
		query := url.Values{}
		query.Set("cql", "space="+cqlQuote(space)+" and type=page")
		query.Set("expand", confluenceCorpusMetadataExpand)
		query.Set("limit", strconv.Itoa(limit))
		query.Set("start", strconv.Itoa(cursor.startAt()))
		var response confluenceCorpusMetadataPage
		if err := cf.c.GetJSON(ctx, "/rest/api/content/search?"+query.Encode(), &response); err != nil {
			return domain.ConfluenceCorpusMetadataInventory{}, err
		}
		rows, next, needsTerminalProbe, pageTotal, pageTotalPresent, err := qualifiedCorpusMetadataPage(response, cursor.startAt())
		if err != nil {
			return domain.ConfluenceCorpusMetadataInventory{}, err
		}
		if totalObserved && (totalPresent != pageTotalPresent || totalPresent && observedTotal != pageTotal) {
			return domain.ConfluenceCorpusMetadataInventory{}, unqualifiedConfluenceCorpusMetadata()
		}
		if !totalObserved {
			totalObserved, totalPresent, observedTotal = true, pageTotalPresent, pageTotal
		}
		if probingTerminal && len(rows) > 0 {
			return inventory, nil
		}
		if len(rows) > remaining {
			return inventory, nil
		}
		for index := range rows {
			row, mapErr := cf.qualifiedCorpusMetadataRow(space, &rows[index])
			if mapErr != nil {
				return domain.ConfluenceCorpusMetadataInventory{}, mapErr
			}
			if _, duplicate := seen[row.ID]; duplicate {
				return domain.ConfluenceCorpusMetadataInventory{}, unqualifiedConfluenceCorpusMetadata()
			}
			seen[row.ID] = struct{}{}
			inventory.Rows = append(inventory.Rows, row)
		}
		if next == "" {
			if needsTerminalProbe {
				if cursor.advance(len(rows), "terminal-probe") != confluencePageMore {
					return domain.ConfluenceCorpusMetadataInventory{}, unqualifiedConfluenceCorpusMetadata()
				}
				terminalProbe = true
				continue
			}
			if err := validateConfluenceCorpusHierarchy(inventory.Rows); err != nil {
				return domain.ConfluenceCorpusMetadataInventory{}, err
			}
			inventory.Complete = true
			return inventory, nil
		}
		if len(inventory.Rows) == maxPages {
			return inventory, nil
		}
		if cursor.advance(len(rows), next) != confluencePageMore {
			return domain.ConfluenceCorpusMetadataInventory{}, unqualifiedConfluenceCorpusMetadata()
		}
	}
}

func qualifiedCorpusMetadataPage(response confluenceCorpusMetadataPage, expectedStart int) ([]content, string, bool, int, bool, error) {
	if response.Results == nil || response.Start == nil || *response.Start != expectedStart ||
		response.Limit == nil || *response.Limit <= 0 || *response.Limit > confluenceCorpusMetadataPageSize ||
		response.Size == nil || response.Links == nil {
		return nil, "", false, 0, false, unqualifiedConfluenceCorpusMetadata()
	}
	rows := *response.Results
	if *response.Size != len(rows) || len(rows) > *response.Limit ||
		(response.Links.Next != "" && (len(rows) == 0 || !safePaginationSignal(response.Links.Next))) {
		return nil, "", false, 0, false, unqualifiedConfluenceCorpusMetadata()
	}
	total, hasTotal, reason := qualifiedSearchTotal(response.TotalCount, response.TotalSize)
	pageCursor := confluencePageCursor{start: expectedStart}
	end, bounded := pageCursor.checkedEnd(len(rows))
	if reason != "" || !bounded || hasTotal && (end > total || response.Links.Next != "" && end >= total || response.Links.Next == "" && end != total) {
		return nil, "", false, 0, false, unqualifiedConfluenceCorpusMetadata()
	}
	needsTerminalProbe := !hasTotal && response.Links.Next == "" && len(rows) == *response.Limit
	return rows, response.Links.Next, needsTerminalProbe, total, hasTotal, nil
}

func (cf *Confluence) qualifiedCorpusMetadataRow(space string, value *content) (domain.ConfluenceCorpusMetadata, error) {
	if value == nil || !canonicalConfluenceGraphPageID(value.ID) || value.Type != "page" ||
		value.Space.Key != space || value.Version.Number <= 0 ||
		!safeConfluenceCorpusValue(value.Title) || !qualifiedConfluenceUpdated(value.Version.When) ||
		value.Ancestors == nil || value.Body.Storage != nil || value.Body.View != nil {
		return domain.ConfluenceCorpusMetadata{}, unqualifiedConfluenceCorpusMetadata()
	}
	row := domain.ConfluenceCorpusMetadata{
		ID: value.ID, Type: value.Type, Title: value.Title, Space: value.Space.Key,
		Version: value.Version.Number, Updated: value.Version.When,
		Ancestors: []string{}, AncestorIDs: []string{}, Labels: []string{},
	}
	ancestorSeen := make(map[string]struct{}, len(*value.Ancestors))
	for _, ancestor := range *value.Ancestors {
		if !canonicalConfluenceGraphPageID(ancestor.ID) || ancestor.ID == value.ID ||
			ancestor.Type != "page" || !safeConfluenceCorpusValue(ancestor.Title) {
			return domain.ConfluenceCorpusMetadata{}, unqualifiedConfluenceCorpusMetadata()
		}
		if _, duplicate := ancestorSeen[ancestor.ID]; duplicate {
			return domain.ConfluenceCorpusMetadata{}, unqualifiedConfluenceCorpusMetadata()
		}
		ancestorSeen[ancestor.ID] = struct{}{}
		row.Ancestors = append(row.Ancestors, ancestor.Title)
		row.AncestorIDs = append(row.AncestorIDs, ancestor.ID)
	}
	if len(row.AncestorIDs) > 0 {
		row.Parent = row.AncestorIDs[len(row.AncestorIDs)-1]
	}
	labels, complete, err := value.qualifiedLabelValues()
	if err != nil || !complete {
		return domain.ConfluenceCorpusMetadata{}, unqualifiedConfluenceCorpusMetadata()
	}
	row.Labels = labels
	restricted, complete, err := value.qualifiedRestrictionState()
	if err != nil || !complete {
		return domain.ConfluenceCorpusMetadata{}, unqualifiedConfluenceCorpusMetadata()
	}
	row.Restricted = restricted
	if value.Links == nil {
		return domain.ConfluenceCorpusMetadata{}, unqualifiedConfluenceCorpusMetadata()
	}
	row.URL, complete = qualifiedConfluenceWebURL(cf.base, value.Links.WebUI)
	if !complete {
		return domain.ConfluenceCorpusMetadata{}, unqualifiedConfluenceCorpusMetadata()
	}
	return row, nil
}

func (ct *content) labelValues() []string {
	if ct == nil || ct.Metadata == nil || ct.Metadata.Labels == nil || ct.Metadata.Labels.Results == nil {
		return nil
	}
	labels := make([]string, 0, len(*ct.Metadata.Labels.Results))
	for _, label := range *ct.Metadata.Labels.Results {
		labels = append(labels, label.Name)
	}
	return labels
}

func (ct *content) qualifiedLabelValues() ([]string, bool, error) {
	if ct == nil || ct.Metadata == nil || ct.Metadata.Labels == nil {
		return nil, false, nil
	}
	labelsPage := ct.Metadata.Labels
	terminal, err := validateQualifiedEmbeddedPage(labelsPage)
	if err != nil {
		return nil, false, err
	}
	if !terminal {
		return nil, false, nil
	}
	labels := make([]string, 0, len(*labelsPage.Results))
	seen := make(map[string]struct{}, len(*labelsPage.Results))
	for _, label := range *labelsPage.Results {
		if !safeConfluenceCorpusValue(label.Name) {
			return nil, false, unqualifiedConfluenceCorpusMetadata()
		}
		if _, duplicate := seen[label.Name]; duplicate {
			return nil, false, unqualifiedConfluenceCorpusMetadata()
		}
		seen[label.Name] = struct{}{}
		labels = append(labels, label.Name)
	}
	sort.Strings(labels)
	return labels, true, nil
}

func (ct *content) qualifiedRestrictionState() (bool, bool, error) {
	if ct == nil || ct.Restrictions == nil || ct.Restrictions.Read == nil || ct.Restrictions.Read.Restrictions == nil {
		return false, false, nil
	}
	restrictions := ct.Restrictions.Read.Restrictions
	userTerminal, groupTerminal := false, false
	if restrictions.User != nil {
		var err error
		userTerminal, err = validateQualifiedEmbeddedPage(restrictions.User)
		if err != nil {
			return false, false, err
		}
	}
	if restrictions.Group != nil {
		var err error
		groupTerminal, err = validateQualifiedEmbeddedPage(restrictions.Group)
		if err != nil {
			return false, false, err
		}
	}
	userRestricted := restrictions.User != nil && len(*restrictions.User.Results) > 0
	groupRestricted := restrictions.Group != nil && len(*restrictions.Group.Results) > 0
	if userRestricted || groupRestricted {
		return true, true, nil
	}
	if restrictions.User == nil || restrictions.Group == nil {
		return false, false, nil
	}
	return false, userTerminal && groupTerminal, nil
}

func validateQualifiedEmbeddedPage[T any](page *qualifiedEmbeddedPage[T]) (bool, error) {
	if page == nil || page.Results == nil || page.Start == nil || *page.Start != 0 ||
		page.Limit == nil || *page.Limit <= 0 || page.Size == nil || page.Links == nil {
		return false, unqualifiedConfluenceCorpusMetadata()
	}
	results := *page.Results
	if *page.Size != len(results) || len(results) > *page.Limit ||
		(page.Links.Next != "" && (len(results) == 0 || !safePaginationSignal(page.Links.Next))) {
		return false, unqualifiedConfluenceCorpusMetadata()
	}
	total, hasTotal, reason := qualifiedSearchTotal(page.TotalCount, page.TotalSize)
	if reason != "" || hasTotal && (len(results) > total || page.Links.Next != "" && len(results) >= total || page.Links.Next == "" && len(results) != total) {
		return false, unqualifiedConfluenceCorpusMetadata()
	}
	if !hasTotal && page.Links.Next == "" && len(results) == *page.Limit {
		return false, nil
	}
	return page.Links.Next == "", nil
}

func qualifiedConfluenceWebURL(base, webUI string) (string, bool) {
	if webUI == "" || strings.TrimSpace(webUI) != webUI || !safePaginationSignal(webUI) {
		return "", false
	}
	value := confluenceWebURL(base, webUI)
	return value, value != ""
}

func safePaginationSignal(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func safeConfluenceCorpusValue(value string) bool {
	return value != "" && len(value) <= 4096 && safePaginationSignal(value)
}

func qualifiedConfluenceUpdated(value string) bool {
	if !safeConfluenceCorpusValue(value) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validateConfluenceCorpusHierarchy(rows []domain.ConfluenceCorpusMetadata) error {
	byID := make(map[string]domain.ConfluenceCorpusMetadata, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	for _, row := range rows {
		for index, ancestorID := range row.AncestorIDs {
			ancestor, present := byID[ancestorID]
			if !present || ancestor.Title != row.Ancestors[index] || len(ancestor.AncestorIDs) != index {
				return unqualifiedConfluenceCorpusMetadata()
			}
			for ancestorIndex := 0; ancestorIndex < index; ancestorIndex++ {
				if ancestor.AncestorIDs[ancestorIndex] != row.AncestorIDs[ancestorIndex] {
					return unqualifiedConfluenceCorpusMetadata()
				}
			}
		}
	}
	return nil
}

func unqualifiedConfluenceCorpusMetadata() error {
	return fmt.Errorf("%w: Confluence corpus metadata is not qualified", domain.ErrCheckFailed)
}

var _ domain.QualifiedConfluenceCorpusMetadataReader = (*Confluence)(nil)
