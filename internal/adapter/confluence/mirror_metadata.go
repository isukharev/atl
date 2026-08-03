package confluence

import (
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const (
	confluencePageMetadataBatchMaxIDs      = 100
	confluencePageMetadataSelectorMaxBytes = 16 << 10
)

// PlanPageMetadataBatches partitions exact page identities without performing
// I/O. The encoded selector byte cap is measured after CQL string escaping.
func (cf *Confluence) PlanPageMetadataBatches(ids []string) ([][]string, error) {
	var batches [][]string
	for start := 0; start < len(ids); {
		end := start
		selectorBytes := 0
		for end < len(ids) && end-start < confluencePageMetadataBatchMaxIDs {
			encoded := len(cqlQuote(ids[end]))
			if end > start {
				encoded++ // comma between literals
			}
			if selectorBytes+encoded > confluencePageMetadataSelectorMaxBytes {
				break
			}
			selectorBytes += encoded
			end++
		}
		if end == start {
			return nil, fmt.Errorf("%w: Confluence page metadata selector exceeds the bounded batch size", domain.ErrUsage)
		}
		batches = append(batches, append([]string(nil), ids[start:end]...))
		start = end
	}
	return batches, nil
}

// ReadPageMetadataBatch performs exactly one qualified search request for a
// batch already produced by PlanPageMetadataBatches.
func (cf *Confluence) ReadPageMetadataBatch(ctx context.Context, ids []string) (domain.ConfluencePageMetadataBatch, error) {
	if len(ids) == 0 || len(ids) > confluencePageMetadataBatchMaxIDs {
		return domain.ConfluencePageMetadataBatch{}, fmt.Errorf("%w: Confluence page metadata batch must contain 1-%d identities", domain.ErrUsage, confluencePageMetadataBatchMaxIDs)
	}
	parts := make([]string, len(ids))
	selectorBytes := 0
	for i, id := range ids {
		parts[i] = cqlQuote(id)
		selectorBytes += len(parts[i])
		if i > 0 {
			selectorBytes++
		}
	}
	if selectorBytes > confluencePageMetadataSelectorMaxBytes {
		return domain.ConfluencePageMetadataBatch{}, fmt.Errorf("%w: Confluence page metadata selector exceeds the bounded batch size", domain.ErrUsage)
	}
	page, err := cf.SearchComplete(ctx, "type=page and id in ("+strings.Join(parts, ",")+")", len(ids), "")
	if err != nil {
		return domain.ConfluencePageMetadataBatch{}, err
	}
	result := domain.ConfluencePageMetadataBatch{Results: page.Results, Complete: page.Complete && page.Next == ""}
	if !result.Complete {
		result.PartialReason = domain.ConfluencePageMetadataPartialPaginationUnqualified
	}
	return result, nil
}
