package confluence

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const confluenceAttachmentDownloadMatchLimit = 2
const confluenceAttachmentDownloadMaxFilenameBytes = 255

type confluenceAttachmentDownloadMetadata struct {
	ID      *string `json:"id"`
	Title   *string `json:"title"`
	Type    *string `json:"type"`
	Version *struct {
		Number *int `json:"number"`
	} `json:"version"`
	Container *struct {
		ID   *string `json:"id"`
		Type *string `json:"type"`
	} `json:"container"`
	Extensions *struct {
		FileSize *int64 `json:"fileSize"`
	} `json:"extensions"`
}

// RevalidateAttachmentDownload resolves one exact filename inside a known
// content container, then verifies a historical attachment version when one
// was requested. The response is metadata only. The eventual binary route is
// still page+filename+version and therefore remains deliberately not ID-bound.
func (cf *Confluence) RevalidateAttachmentDownload(ctx context.Context, pageID, filename string, requestedVersion int) (domain.ConfluenceAttachmentDownloadEvidence, error) {
	if domain.ReadBudgetFromContext(ctx) == nil || !domain.SingleAttempt(ctx) {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: attachment download revalidation requires a bounded single-attempt context", domain.ErrCheckFailed)
	}
	if !domain.ValidConfluenceReadID(pageID) || strings.TrimSpace(filename) == "" || !utf8.ValidString(filename) ||
		len(filename) > confluenceAttachmentDownloadMaxFilenameBytes || requestedVersion < 0 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: attachment download selector is invalid", domain.ErrUsage)
	}
	query := url.Values{}
	query.Set("expand", "container,extensions,version")
	query.Set("filename", filename)
	query.Set("limit", strconv.Itoa(confluenceAttachmentDownloadMatchLimit))
	query.Set("start", "0")
	var listing struct {
		Results    *[]confluenceAttachmentDownloadMetadata `json:"results"`
		TotalCount *int                                    `json:"totalCount"`
		Start      *int                                    `json:"start"`
		Limit      *int                                    `json:"limit"`
		Size       *int                                    `json:"size"`
		Links      *struct {
			Next string `json:"next"`
		} `json:"_links"`
	}
	path := "/rest/api/content/" + url.PathEscape(pageID) + "/child/attachment?" + query.Encode()
	if err := cf.c.GetJSON(ctx, path, &listing); err != nil {
		return domain.ConfluenceAttachmentDownloadEvidence{}, err
	}
	// Some compatible endpoints omit totalCount for a terminal child listing.
	// A bounded start=0 page remains sufficient when it is self-consistent,
	// terminal, and has exactly one result below. A present totalCount is still
	// exact evidence and must agree with that terminal page. Do not require the
	// response to echo our requested limit: servers may clamp or normalize it,
	// but it must still bound the returned collection.
	if listing.Results == nil || listing.Start == nil || listing.Limit == nil || listing.Size == nil || listing.Links == nil ||
		*listing.Start != 0 || *listing.Limit <= 0 ||
		*listing.Size != len(*listing.Results) || *listing.Size < 0 || *listing.Size > *listing.Limit ||
		listing.Links.Next != "" ||
		(listing.TotalCount != nil && (*listing.TotalCount < 0 || *listing.TotalCount != *listing.Size)) {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: attachment filename inventory is incomplete or inconsistent", domain.ErrCheckFailed)
	}
	if len(*listing.Results) == 0 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: attachment filename was not found", domain.ErrNotFound)
	}
	if len(*listing.Results) != 1 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: attachment filename is ambiguous", domain.ErrCheckFailed)
	}
	current, ok := qualifiedConfluenceAttachmentDownloadMetadata((*listing.Results)[0], pageID, filename)
	if !ok {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: attachment filename inventory omitted exact identity or version evidence", domain.ErrCheckFailed)
	}
	if requestedVersion == 0 || requestedVersion == current.Version {
		return current, nil
	}
	if requestedVersion > current.Version {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: requested attachment version is newer than the revalidated current version", domain.ErrCheckFailed)
	}

	query = url.Values{}
	query.Set("expand", "container,extensions,version")
	query.Set("status", "historical")
	query.Set("version", strconv.Itoa(requestedVersion))
	var historical confluenceAttachmentDownloadMetadata
	if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(current.AttachmentID)+"?"+query.Encode(), &historical); err != nil {
		return domain.ConfluenceAttachmentDownloadEvidence{}, err
	}
	evidence, ok := qualifiedConfluenceAttachmentDownloadMetadata(historical, pageID, filename)
	if !ok || evidence.AttachmentID != current.AttachmentID || evidence.Version != requestedVersion {
		return domain.ConfluenceAttachmentDownloadEvidence{}, fmt.Errorf("%w: requested attachment version did not match the revalidated selector", domain.ErrCheckFailed)
	}
	return evidence, nil
}

func qualifiedConfluenceAttachmentDownloadMetadata(value confluenceAttachmentDownloadMetadata, pageID, filename string) (domain.ConfluenceAttachmentDownloadEvidence, bool) {
	if value.ID == nil || value.Title == nil || value.Type == nil || value.Version == nil || value.Version.Number == nil ||
		value.Container == nil || value.Container.ID == nil || value.Container.Type == nil ||
		value.Extensions == nil || value.Extensions.FileSize == nil {
		return domain.ConfluenceAttachmentDownloadEvidence{}, false
	}
	if !domain.ValidConfluenceReadID(*value.ID) || *value.Title != filename || *value.Type != "attachment" || *value.Version.Number <= 0 ||
		*value.ID == pageID || *value.Container.ID != pageID || (*value.Container.Type != "page" && *value.Container.Type != "blogpost") || *value.Extensions.FileSize < 0 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, false
	}
	return domain.ConfluenceAttachmentDownloadEvidence{
		AttachmentID: *value.ID, PageID: pageID, Filename: filename, Version: *value.Version.Number, FileSize: *value.Extensions.FileSize,
	}, true
}
