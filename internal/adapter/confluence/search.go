package confluence

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// parseCursor parses a pagination cursor (a start offset). Empty means the
// first page; a non-numeric or negative value is a usage error rather than a
// silent restart from offset 0.
func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid cursor %q (expected a non-negative offset)", domain.ErrUsage, cursor)
	}
	return n, nil
}

// Search runs a CQL query via /rest/api/search (which carries excerpts). cursor
// is the start offset; the returned cursor is the next start, or "" when
// exhausted.
func (cf *Confluence) Search(ctx context.Context, query string, limit int, cursor string) ([]domain.PageRef, string, error) {
	page, err := cf.SearchComplete(ctx, query, limit, cursor)
	return page.Results, page.Next, err
}

// SearchComplete preserves the backend's total/next evidence so completeness-
// sensitive callers can fail closed instead of treating a malformed terminal
// page as exhaustion. Search keeps its historical lightweight contract above.
func (cf *Confluence) SearchComplete(ctx context.Context, query string, limit int, cursor string) (domain.PageSearchPage, error) {
	start, err := parseCursor(cursor)
	if err != nil {
		return domain.PageSearchPage{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	q := url.Values{}
	q.Set("cql", query)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("start", strconv.Itoa(start))
	q.Set("expand", "content.version,content.space")
	var resp struct {
		Results []struct {
			Content content `json:"content"`
			Title   string  `json:"title"`
			Excerpt string  `json:"excerpt"`
			URL     string  `json:"url"`
		} `json:"results"`
		Start      *int `json:"start"`
		Size       int  `json:"size"`
		TotalCount *int `json:"totalCount"`
		TotalSize  *int `json:"totalSize"`
		Links      struct {
			Next string `json:"next"`
			Base string `json:"base"`
		} `json:"_links"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/search?"+q.Encode(), &resp); err != nil {
		return domain.PageSearchPage{}, err
	}
	out := make([]domain.PageRef, 0, len(resp.Results))
	for _, r := range resp.Results {
		pr := domain.PageRef{
			ID: r.Content.ID, Title: firstNonEmpty(r.Content.Title, stripHTML(r.Title)),
			Space: r.Content.Space.Key, Version: r.Content.Version.Number, Updated: r.Content.Version.When,
			Excerpt: stripHTML(r.Excerpt),
		}
		if r.URL != "" {
			pr.URL = resp.Links.Base + r.URL
		}
		out = append(out, pr)
	}
	pageCursor := confluencePageCursor{start: start}
	end, bounded := pageCursor.checkedEnd(len(resp.Results))
	page := domain.PageSearchPage{Results: out}
	if resp.Start != nil && *resp.Start != start {
		page.PartialReason = "backend returned a non-contiguous search page"
		return page, nil
	}
	if !bounded {
		page.PartialReason = "backend search pagination offset overflowed"
		return page, nil
	}
	total, hasTotal, totalReason := qualifiedSearchTotal(resp.TotalCount, resp.TotalSize)
	if totalReason != "" {
		page.PartialReason = totalReason
		return page, nil
	}
	if hasTotal {
		page.ExactTotal = &total
	}
	if hasTotal && end > total {
		page.PartialReason = fmt.Sprintf("backend returned %d reachable matches beyond its reported total of %d", end, total)
		return page, nil
	}
	if resp.Links.Next != "" && hasTotal && end >= total {
		page.PartialReason = fmt.Sprintf("backend advertised another page after reaching its reported total of %d matches", total)
		return page, nil
	}
	advance := pageCursor.advance(len(resp.Results), resp.Links.Next)
	if advance == confluencePageMore {
		// Advance by the number of results actually returned, not the requested
		// limit, so a short page (server returns < limit but still signals more)
		// can't skip or repeat the next offset. An empty page is treated as
		// exhausted even if the server still sets _links.next, so the cursor
		// never stalls at the same offset.
		page.Next = strconv.Itoa(pageCursor.startAt())
	}
	if advance == confluencePageStalled {
		page.PartialReason = "backend returned an empty page with a next link"
		return page, nil
	}
	if page.Next != "" {
		return page, nil
	}
	if hasTotal {
		if end < total {
			page.PartialReason = fmt.Sprintf("backend reported %d total matches but only %d were reachable", total, end)
			return page, nil
		}
		page.Complete = true
		return page, nil
	}
	if len(resp.Results) >= limit {
		page.PartialReason = "backend returned a full search page without terminal pagination evidence"
		return page, nil
	}
	page.Complete = true
	return page, nil
}

// SearchCompleteContent qualifies the Server/Data Center content-search
// endpoint for exhaustive pulls. It is deliberately separate from
// SearchComplete: the global CQL search endpoint is the compatibility path for
// ordinary search and may carry excerpts, while content search exposes the
// offset/total/next evidence required to prove a large space snapshot.
//
// The continuation URL is treated only as a signal. The next offset is derived
// from the validated response size, so an untrusted backend cannot redirect a
// subsequent request to another origin or query.
func (cf *Confluence) SearchCompleteContent(ctx context.Context, query string, limit int, cursor string) (domain.PageSearchPage, error) {
	start, err := parseCursor(cursor)
	if err != nil {
		return domain.PageSearchPage{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	q := url.Values{}
	q.Set("cql", query)
	q.Set("expand", "ancestors,version,space")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("start", strconv.Itoa(start))
	var resp struct {
		Results    *[]content                       `json:"results"`
		Start      *int                             `json:"start"`
		Limit      *int                             `json:"limit"`
		Size       *int                             `json:"size"`
		TotalCount confluenceContentSearchWireTotal `json:"totalCount"`
		TotalSize  confluenceContentSearchWireTotal `json:"totalSize"`
		Links      *struct {
			Next string `json:"next"`
		} `json:"_links"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/content/search?"+q.Encode(), &resp); err != nil {
		return domain.PageSearchPage{}, err
	}
	page := domain.PageSearchPage{Results: []domain.PageRef{}}
	if resp.Results == nil || resp.Start == nil || resp.Limit == nil || resp.Size == nil || resp.Links == nil {
		page.PartialReason = "backend content search omitted qualified pagination evidence"
		return page, nil
	}
	results := *resp.Results
	if *resp.Start < 0 || *resp.Start != start || *resp.Limit <= 0 || *resp.Limit > limit ||
		*resp.Size < 0 || *resp.Size != len(results) || len(results) > *resp.Limit ||
		(resp.Links.Next != "" && (len(results) == 0 || !safePaginationSignal(resp.Links.Next))) {
		page.PartialReason = "backend content search returned unqualified pagination evidence"
		return page, nil
	}
	total, totalOK := qualifiedConfluenceContentSearchTotal(resp.TotalCount, resp.TotalSize)
	if !totalOK {
		page.PartialReason = "backend content search omitted a qualified exact total"
		return page, nil
	}
	pageCursor := confluencePageCursor{start: start}
	end, bounded := pageCursor.checkedEnd(len(results))
	if !bounded || end > total ||
		(resp.Links.Next == "" && end != total) ||
		(resp.Links.Next != "" && end >= total) {
		page.PartialReason = "backend content search returned contradictory pagination totals"
		page.ExactTotal = &total
		return page, nil
	}
	page.ExactTotal = &total
	page.Results = make([]domain.PageRef, 0, len(results))
	for index := range results {
		row := &results[index]
		if !qualifiedConfluenceTreeContent(*row, row.Space.Key) {
			page.PartialReason = "backend content search returned an invalid page identity"
			page.Results = nil
			return page, nil
		}
		ref := domain.PageRef{ID: row.ID, Title: row.Title, Space: row.Space.Key, Version: row.Version.Number, Updated: row.Version.When}
		if row.Ancestors != nil && len(*row.Ancestors) > 0 {
			ref.Parent = (*row.Ancestors)[len(*row.Ancestors)-1].ID
		}
		if row.Links != nil {
			ref.URL = confluenceWebURL(cf.base, row.Links.WebUI)
		}
		page.Results = append(page.Results, ref)
	}
	switch pageCursor.advance(len(results), resp.Links.Next) {
	case confluencePageExhausted:
		page.Complete = true
	case confluencePageStalled:
		page.PartialReason = "backend content search returned an empty page with a next link"
	default:
		page.Next = strconv.Itoa(pageCursor.startAt())
	}
	return page, nil
}

func qualifiedSearchTotal(totalCount, totalSize *int) (int, bool, string) {
	if (totalCount != nil && *totalCount < 0) || (totalSize != nil && *totalSize < 0) {
		return 0, false, "backend reported a negative total match count"
	}
	if totalCount != nil && totalSize != nil && *totalCount != *totalSize {
		return 0, false, "backend reported contradictory total match counts"
	}
	if totalCount != nil {
		return *totalCount, true, ""
	}
	if totalSize != nil {
		return *totalSize, true, ""
	}
	return 0, false, ""
}

// treePageCap bounds the returned hierarchy. treeScanCap separately bounds raw
// backend rows so a depth filter cannot consume the result budget while a huge
// or hostile space still cannot drive unbounded pagination.
const (
	treePageCap = 2000
	treeScanCap = 20_000
)

// Tree returns the page hierarchy of a space (Parent set from ancestors). depth
// <= 0 means unlimited. It returns up to treePageCap matching pages and scans
// up to treeScanCap backend rows; truncated is true when either cap or stalled
// pagination stopped the listing before exhaustion.
func (cf *Confluence) Tree(ctx context.Context, space string, depth int) ([]domain.PageRef, bool, error) {
	page, err := cf.TreeQualified(ctx, domain.ConfluenceTreeRequest{
		Space: space, Depth: depth, MaxItems: treePageCap, MaxScannedItems: treeScanCap,
	})
	if err != nil {
		return nil, false, err
	}
	return page.Pages, !page.Complete, nil
}

// TreeQualified returns one caller-bounded hierarchy prefix. Item and scan
// limits are enforced here; physical request and aggregate response-byte
// limits are enforced by httpx from the ReadBudget carried in ctx. Confluence
// exposes live offset pagination rather than a snapshot token, so Consistency
// never claims a transaction even when pagination is exhausted.
func (cf *Confluence) TreeQualified(ctx context.Context, request domain.ConfluenceTreeRequest) (domain.ConfluenceTreePage, error) {
	result := domain.ConfluenceTreePage{
		Pages: []domain.PageRef{}, Consistency: domain.ConfluenceTreeConsistencyLiveUnproven,
	}
	if request.MaxItems <= 0 || request.MaxItems > treePageCap {
		return result, fmt.Errorf("%w: Confluence tree item limit must be between 1 and %d", domain.ErrUsage, treePageCap)
	}
	if request.MaxScannedItems <= 0 || request.MaxScannedItems > treeScanCap {
		return result, fmt.Errorf("%w: Confluence tree scan limit must be between 1 and %d", domain.ErrUsage, treeScanCap)
	}
	partial := func(reason string) (domain.ConfluenceTreePage, error) {
		result.PartialReason = reason
		return result, nil
	}
	cursor := confluencePageCursor{}
	scanned := 0
	qualifiedTotal := -1
	seen := make(map[string]struct{})
	for {
		remainingScan := request.MaxScannedItems - scanned
		if remainingScan <= 0 {
			return partial(domain.ConfluenceTreePartialScanLimit)
		}
		pageLimit := 200
		if remainingScan < pageLimit {
			pageLimit = remainingScan
		}
		q := url.Values{}
		q.Set("cql", "space="+cqlQuote(request.Space)+" and type=page")
		q.Set("expand", "ancestors,version,space")
		q.Set("limit", strconv.Itoa(pageLimit))
		q.Set("start", strconv.Itoa(cursor.startAt()))
		var resp struct {
			Results    *[]content                       `json:"results"`
			Start      *int                             `json:"start"`
			Limit      *int                             `json:"limit"`
			Size       *int                             `json:"size"`
			TotalCount confluenceContentSearchWireTotal `json:"totalCount"`
			TotalSize  confluenceContentSearchWireTotal `json:"totalSize"`
			Links      *struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := cf.c.GetJSON(ctx, "/rest/api/content/search?"+q.Encode(), &resp); err != nil {
			switch {
			case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
				return partial(domain.ConfluenceTreePartialRequestLimit)
			case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
				return partial(domain.ConfluenceTreePartialResponseByteLimit)
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				return partial(domain.ConfluenceTreePartialDeadline)
			default:
				return result, err
			}
		}
		if resp.Results == nil || resp.Start == nil || resp.Limit == nil || resp.Size == nil || resp.Links == nil {
			return partial(domain.ConfluenceTreePartialPaginationUnqualified)
		}
		results := *resp.Results
		if *resp.Start < 0 || *resp.Start != cursor.startAt() || *resp.Limit <= 0 || *resp.Limit > pageLimit ||
			*resp.Size < 0 || *resp.Size != len(results) || *resp.Size > *resp.Limit ||
			len(results) > pageLimit {
			return partial(domain.ConfluenceTreePartialPaginationUnqualified)
		}
		total, totalOK := qualifiedConfluenceContentSearchTotal(resp.TotalCount, resp.TotalSize)
		if !totalOK {
			return partial(domain.ConfluenceTreePartialPaginationUnqualified)
		}
		if qualifiedTotal < 0 {
			qualifiedTotal = total
		} else if total != qualifiedTotal {
			return partial(domain.ConfluenceTreePartialPaginationUnqualified)
		}
		end, bounded := cursor.checkedEnd(len(results))
		if !bounded || end > qualifiedTotal ||
			(resp.Links.Next == "" && end != qualifiedTotal) ||
			(resp.Links.Next != "" && end >= qualifiedTotal) {
			return partial(domain.ConfluenceTreePartialPaginationUnqualified)
		}
		pageSeen := make(map[string]struct{}, len(results))
		pageRefs := make([]domain.PageRef, 0, len(results))
		outputOverflow := false
		for _, ct := range results {
			if !qualifiedConfluenceTreeContent(ct, request.Space) {
				return partial(domain.ConfluenceTreePartialPaginationUnqualified)
			}
			if _, duplicate := seen[ct.ID]; duplicate {
				return partial(domain.ConfluenceTreePartialPaginationUnqualified)
			}
			if _, duplicate := pageSeen[ct.ID]; duplicate {
				return partial(domain.ConfluenceTreePartialPaginationUnqualified)
			}
			pageSeen[ct.ID] = struct{}{}
			d := len(*ct.Ancestors)
			if request.Depth > 0 && d >= request.Depth {
				continue
			}
			pr := domain.PageRef{ID: ct.ID, Title: ct.Title, Space: ct.Space.Key, Version: ct.Version.Number}
			if n := len(*ct.Ancestors); n > 0 {
				pr.Parent = (*ct.Ancestors)[n-1].ID
			}
			if len(result.Pages)+len(pageRefs) < request.MaxItems {
				pageRefs = append(pageRefs, pr)
			} else {
				outputOverflow = true
			}
		}
		for id := range pageSeen {
			seen[id] = struct{}{}
		}
		scanned += len(results)
		result.Pages = append(result.Pages, pageRefs...)
		result.ScannedItems = scanned
		if outputOverflow {
			return partial(domain.ConfluenceTreePartialItemLimit)
		}
		switch cursor.advance(len(results), resp.Links.Next) {
		case confluencePageExhausted:
			result.Complete = true
			return result, nil
		case confluencePageStalled:
			return partial(domain.ConfluenceTreePartialPaginationStalled)
		}
		if len(result.Pages) >= request.MaxItems {
			return partial(domain.ConfluenceTreePartialItemLimit)
		}
		if scanned >= request.MaxScannedItems {
			return partial(domain.ConfluenceTreePartialScanLimit)
		}
	}
}

func qualifiedConfluenceTreeContent(ct content, expectedSpace string) bool {
	if !domain.ValidConfluenceReadID(ct.ID) || ct.Type != "page" || strings.TrimSpace(ct.Title) == "" ||
		strings.TrimSpace(ct.Space.Key) == "" || ct.Space.Key != expectedSpace || ct.Version.Number <= 0 || ct.Ancestors == nil {
		return false
	}
	ancestors := make(map[string]struct{}, len(*ct.Ancestors))
	for _, ancestor := range *ct.Ancestors {
		if !domain.ValidConfluenceReadID(ancestor.ID) || ancestor.ID == ct.ID {
			return false
		}
		if _, duplicate := ancestors[ancestor.ID]; duplicate {
			return false
		}
		ancestors[ancestor.ID] = struct{}{}
	}
	return true
}

type confluenceContentSearchWireTotal struct {
	present bool
	valid   bool
	value   int64
}

func (total *confluenceContentSearchWireTotal) UnmarshalJSON(data []byte) error {
	total.present = true
	parsed, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil || parsed > uint64(1<<63-1) {
		return nil
	}
	total.valid = true
	total.value = int64(parsed)
	return nil
}

func qualifiedConfluenceContentSearchTotal(totalCount, totalSize confluenceContentSearchWireTotal) (int, bool) {
	if !totalCount.present && !totalSize.present {
		return 0, false
	}
	if (totalCount.present && !totalCount.valid) || (totalSize.present && !totalSize.valid) ||
		(totalCount.present && totalSize.present && totalCount.value != totalSize.value) {
		return 0, false
	}
	value := totalCount.value
	if !totalCount.present {
		value = totalSize.value
	}
	converted := int(value)
	if converted < 0 || int64(converted) != value {
		return 0, false
	}
	return converted, true
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// cqlQuote renders a value as a safe CQL string literal, escaping backslashes
// and quotes so a crafted space key cannot alter the query.
func cqlQuote(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// stripHTML removes the <b>…</b> highlight tags Confluence wraps excerpts in.
func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
