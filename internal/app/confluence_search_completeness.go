package app

import "github.com/isukharev/atl/internal/domain"

// confluenceSearchEvidence reconciles exact-total evidence across a complete
// cursor chain. An exact total may be absent from some pages, but every value
// the backend does provide must agree with the chain and its terminal identity
// count.
type confluenceSearchEvidence struct {
	exactTotal *int
}

func (e *confluenceSearchEvidence) observe(page domain.PageSearchPage, identities int) string {
	if page.ExactTotal != nil {
		total := *page.ExactTotal
		if total < 0 {
			return "backend reported a negative exact search total"
		}
		if e.exactTotal != nil && *e.exactTotal != total {
			return "backend reported contradictory exact search totals across pages"
		}
		if e.exactTotal == nil {
			e.exactTotal = &total
		}
	}
	if e.exactTotal == nil {
		return ""
	}
	if identities > *e.exactTotal {
		return "search pagination produced more distinct page identities than its exact total"
	}
	if page.Next == "" && page.Complete && identities != *e.exactTotal {
		return "terminal search pagination did not match its exact total"
	}
	return ""
}
