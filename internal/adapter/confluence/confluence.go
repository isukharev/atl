// Package confluence implements domain.DocStore (and AssetResolver/UserResolver)
// against a Confluence Server/Data Center REST API using bearer-PAT auth. Bodies
// are the native Storage Format; the adapter never converts them.
package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// Confluence is the DocStore adapter.
type Confluence struct {
	c    *httpx.Client
	base string
}

// New builds a Confluence adapter for base URL with a PAT.
func New(base, token, version string) *Confluence {
	return &Confluence{c: httpx.New(base, token, version), base: strings.TrimRight(base, "/")}
}

var _ domain.DocStore = (*Confluence)(nil)
var _ domain.AssetResolver = (*Confluence)(nil)

// --- REST DTOs (only the fields we use) ---

type content struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Space struct {
		Key string `json:"key"`
	} `json:"space"`
	Version struct {
		Number  int    `json:"number"`
		When    string `json:"when"`
		Message string `json:"message"`
		By      struct {
			DisplayName string `json:"displayName"`
		} `json:"by"`
	} `json:"version"`
	Ancestors []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"ancestors"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"body"`
	Metadata struct {
		Labels struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		} `json:"labels"`
	} `json:"metadata"`
	Restrictions json.RawMessage `json:"restrictions"`
	Links        struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

func (ct *content) toResource(base, body string) *domain.Resource {
	r := &domain.Resource{
		ID: ct.ID, Title: ct.Title, SpaceKey: ct.Space.Key,
		Version: ct.Version.Number, Body: []byte(body),
	}
	for _, a := range ct.Ancestors {
		r.Ancestors = append(r.Ancestors, a.Title)
	}
	if n := len(ct.Ancestors); n > 0 {
		r.Parent = ct.Ancestors[n-1].ID
	}
	for _, l := range ct.Metadata.Labels.Results {
		r.Labels = append(r.Labels, l.Name)
	}
	if ct.Links.WebUI != "" {
		r.URL = base + ct.Links.WebUI
	}
	return r
}

// GetPage fetches a page; Body is native CSF unless opts.Format=="view".
func (cf *Confluence) GetPage(ctx context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	expand := "body.storage,version,space,ancestors,metadata.labels"
	if opts.Format == "view" {
		expand = "body.view,version,space,ancestors,metadata.labels"
	}
	var ct content
	if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+"?expand="+expand, &ct); err != nil {
		return nil, err
	}
	body := ct.Body.Storage.Value
	if opts.Format == "view" {
		body = ct.Body.View.Value
	}
	return ct.toResource(cf.base, body), nil
}

// GetMeta returns non-body metadata.
func (cf *Confluence) GetMeta(ctx context.Context, id string) (*domain.PageMeta, error) {
	var ct content
	if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+
		"?expand=version,space,ancestors,metadata.labels,restrictions.read.restrictions.user", &ct); err != nil {
		return nil, err
	}
	m := &domain.PageMeta{ID: ct.ID, Title: ct.Title, Space: ct.Space.Key, Version: ct.Version.Number}
	for _, a := range ct.Ancestors {
		m.Ancestors = append(m.Ancestors, a.Title)
	}
	for _, l := range ct.Metadata.Labels.Results {
		m.Labels = append(m.Labels, l.Name)
	}
	m.Restrictions = len(ct.Restrictions) > 0 && !strings.Contains(string(ct.Restrictions), `"results":[]`)
	if ct.Links.WebUI != "" {
		m.URL = cf.base + ct.Links.WebUI
	}
	return m, nil
}

// History returns version records, newest first.
func (cf *Confluence) History(ctx context.Context, id string) ([]domain.Version, error) {
	var resp struct {
		Results []struct {
			Number  int    `json:"number"`
			When    string `json:"when"`
			Message string `json:"message"`
			By      struct {
				DisplayName string `json:"displayName"`
			} `json:"by"`
		} `json:"results"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+"/version?limit=50", &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Version, 0, len(resp.Results))
	for _, v := range resp.Results {
		out = append(out, domain.Version{Number: v.Number, When: v.When, By: v.By.DisplayName, Message: v.Message})
	}
	return out, nil
}

// UpdatePage pushes a new body under the optimistic version gate. We PUT with
// version.number = expectVersion+1; Confluence rejects (409) unless the remote
// is exactly at expectVersion — that is the drift refusal. force re-reads the
// current version and bumps from there.
func (cf *Confluence) UpdatePage(ctx context.Context, id string, expectVersion int, title string, body []byte, force bool) (int, error) {
	next := expectVersion + 1
	if force {
		var cur content
		if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+"?expand=version", &cur); err != nil {
			return 0, err
		}
		next = cur.Version.Number + 1
		if title == "" {
			title = cur.Title
		}
	}
	if title == "" {
		// Title is required by the API; fetch it if the caller didn't supply one.
		var cur content
		if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+"?expand=version", &cur); err != nil {
			return 0, err
		}
		title = cur.Title
		if !force && cur.Version.Number != expectVersion {
			return 0, fmt.Errorf("%w: remote is v%d, expected v%d", domain.ErrVersionConflict, cur.Version.Number, expectVersion)
		}
	}
	payload := map[string]any{
		"type":    "page",
		"title":   title,
		"version": map[string]any{"number": next},
		"body": map[string]any{
			"storage": map[string]any{"value": string(body), "representation": "storage"},
		},
	}
	var out content
	err := cf.c.SendJSON(ctx, "PUT", "/rest/api/content/"+url.PathEscape(id), payload, &out)
	if err != nil {
		return 0, err
	}
	return out.Version.Number, nil
}

// CreatePage creates a new page.
func (cf *Confluence) CreatePage(ctx context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	payload := map[string]any{
		"type":  "page",
		"title": title,
		"space": map[string]string{"key": space},
		"body": map[string]any{
			"storage": map[string]any{"value": string(body), "representation": "storage"},
		},
	}
	if parent != "" {
		payload["ancestors"] = []map[string]string{{"id": parent}}
	}
	var out content
	if err := cf.c.SendJSON(ctx, "POST", "/rest/api/content", payload, &out); err != nil {
		return nil, err
	}
	return out.toResource(cf.base, out.Body.Storage.Value), nil
}

// MovePage reparents by reading the page and re-putting it with new ancestors.
// Unlike push, this always targets the latest version (read-modify-write); a
// concurrent edit between the read and the PUT surfaces as a 409.
func (cf *Confluence) MovePage(ctx context.Context, id, newParent string) error {
	var cur content
	if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+"?expand=version,body.storage", &cur); err != nil {
		return err
	}
	payload := map[string]any{
		"type":      "page",
		"title":     cur.Title,
		"version":   map[string]any{"number": cur.Version.Number + 1},
		"ancestors": []map[string]string{{"id": newParent}},
		"body": map[string]any{
			"storage": map[string]any{"value": cur.Body.Storage.Value, "representation": "storage"},
		},
	}
	return cf.c.SendJSON(ctx, "PUT", "/rest/api/content/"+url.PathEscape(id), payload, nil)
}

// DeletePage trashes a page. Per-space permissions may yield ErrForbidden.
func (cf *Confluence) DeletePage(ctx context.Context, id string) error {
	_, err := cf.c.Do(ctx, "DELETE", "/rest/api/content/"+url.PathEscape(id), nil, nil)
	return err
}
