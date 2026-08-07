package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// pagination safety caps shared by the paged list endpoints below: stop after
// maxPages requests or maxItems collected, so a server that keeps signaling
// _links.next can never spin forever.
const (
	maxPages = 100
	maxItems = 100_000
)

type multipartReadCloser struct {
	io.Reader
	source io.Closer
	once   sync.Once
}

func (r *multipartReadCloser) Close() error {
	var err error
	r.once.Do(func() { err = r.source.Close() })
	return err
}

// ListComments returns a page's comments with both a plain-text fallback and
// the native storage body used by readonly Markdown rendering. It
// follows _links.next, paging until the server stops signaling more. truncated
// is true when a safety cap (maxPages/maxItems) clipped or stopped the listing,
// or when the server advertised another page without making progress — the
// mirror must surface that, never bake in a silently-truncated set.
func (cf *Confluence) ListComments(ctx context.Context, id string) ([]domain.Comment, bool, error) {
	start := 0
	var out []domain.Comment
	for page := 0; page < maxPages && len(out) < maxItems; page++ {
		var resp struct {
			Results []struct {
				ID      string `json:"id"`
				History struct {
					CreatedDate string `json:"createdDate"`
					CreatedBy   struct {
						DisplayName string `json:"displayName"`
					} `json:"createdBy"`
				} `json:"history"`
				Body struct {
					Storage struct {
						Value string `json:"value"`
					} `json:"storage"`
				} `json:"body"`
			} `json:"results"`
			Links struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		q := url.Values{}
		q.Set("expand", "body.storage,history")
		q.Set("limit", "100")
		q.Set("start", strconv.Itoa(start))
		path := "/rest/api/content/" + url.PathEscape(id) + "/child/comment?" + q.Encode()
		if err := cf.c.GetJSON(ctx, path, &resp); err != nil {
			return nil, false, err
		}
		remaining := maxItems - len(out)
		resultCount := len(resp.Results)
		if resultCount > remaining {
			resultCount = remaining
		}
		for _, r := range resp.Results[:resultCount] {
			storage := r.Body.Storage.Value
			body := storage
			if root, err := csf.Parse([]byte(body)); err == nil {
				body = csf.TextContent(root)
			}
			out = append(out, domain.Comment{
				ID: r.ID, Author: r.History.CreatedBy.DisplayName,
				Created: r.History.CreatedDate, Body: body, BodyStorage: storage,
			})
		}
		if len(resp.Results) > remaining {
			return out, true, nil // the response itself exceeded the item cap
		}
		if resp.Links.Next == "" {
			return out, false, nil // server exhausted at or under the cap
		}
		if len(resp.Results) == 0 || len(out) >= maxItems {
			return out, true, nil
		}
		start += len(resp.Results)
	}
	// The loop only reaches here by hitting a safety cap; the sole natural exit
	// returns above, so the last page still signaled _links.next — truncated.
	return out, true, nil
}

// AddComment posts a storage-format comment on a page.
func (cf *Confluence) AddComment(ctx context.Context, id string, body []byte) (*domain.Comment, error) {
	writeContext, _, err := cf.authorizeContent(ctx, domain.WriteVerbSet{domain.WriteVerbComment}, "comment", "", id)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"type":      "comment",
		"container": map[string]string{"id": id, "type": "page"},
		"body": map[string]any{
			"storage": map[string]any{"value": string(body), "representation": "storage"},
		},
	}
	var out struct {
		ID      string `json:"id"`
		History *struct {
			CreatedDate string             `json:"createdDate"`
			CreatedBy   *commentPersonJSON `json:"createdBy"`
		} `json:"history"`
		Body *struct {
			Storage *commentBodyJSON `json:"storage"`
		} `json:"body"`
	}
	if err := cf.c.SendJSON(domain.WithWriteClearance(writeContext), "POST", "/rest/api/content", payload, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return nil, fmt.Errorf("%w: created Confluence comment response omitted id", domain.ErrCheckFailed)
	}
	comment := &domain.Comment{ID: out.ID, Body: string(body)}
	if out.History != nil {
		comment.Created = out.History.CreatedDate
		if out.History.CreatedBy != nil {
			comment.Author = out.History.CreatedBy.DisplayName
			actorID := firstNonEmpty(out.History.CreatedBy.UserKey, out.History.CreatedBy.Username)
			if strings.TrimSpace(actorID) != "" {
				comment.AuthorKey = actorID
			}
		}
	}
	if out.Body != nil && out.Body.Storage != nil && out.Body.Storage.Representation != nil && *out.Body.Storage.Representation == "storage" {
		comment.BodyStorage = out.Body.Storage.Value
		comment.Body = comment.BodyStorage
		if root, err := csf.Parse([]byte(comment.BodyStorage)); err == nil {
			comment.Body = csf.TextContent(root)
		}
	}
	return comment, nil
}

// ListAttachments returns a page's attachments. It is the compatibility
// surface: it delegates to ListAttachmentsQualified and intentionally drops the
// qualification, so an inventory cut short by a safety cap is indistinguishable
// from an exhausted one. New callers that must not mistake a capped listing for
// a complete one use ListAttachmentsQualified instead.
func (cf *Confluence) ListAttachments(ctx context.Context, id string) ([]domain.Attachment, error) {
	inventory, err := cf.ListAttachmentsQualified(ctx, id)
	if err != nil {
		return nil, err
	}
	return inventory.Attachments, nil
}

// ListAttachmentsQualified returns a page's attachments with explicit
// completeness. It follows _links.next until the server stops signaling more
// (complete) and otherwise reports the exact limiter that stopped it: the page
// cap, the item cap, or a page that advertised more while making no progress.
// The item cap is enforced per attachment, so the returned slice never exceeds
// it silently.
func (cf *Confluence) ListAttachmentsQualified(ctx context.Context, id string) (domain.AttachmentInventory, error) {
	start := 0
	out := []domain.Attachment{}
	partial := func(reason string) (domain.AttachmentInventory, error) {
		return domain.AttachmentInventory{Attachments: out, PartialReason: reason}, nil
	}
	for page := 0; page < maxPages; page++ {
		// Reaching a later iteration means the previous page both advertised more
		// and made progress, so a filled collection is provably a prefix.
		if len(out) >= maxItems {
			return partial(domain.AttachmentPartialItemLimit)
		}
		var resp struct {
			Results []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Metadata struct {
					MediaType string `json:"mediaType"`
				} `json:"metadata"`
				Extensions struct {
					FileSize int64  `json:"fileSize"`
					Comment  string `json:"comment"`
				} `json:"extensions"`
				Version struct {
					Number int `json:"number"`
				} `json:"version"`
				Links struct {
					Download string `json:"download"`
				} `json:"_links"`
			} `json:"results"`
			Links struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		q := url.Values{}
		q.Set("expand", "version,metadata")
		q.Set("limit", "200")
		q.Set("start", strconv.Itoa(start))
		path := "/rest/api/content/" + url.PathEscape(id) + "/child/attachment?" + q.Encode()
		if err := cf.c.GetJSON(ctx, path, &resp); err != nil {
			return domain.AttachmentInventory{}, err
		}
		for _, r := range resp.Results {
			if len(out) >= maxItems {
				// One response carried more rows than the cap allows; stop exactly at
				// the cap instead of silently exceeding it.
				return partial(domain.AttachmentPartialItemLimit)
			}
			out = append(out, domain.Attachment{
				ID: r.ID, Title: r.Title, MediaType: r.Metadata.MediaType,
				FileSize: r.Extensions.FileSize, Version: r.Version.Number,
				Comment: r.Extensions.Comment, DownPath: r.Links.Download,
			})
		}
		if resp.Links.Next == "" {
			return domain.AttachmentInventory{Attachments: out, Complete: true}, nil // server exhausted at or under the caps
		}
		if len(resp.Results) == 0 {
			// The server still advertises more but returned nothing, so paging cannot
			// progress. Reporting exhaustion here would fabricate completeness.
			return partial(domain.AttachmentPartialPaginationStalled)
		}
		start += len(resp.Results)
	}
	// The loop only reaches here by exhausting the page cap; every natural exit
	// returns above, so the last page still signaled _links.next.
	return partial(domain.AttachmentPartialPageLimit)
}

// DownloadAttachment streams attachment bytes. version<=0 means latest. The
// download path /download/attachments/<pageID>/<filename>?version=<v> is what
// the draw.io PNG preview uses for an exact revision (verified).
func (cf *Confluence) DownloadAttachment(ctx context.Context, pageID, filename string, version int) (io.ReadCloser, error) {
	p := "/download/attachments/" + url.PathEscape(pageID) + "/" + url.PathEscape(filename)
	if version > 0 {
		p += "?version=" + strconv.Itoa(version)
	}
	return cf.c.GetStream(ctx, p)
}

// UploadAttachment uploads file bytes as an attachment to a page via
// multipart/form-data. DC endpoint: POST /rest/api/content/{pageId}/child/attachment.
// X-Atlassian-Token: nocheck is required to bypass XSRF protection.
func (cf *Confluence) UploadAttachment(ctx context.Context, pageID, filename string, data io.ReadCloser, size int64, comment string) (*domain.Attachment, error) {
	writeContext, _, authorizationErr := cf.authorizeContent(ctx, domain.WriteVerbSet{domain.WriteVerbCreate}, "attachment", "", pageID)
	if authorizationErr != nil {
		_ = data.Close()
		return nil, authorizationErr
	}
	if size < 0 {
		_ = data.Close()
		return nil, fmt.Errorf("%w: upload attachment: size must be non-negative", domain.ErrUsage)
	}
	var framing bytes.Buffer
	w := multipart.NewWriter(&framing)
	if comment != "" {
		if err := w.WriteField("comment", comment); err != nil {
			_ = data.Close()
			return nil, err
		}
	}
	if err := w.WriteField("minorEdit", "true"); err != nil {
		_ = data.Close()
		return nil, err
	}
	if _, err := w.CreateFormFile("file", filename); err != nil {
		_ = data.Close()
		return nil, err
	}
	prefixLen := framing.Len()
	if err := w.Close(); err != nil {
		_ = data.Close()
		return nil, err
	}
	framingBytes := framing.Bytes()
	prefix := append([]byte(nil), framingBytes[:prefixLen]...)
	suffix := append([]byte(nil), framingBytes[prefixLen:]...)
	framingSize := int64(len(prefix)) + int64(len(suffix))
	const maxInt64 = int64(1<<63 - 1)
	if size > maxInt64-framingSize {
		_ = data.Close()
		return nil, fmt.Errorf("%w: upload attachment: multipart body is too large", domain.ErrUsage)
	}
	body := &multipartReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), data, bytes.NewReader(suffix)),
		source: data,
	}
	defer func() { _ = body.Close() }()
	headers := map[string]string{
		"Content-Type":      w.FormDataContentType(),
		"X-Atlassian-Token": "nocheck",
		"Expect":            "100-continue",
	}
	var resp struct {
		Results []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Metadata struct {
				MediaType string `json:"mediaType"`
			} `json:"metadata"`
			Extensions struct {
				FileSize int64  `json:"fileSize"`
				Comment  string `json:"comment"`
			} `json:"extensions"`
			Version struct {
				Number int `json:"number"`
			} `json:"version"`
		} `json:"results"`
	}
	contentLength := framingSize + size
	raw, err := cf.c.DoStreamSized(domain.WithWriteClearance(writeContext), "POST", "/rest/api/content/"+url.PathEscape(pageID)+"/child/attachment", body, contentLength, headers)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: upload attachment: decode response: %w", domain.ErrCheckFailed, err)
	}
	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("%w: upload attachment: empty response", domain.ErrCheckFailed)
	}
	r := resp.Results[0]
	return &domain.Attachment{
		ID: r.ID, Title: r.Title, MediaType: r.Metadata.MediaType,
		FileSize: r.Extensions.FileSize, Version: r.Version.Number,
		Comment: r.Extensions.Comment,
	}, nil
}

// DeleteAttachment deletes an attachment by its content id.
// DC endpoint: DELETE /rest/api/content/{attachmentId}
func (cf *Confluence) DeleteAttachment(ctx context.Context, pageID, attachmentID string) error {
	writeContext, _, err := cf.authorizeContent(ctx, domain.WriteVerbSet{domain.WriteVerbDelete}, "attachment", attachmentID, pageID)
	if err != nil {
		return err
	}
	_, err = cf.c.Do(domain.WithWriteClearance(domain.WithSingleAttempt(writeContext)), "DELETE", "/rest/api/content/"+url.PathEscape(attachmentID), nil, nil)
	return err
}

// Resolve implements domain.AssetResolver for draw.io diagrams and inline
// images: it returns the rendered PNG bytes + the on-disk filename to use.
// The AssetSink API is byte-based, so the stream is buffered here under the
// binary cap (renders are small; huge user attachments go through the
// streaming download path instead).
func (cf *Confluence) Resolve(ctx context.Context, page *domain.Resource, ref domain.Ref) ([]byte, string, error) {
	switch ref.Kind {
	case domain.RefDrawio:
		name := ref.Key + ".png"
		rev := 0
		if v := ref.Params["revision"]; v != "" {
			rev, _ = strconv.Atoi(v)
		}
		data, err := cf.downloadAll(ctx, page.ID, name, rev)
		if err != nil {
			return nil, "", err
		}
		return data, name, nil
	case domain.RefImage:
		data, err := cf.downloadAll(ctx, page.ID, ref.Key, 0)
		if err != nil {
			return nil, "", err
		}
		return data, ref.Key, nil
	default:
		return nil, "", domain.ErrNotFound
	}
}

// downloadAll buffers a full attachment stream under the binary cap.
func (cf *Confluence) downloadAll(ctx context.Context, pageID, filename string, version int) ([]byte, error) {
	rc, err := cf.DownloadAttachment(ctx, pageID, filename, version)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return httpx.ReadCapped(rc, httpx.BinBodyCap)
}

// ResolveUser maps a Confluence userkey (or account-id) to a display name.
// Suitable as a domain.UserResolver. Errors degrade to the raw key upstream.
func (cf *Confluence) ResolveUser(ctx context.Context, key string) (string, error) {
	var out struct {
		DisplayName string `json:"displayName"`
	}
	param := "key"
	// Cloud-style account-ids are long and contain ':' or are 24+ hex chars;
	// DC userkeys are 32 hex. Try key first, then accountId.
	err := cf.c.GetJSON(ctx, "/rest/api/user?"+param+"="+url.QueryEscape(key), &out)
	if err != nil || out.DisplayName == "" {
		var out2 struct {
			DisplayName string `json:"displayName"`
		}
		if e2 := cf.c.GetJSON(ctx, "/rest/api/user?accountId="+url.QueryEscape(key), &out2); e2 == nil && out2.DisplayName != "" {
			return out2.DisplayName, nil
		}
		if err != nil {
			return "", err
		}
	}
	return out.DisplayName, nil
}
