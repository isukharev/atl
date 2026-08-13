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

const (
	confluenceAttachmentDiscoveryPageSize = 100
	confluenceAttachmentDiscoveryMaxItems = 10_000
)

// DiscoverAttachmentsQualified searches metadata for attachments across a
// caller-selected live scope. It never follows _links.next or any attachment
// URL: the link is terminality evidence only, and continuation advances by the
// strictly validated number of returned rows.
func (cf *Confluence) DiscoverAttachmentsQualified(ctx context.Context, request domain.ConfluenceAttachmentDiscoveryRequest) (domain.ConfluenceAttachmentDiscoveryPage, error) {
	result := domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{}, Start: request.Start,
		Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
	}
	if request.Start < 0 || request.MaxItems < 1 || request.MaxItems > confluenceAttachmentDiscoveryMaxItems {
		return result, fmt.Errorf("%w: Confluence attachment discovery bounds are invalid", domain.ErrUsage)
	}
	partial := func(reason string, start int) (domain.ConfluenceAttachmentDiscoveryPage, error) {
		result.PartialReason = reason
		result.NextStart = intPointer(start)
		return result, nil
	}
	cursor := confluencePageCursor{start: request.Start}
	qualifiedTotal := -1
	seen := map[string]struct{}{}
	for len(result.Attachments) < request.MaxItems {
		remaining := request.MaxItems - len(result.Attachments)
		pageLimit := confluenceAttachmentDiscoveryPageSize
		if remaining < pageLimit {
			pageLimit = remaining
		}
		query := url.Values{}
		query.Set("cql", confluenceAttachmentDiscoveryCQL(request.Space, request.CQL))
		query.Set("expand", "container.version,extensions,metadata,space,version")
		query.Set("limit", strconv.Itoa(pageLimit))
		query.Set("start", strconv.Itoa(cursor.startAt()))
		var response struct {
			Results *[]struct {
				ID      *string `json:"id"`
				Title   *string `json:"title"`
				Type    *string `json:"type"`
				Version *struct {
					Number *int `json:"number"`
				} `json:"version"`
				Container *struct {
					ID      *string `json:"id"`
					Type    *string `json:"type"`
					Version *struct {
						Number *int `json:"number"`
					} `json:"version"`
				} `json:"container"`
				Space *struct {
					Key *string `json:"key"`
				} `json:"space"`
				Metadata *struct {
					MediaType *string `json:"mediaType"`
				} `json:"metadata"`
				Extensions *struct {
					FileSize *int64 `json:"fileSize"`
				} `json:"extensions"`
			} `json:"results"`
			Start      *int                             `json:"start"`
			Limit      *int                             `json:"limit"`
			Size       *int                             `json:"size"`
			TotalCount confluenceContentSearchWireTotal `json:"totalCount"`
			TotalSize  confluenceContentSearchWireTotal `json:"totalSize"`
			Links      *struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := cf.c.GetJSON(ctx, "/rest/api/content/search?"+query.Encode(), &response); err != nil {
			switch {
			case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
				return partial(domain.ConfluenceAttachmentDiscoveryPartialRequestLimit, cursor.startAt())
			case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
				return partial(domain.ConfluenceAttachmentDiscoveryPartialResponseByteLimit, cursor.startAt())
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				return partial(domain.ConfluenceAttachmentDiscoveryPartialDeadline, cursor.startAt())
			default:
				return result, err
			}
		}
		if response.Results == nil || response.Start == nil || response.Limit == nil || response.Size == nil || response.Links == nil {
			return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified, cursor.startAt())
		}
		rows := *response.Results
		if *response.Start < 0 || *response.Start != cursor.startAt() || *response.Limit <= 0 || *response.Limit > pageLimit ||
			*response.Size < 0 || *response.Size != len(rows) || *response.Size > *response.Limit || len(rows) > pageLimit {
			return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified, cursor.startAt())
		}
		total, totalOK := qualifiedConfluenceContentSearchTotal(response.TotalCount, response.TotalSize)
		if !totalOK {
			return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified, cursor.startAt())
		}
		if qualifiedTotal < 0 {
			qualifiedTotal = total
			result.TotalSize = intPointer(qualifiedTotal)
		} else if total != qualifiedTotal {
			result.TotalSize = nil
			return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified, cursor.startAt())
		}
		end, bounded := cursor.checkedEnd(len(rows))
		if !bounded || end > qualifiedTotal ||
			(response.Links.Next == "" && end != qualifiedTotal) ||
			(response.Links.Next != "" && end >= qualifiedTotal) {
			return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified, cursor.startAt())
		}
		pageItems := make([]domain.ConfluenceAttachmentMetadata, 0, len(rows))
		for _, row := range rows {
			item, ok := qualifiedConfluenceAttachmentMetadata(row.ID, row.Title, row.Type, row.Version, row.Container, row.Space, row.Metadata, row.Extensions, request.Space)
			if !ok {
				return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified, cursor.startAt())
			}
			if _, duplicate := seen[item.ID]; duplicate {
				return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified, cursor.startAt())
			}
			seen[item.ID] = struct{}{}
			pageItems = append(pageItems, item)
		}
		result.Attachments = append(result.Attachments, pageItems...)
		switch cursor.advance(len(rows), response.Links.Next) {
		case confluencePageExhausted:
			result.Complete = true
			return result, nil
		case confluencePageStalled:
			return partial(domain.ConfluenceAttachmentDiscoveryPartialPaginationStalled, cursor.startAt())
		}
	}
	return partial(domain.ConfluenceAttachmentDiscoveryPartialItemLimit, cursor.startAt())
}

func confluenceAttachmentDiscoveryCQL(space, additional string) string {
	parts := []string{"type = attachment"}
	if strings.TrimSpace(space) != "" {
		parts = append(parts, "space = "+cqlQuote(strings.TrimSpace(space)))
	}
	if strings.TrimSpace(additional) != "" {
		parts = append(parts, "("+strings.TrimSpace(additional)+")")
	}
	return strings.Join(parts, " and ")
}

func intPointer(value int) *int { return &value }

func qualifiedConfluenceAttachmentMetadata(
	id, title, contentType *string,
	version *struct {
		Number *int `json:"number"`
	},
	container *struct {
		ID      *string `json:"id"`
		Type    *string `json:"type"`
		Version *struct {
			Number *int `json:"number"`
		} `json:"version"`
	},
	space *struct {
		Key *string `json:"key"`
	},
	metadata *struct {
		MediaType *string `json:"mediaType"`
	},
	extensions *struct {
		FileSize *int64 `json:"fileSize"`
	},
	expectedSpace string,
) (domain.ConfluenceAttachmentMetadata, bool) {
	if id == nil || title == nil || contentType == nil || version == nil || version.Number == nil ||
		container == nil || container.ID == nil || container.Type == nil || container.Version == nil || container.Version.Number == nil ||
		space == nil || space.Key == nil || metadata == nil || metadata.MediaType == nil || extensions == nil || extensions.FileSize == nil {
		return domain.ConfluenceAttachmentMetadata{}, false
	}
	if !domain.ValidConfluenceReadID(*id) || strings.TrimSpace(*title) == "" || *contentType != "attachment" || *version.Number <= 0 ||
		!domain.ValidConfluenceReadID(*container.ID) || (*container.Type != "page" && *container.Type != "blogpost") || *container.Version.Number <= 0 ||
		*id == *container.ID ||
		strings.TrimSpace(*space.Key) == "" || strings.TrimSpace(*metadata.MediaType) == "" || *extensions.FileSize < 0 ||
		(strings.TrimSpace(expectedSpace) != "" && *space.Key != strings.TrimSpace(expectedSpace)) {
		return domain.ConfluenceAttachmentMetadata{}, false
	}
	return domain.ConfluenceAttachmentMetadata{
		ID: *id, Title: *title, Type: *contentType, Version: *version.Number,
		ContainerID: *container.ID, ContainerType: *container.Type, ContainerVersion: *container.Version.Number,
		Space: *space.Key, MediaType: *metadata.MediaType, FileSize: *extensions.FileSize,
	}, true
}
