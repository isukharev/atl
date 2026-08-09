package confluence

// confluencePageCursor owns the checked offset arithmetic shared by endpoints
// that consume Confluence _links.next pagination. The link is deliberately only
// a continuation signal: server-provided URLs are never followed or resolved.
type confluencePageCursor struct {
	start int
}

type confluencePageAdvance uint8

const (
	confluencePageMore confluencePageAdvance = iota
	confluencePageExhausted
	confluencePageStalled
)

func (c *confluencePageCursor) startAt() int { return c.start }

func (c *confluencePageCursor) checkedEnd(rows int) (int, bool) {
	if rows < 0 || rows > int(^uint(0)>>1)-c.start {
		return c.start, false
	}
	return c.start + rows, true
}

func (c *confluencePageCursor) advance(rows int, nextLink string) confluencePageAdvance {
	if nextLink == "" {
		return confluencePageExhausted
	}
	next, ok := c.checkedEnd(rows)
	if !ok || rows == 0 {
		return confluencePageStalled
	}
	c.start = next
	return confluencePageMore
}
