package confluence

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// Search runs a CQL query via /rest/api/search (which carries excerpts). cursor
// is the start offset; the returned cursor is the next start, or "" when
// exhausted.
func (cf *Confluence) Search(ctx context.Context, query string, limit int, cursor string) ([]domain.PageRef, string, error) {
	start, _ := strconv.Atoi(cursor)
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
		Size  int `json:"size"`
		Links struct {
			Next string `json:"next"`
			Base string `json:"base"`
		} `json:"_links"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/search?"+q.Encode(), &resp); err != nil {
		return nil, "", err
	}
	out := make([]domain.PageRef, 0, len(resp.Results))
	for _, r := range resp.Results {
		pr := domain.PageRef{
			ID: r.Content.ID, Title: firstNonEmpty(r.Content.Title, stripHTML(r.Title)),
			Space: r.Content.Space.Key, Version: r.Content.Version.Number,
			Excerpt: stripHTML(r.Excerpt),
		}
		if r.URL != "" {
			pr.URL = resp.Links.Base + r.URL
		}
		out = append(out, pr)
	}
	next := ""
	if resp.Links.Next != "" {
		// Advance by the number of results actually returned, not the requested
		// limit, so a short page (server returns < limit but still signals more)
		// can't skip or repeat the next offset.
		next = strconv.Itoa(start + len(resp.Results))
	}
	return out, next, nil
}

// Tree returns the page hierarchy of a space (Parent set from ancestors). depth
// <= 0 means unlimited. It pages internally up to a safety cap.
func (cf *Confluence) Tree(ctx context.Context, space string, depth int) ([]domain.PageRef, error) {
	const cap = 2000
	start := 0
	var out []domain.PageRef
	for len(out) < cap {
		q := url.Values{}
		q.Set("cql", "space="+cqlQuote(space)+" and type=page")
		q.Set("expand", "ancestors,version,space")
		q.Set("limit", "200")
		q.Set("start", strconv.Itoa(start))
		var resp struct {
			Results []content `json:"results"`
			Size    int       `json:"size"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := cf.c.GetJSON(ctx, "/rest/api/content/search?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, ct := range resp.Results {
			d := len(ct.Ancestors)
			if depth > 0 && d >= depth {
				continue
			}
			pr := domain.PageRef{ID: ct.ID, Title: ct.Title, Space: ct.Space.Key, Version: ct.Version.Number}
			if n := len(ct.Ancestors); n > 0 {
				pr.Parent = ct.Ancestors[n-1].ID
			}
			out = append(out, pr)
		}
		if resp.Links.Next == "" || len(resp.Results) == 0 {
			break
		}
		start += len(resp.Results)
	}
	return out, nil
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
