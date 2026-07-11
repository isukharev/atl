// Package confluence implements domain.DocStore (and AssetResolver/UserResolver)
// against a Confluence Server/Data Center REST API using bearer-PAT auth. Bodies
// are the native Storage Format; the adapter never converts them.
package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
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
var _ domain.Verifier = (*Confluence)(nil)

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
	Ancestors *[]struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"ancestors"`
	Body struct {
		Storage *struct {
			Value *string `json:"value"`
		} `json:"storage"`
		View *struct {
			Value *string `json:"value"`
		} `json:"view"`
	} `json:"body"`
	Metadata struct {
		Labels struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		} `json:"labels"`
	} `json:"metadata"`
	Restrictions *struct {
		Read *struct {
			Restrictions *struct {
				User  *restrictionSubjects `json:"user"`
				Group *restrictionSubjects `json:"group"`
			} `json:"restrictions"`
		} `json:"read"`
	} `json:"restrictions"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

type restrictionSubjects struct {
	Results *[]json.RawMessage `json:"results"`
}

// restrictionState returns nil unless the expanded response explicitly
// contains both user and group result arrays. A partial expansion is not proof
// that the page is unrestricted.
func (ct *content) restrictionState() *bool {
	if ct == nil || ct.Restrictions == nil || ct.Restrictions.Read == nil || ct.Restrictions.Read.Restrictions == nil {
		return nil
	}
	r := ct.Restrictions.Read.Restrictions
	if r.User == nil || r.Group == nil || r.User.Results == nil || r.Group.Results == nil {
		return nil
	}
	restricted := len(*r.User.Results) > 0 || len(*r.Group.Results) > 0
	return &restricted
}

func (ct *content) toResource(base, body string) *domain.Resource {
	r := &domain.Resource{
		ID: ct.ID, Type: ct.Type, Title: ct.Title, SpaceKey: ct.Space.Key,
		Version: ct.Version.Number, Body: []byte(body), Updated: ct.Version.When,
		AncestorsPresent: ct.Ancestors != nil,
	}
	if ct.Ancestors != nil {
		for _, a := range *ct.Ancestors {
			r.Ancestors = append(r.Ancestors, a.Title)
			r.AncestorIDs = append(r.AncestorIDs, a.ID)
		}
		if n := len(*ct.Ancestors); n > 0 {
			r.Parent = (*ct.Ancestors)[n-1].ID
		}
	}
	for _, l := range ct.Metadata.Labels.Results {
		r.Labels = append(r.Labels, l.Name)
	}
	if ct.Links.WebUI != "" {
		r.URL = base + ct.Links.WebUI
	}
	return r
}

func (ct *content) storageBody() (string, bool) {
	if ct == nil || ct.Body.Storage == nil || ct.Body.Storage.Value == nil {
		return "", false
	}
	return *ct.Body.Storage.Value, true
}

// GetPage fetches a page; Body is native CSF unless opts.Format=="view".
func (cf *Confluence) GetPage(ctx context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	expand := "body.storage,version,space,ancestors,metadata.labels"
	if opts.Format == "view" {
		expand = "body.view,version,space,ancestors,metadata.labels"
	}
	if opts.IncludeRestrictions {
		expand += ",restrictions.read.restrictions.user,restrictions.read.restrictions.group"
	}
	var ct content
	if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+"?expand="+expand, &ct); err != nil {
		return nil, err
	}
	body, present := ct.storageBody()
	if opts.Format == "view" {
		present = ct.Body.View != nil && ct.Body.View.Value != nil
		if present {
			body = *ct.Body.View.Value
		} else {
			body = ""
		}
	}
	r := ct.toResource(cf.base, body)
	r.BodyPresent = present
	if opts.IncludeRestrictions {
		r.Restricted = ct.restrictionState()
	}
	return r, nil
}

// GetMeta returns non-body metadata.
func (cf *Confluence) GetMeta(ctx context.Context, id string) (*domain.PageMeta, error) {
	var ct content
	if err := cf.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id)+
		"?expand=version,space,ancestors,metadata.labels,"+
		"restrictions.read.restrictions.user,restrictions.read.restrictions.group", &ct); err != nil {
		return nil, err
	}
	m := &domain.PageMeta{ID: ct.ID, Title: ct.Title, Space: ct.Space.Key, Version: ct.Version.Number, Updated: ct.Version.When}
	if ct.Ancestors != nil {
		for _, a := range *ct.Ancestors {
			m.Ancestors = append(m.Ancestors, a.Title)
		}
	}
	for _, l := range ct.Metadata.Labels.Results {
		m.Labels = append(m.Labels, l.Name)
	}
	m.Restrictions = ct.restrictionState()
	if ct.Links.WebUI != "" {
		m.URL = cf.base + ct.Links.WebUI
	}
	return m, nil
}

// Whoami confirms the PAT by fetching the current user and returns their
// display name. Used by `atl auth login` to validate credentials before they
// are persisted.
func (cf *Confluence) Whoami(ctx context.Context) (string, error) {
	var u struct {
		DisplayName string `json:"displayName"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/user/current", &u); err != nil {
		return "", err
	}
	return u.DisplayName, nil
}

// History returns version records, newest first. It pages until the listing is
// exhausted (previously it returned only the first 50 versions).
func (cf *Confluence) History(ctx context.Context, id string) ([]domain.Version, error) {
	start := 0
	out := []domain.Version{}
	for page := 0; page < maxPages && len(out) < maxItems; page++ {
		var resp struct {
			Results []struct {
				Number  int    `json:"number"`
				When    string `json:"when"`
				Message string `json:"message"`
				By      struct {
					DisplayName string `json:"displayName"`
				} `json:"by"`
			} `json:"results"`
			Links struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		q := url.Values{}
		q.Set("limit", "100")
		q.Set("start", strconv.Itoa(start))
		// Confluence Data Center serves the full version list under
		// /rest/experimental; the Cloud-style /rest/api/content/{id}/version path
		// 404s on DC.
		if err := cf.c.GetJSON(ctx, "/rest/experimental/content/"+url.PathEscape(id)+"/version?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, v := range resp.Results {
			out = append(out, domain.Version{Number: v.Number, When: v.When, By: v.By.DisplayName, Message: v.Message})
		}
		if resp.Links.Next == "" || len(resp.Results) == 0 {
			break
		}
		start += len(resp.Results)
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
	bodyValue, present := out.storageBody()
	r := out.toResource(cf.base, bodyValue)
	r.BodyPresent = present
	return r, nil
}

// MovePage performs one version-gated ancestor update. Fresh reads, cycle
// checks, and ambiguous-outcome reconciliation belong to the app layer.
func (cf *Confluence) MovePage(ctx context.Context, id, newParent string, expectVersion int, title string, body []byte) (int, error) {
	payload := map[string]any{
		"type":      "page",
		"title":     title,
		"version":   map[string]any{"number": expectVersion + 1},
		"ancestors": []map[string]string{{"id": newParent}},
		"body": map[string]any{
			"storage": map[string]any{"value": string(body), "representation": "storage"},
		},
	}
	var out struct {
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := cf.c.SendJSON(ctx, "PUT", "/rest/api/content/"+url.PathEscape(id), payload, &out); err != nil {
		return 0, err
	}
	return out.Version.Number, nil
}

// DeletePage trashes a page. Per-space permissions may yield ErrForbidden.
func (cf *Confluence) DeletePage(ctx context.Context, id string) error {
	_, err := cf.c.Do(ctx, "DELETE", "/rest/api/content/"+url.PathEscape(id), nil, nil)
	return err
}
