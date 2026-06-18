package confluence

import (
	"context"
	"net/url"
	"strconv"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// ListComments returns a page's comments (storage bodies, rendered to text).
func (cf *Confluence) ListComments(ctx context.Context, id string) ([]domain.Comment, error) {
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
	}
	path := "/rest/api/content/" + url.PathEscape(id) + "/child/comment?expand=body.storage,history&limit=100"
	if err := cf.c.GetJSON(ctx, path, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Comment, 0, len(resp.Results))
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
	return out, nil
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

// ListAttachments returns a page's attachments.
func (cf *Confluence) ListAttachments(ctx context.Context, id string) ([]domain.Attachment, error) {
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
	}
	path := "/rest/api/content/" + url.PathEscape(id) + "/child/attachment?expand=version,metadata&limit=200"
	if err := cf.c.GetJSON(ctx, path, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Attachment, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, domain.Attachment{
			ID: r.ID, Title: r.Title, MediaType: r.Metadata.MediaType,
			FileSize: r.Extensions.FileSize, Version: r.Version.Number,
			Comment: r.Extensions.Comment, DownPath: r.Links.Download,
		})
	}
	return out, nil
}

// DownloadAttachment fetches attachment bytes. version<=0 means latest. The
// download path /download/attachments/<pageID>/<filename>?version=<v> is what
// the draw.io PNG preview uses for an exact revision (verified).
func (cf *Confluence) DownloadAttachment(ctx context.Context, pageID, filename string, version int) ([]byte, error) {
	p := "/download/attachments/" + url.PathEscape(pageID) + "/" + url.PathEscape(filename)
	if version > 0 {
		p += "?version=" + strconv.Itoa(version)
	}
	return cf.c.GetBytes(ctx, p)
}

// Resolve implements domain.AssetResolver for draw.io diagrams and inline
// images: it returns the rendered PNG bytes + the on-disk filename to use.
func (cf *Confluence) Resolve(ctx context.Context, page *domain.Resource, ref domain.Ref) ([]byte, string, error) {
	switch ref.Kind {
	case domain.RefDrawio:
		name := ref.Key + ".png"
		rev := 0
		if v := ref.Params["revision"]; v != "" {
			rev, _ = strconv.Atoi(v)
		}
		data, err := cf.DownloadAttachment(ctx, page.ID, name, rev)
		if err != nil {
			return nil, "", err
		}
		return data, name, nil
	case domain.RefImage:
		data, err := cf.DownloadAttachment(ctx, page.ID, ref.Key, 0)
		if err != nil {
			return nil, "", err
		}
		return data, ref.Key, nil
	default:
		return nil, "", domain.ErrNotFound
	}
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
