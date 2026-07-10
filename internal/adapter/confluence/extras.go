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

// ListComments returns a page's comments (storage bodies, rendered to text). It
// follows _links.next, paging until the server stops signaling more. truncated
// is true when a safety cap (maxPages/maxItems) stopped the listing while the
// server still signaled _links.next — the mirror must surface that, never bake
// in a silently-truncated set.
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
		for _, r := range resp.Results {
			body := r.Body.Storage.Value
			if root, err := csf.Parse([]byte(body)); err == nil {
				body = csf.TextContent(root)
			}
			out = append(out, domain.Comment{
				ID: r.ID, Author: r.History.CreatedBy.DisplayName,
				Created: r.History.CreatedDate, Body: body,
			})
		}
		if resp.Links.Next == "" || len(resp.Results) == 0 {
			return out, false, nil // server exhausted at or under the cap
		}
		start += len(resp.Results)
	}
	// The loop only reaches here by hitting a safety cap; the sole natural exit
	// returns above, so the last page still signaled _links.next — truncated.
	return out, true, nil
}

// AddComment posts a storage-format comment on a page.
func (cf *Confluence) AddComment(ctx context.Context, id string, body []byte) (*domain.Comment, error) {
	payload := map[string]any{
		"type":      "comment",
		"container": map[string]string{"id": id, "type": "page"},
		"body": map[string]any{
			"storage": map[string]any{"value": string(body), "representation": "storage"},
		},
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := cf.c.SendJSON(ctx, "POST", "/rest/api/content", payload, &out); err != nil {
		return nil, err
	}
	return &domain.Comment{ID: out.ID, Body: string(body)}, nil
}

// ListAttachments returns a page's attachments. It follows _links.next, paging
// until the server stops signaling more.
func (cf *Confluence) ListAttachments(ctx context.Context, id string) ([]domain.Attachment, error) {
	start := 0
	var out []domain.Attachment
	for page := 0; page < maxPages && len(out) < maxItems; page++ {
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
			return nil, err
		}
		for _, r := range resp.Results {
			out = append(out, domain.Attachment{
				ID: r.ID, Title: r.Title, MediaType: r.Metadata.MediaType,
				FileSize: r.Extensions.FileSize, Version: r.Version.Number,
				Comment: r.Extensions.Comment, DownPath: r.Links.Download,
			})
		}
		if resp.Links.Next == "" || len(resp.Results) == 0 {
			break
		}
		start += len(resp.Results)
	}
	return out, nil
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
func (cf *Confluence) UploadAttachment(ctx context.Context, pageID, filename string, data io.ReadCloser, comment string) (*domain.Attachment, error) {
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
	raw, err := cf.c.DoStream(ctx, "POST", "/rest/api/content/"+url.PathEscape(pageID)+"/child/attachment", body, headers)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("upload attachment: decode response: %w", err)
	}
	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("upload attachment: empty response")
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
func (cf *Confluence) DeleteAttachment(ctx context.Context, attachmentID string) error {
	_, err := cf.c.Do(ctx, "DELETE", "/rest/api/content/"+url.PathEscape(attachmentID), nil, nil)
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
