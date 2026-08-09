package confluence

import (
	"context"
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
		Size       int  `json:"size"`
		TotalCount *int `json:"totalCount"`
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
	next := ""
	if resp.Links.Next != "" && len(resp.Results) > 0 {
		// Advance by the number of results actually returned, not the requested
		// limit, so a short page (server returns < limit but still signals more)
		// can't skip or repeat the next offset. An empty page is treated as
		// exhausted even if the server still sets _links.next, so the cursor
		// never stalls at the same offset.
		next = strconv.Itoa(start + len(resp.Results))
	}
	page := domain.PageSearchPage{Results: out, Next: next, Complete: next == ""}
	if resp.Links.Next != "" && len(resp.Results) == 0 {
		page.Complete = false
		page.PartialReason = "backend returned an empty page with a next link"
	} else if next == "" && resp.TotalCount != nil && start+len(resp.Results) < *resp.TotalCount {
		page.Complete = false
		page.PartialReason = fmt.Sprintf("backend reported %d total matches but only %d were reachable", *resp.TotalCount, start+len(resp.Results))
	}
	return page, nil
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
	cursor := confluencePageCursor{}
	scanned := 0
	var out []domain.PageRef
	for {
		q := url.Values{}
		q.Set("cql", "space="+cqlQuote(space)+" and type=page")
		q.Set("expand", "ancestors,version,space")
		q.Set("limit", "200")
		q.Set("start", strconv.Itoa(cursor.startAt()))
		var resp struct {
			Results []content `json:"results"`
			Size    int       `json:"size"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := cf.c.GetJSON(ctx, "/rest/api/content/search?"+q.Encode(), &resp); err != nil {
			return nil, false, err
		}
		remaining := treeScanCap - scanned
		resultCount := len(resp.Results)
		if resultCount > remaining {
			resultCount = remaining
		}
		scanned += resultCount
		outputOverflow := false
		for _, ct := range resp.Results[:resultCount] {
			d := 0
			if ct.Ancestors != nil {
				d = len(*ct.Ancestors)
			}
			if depth > 0 && d >= depth {
				continue
			}
			pr := domain.PageRef{ID: ct.ID, Title: ct.Title, Space: ct.Space.Key, Version: ct.Version.Number}
			if ct.Ancestors != nil {
				if n := len(*ct.Ancestors); n > 0 {
					pr.Parent = (*ct.Ancestors)[n-1].ID
				}
			}
			if len(out) < treePageCap {
				out = append(out, pr)
			} else {
				outputOverflow = true
			}
		}
		if len(resp.Results) > remaining {
			return out, true, nil // the response itself exceeded the scan cap
		}
		if outputOverflow {
			return out, true, nil
		}
		switch cursor.advance(len(resp.Results), resp.Links.Next) {
		case confluencePageExhausted:
			return out, false, nil
		case confluencePageStalled:
			return out, true, nil
		}
		if scanned >= treeScanCap {
			return out, true, nil // cap hit with more pages remaining
		}
	}
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
