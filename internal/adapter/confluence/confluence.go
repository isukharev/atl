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
	c          *httpx.Client
	base       string
	authorizer domain.WriteAuthorizer
	identity   *confluenceIdentityCache
}

// Option configures transport-neutral adapter behavior.
type Option func(*Confluence)

// WithWriteAuthorizer enables content-scoped last-hop authorization. A nil
// authorizer is equivalent to omitting the option.
func WithWriteAuthorizer(authorizer domain.WriteAuthorizer) Option {
	return func(cf *Confluence) { cf.authorizer = authorizer }
}

// New builds a Confluence adapter for base URL with a PAT.
func New(base, token, version string, options ...Option) *Confluence {
	return NewWithScheduler(base, token, version, nil, options...)
}

// NewWithScheduler shares a command-scoped request scheduler with every
// Confluence transport path, including comments and streamed assets.
func NewWithScheduler(base, token, version string, scheduler *httpx.Scheduler, options ...Option) *Confluence {
	c := httpx.NewWithScheduler(base, token, version, scheduler)
	return newWithClient(base, c, options...)
}

// NewWithSchedulerTLS builds a Confluence adapter with backend-specific trust
// material. Existing constructors preserve the default transport path.
func NewWithSchedulerTLS(base, token, version string, scheduler *httpx.Scheduler, tlsOptions httpx.TLSOptions, options ...Option) (*Confluence, error) {
	c, err := httpx.NewWithSchedulerTLS(base, token, version, scheduler, tlsOptions)
	if err != nil {
		return nil, err
	}
	return newWithClient(base, c, options...), nil
}

func newWithClient(base string, c *httpx.Client, options ...Option) *Confluence {
	cf := &Confluence{c: c, base: strings.TrimRight(base, "/"), identity: newConfluenceIdentityCache()}
	for _, option := range options {
		if option != nil {
			option(cf)
		}
	}
	c.RequireWriteClearance()
	return cf
}

var _ domain.DocStore = (*Confluence)(nil)
var _ domain.AssetResolver = (*Confluence)(nil)
var _ domain.Verifier = (*Confluence)(nil)
var _ domain.PageShortLinkResolver = (*Confluence)(nil)
var _ domain.CompletePageSearcher = (*Confluence)(nil)
var _ domain.QualifiedConfluencePageMetadataBatchReader = (*Confluence)(nil)
var _ domain.ConfluenceTimeSemanticsReader = (*Confluence)(nil)
var _ domain.ServerMetadataReader = (*Confluence)(nil)
var _ domain.ConfluenceCurrentUserReader = (*Confluence)(nil)

func (cf *Confluence) ResolveShortPageLink(ctx context.Context, path string) (string, error) {
	return cf.c.ResolveGET(ctx, path)
}

// --- REST DTOs (only the fields we use) ---

type content struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Title  string `json:"title"`
	Space  struct {
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
		ID: ct.ID, Type: ct.Type, Status: ct.Status, Title: ct.Title, SpaceKey: ct.Space.Key,
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
	r.URL = confluenceWebURL(base, ct.Links.WebUI)
	return r
}

// confluenceWebURL converts the backend-provided webui path to a same-origin
// browser target. The REST field is untrusted: parse it before joining and
// never allow an absolute/network-path reference, userinfo, or scheme change
// to reach an OS browser opener.
func confluenceWebURL(base, webUI string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	webUI = strings.TrimSpace(webUI)
	if base == "" || webUI == "" {
		return ""
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.User != nil || baseURL.Host == "" || baseURL.RawQuery != "" || baseURL.Fragment != "" || (baseURL.Scheme != "https" && baseURL.Scheme != "http") {
		return ""
	}
	ref, err := url.Parse(webUI)
	if err != nil || ref.IsAbs() || ref.Host != "" || ref.User != nil {
		return ""
	}
	for _, segment := range strings.Split(ref.Path, "/") {
		if segment == "." || segment == ".." {
			return ""
		}
	}
	joined, err := url.Parse(base + "/" + strings.TrimLeft(webUI, "/"))
	if err != nil || joined.User != nil || !strings.EqualFold(joined.Host, baseURL.Host) || joined.Scheme != baseURL.Scheme {
		return ""
	}
	return joined.String()
}

func (ct *content) storageBody() (string, bool) {
	if ct == nil || ct.Body.Storage == nil || ct.Body.Storage.Value == nil {
		return "", false
	}
	return *ct.Body.Storage.Value, true
}

// GetPage fetches a page; Body is native CSF unless opts.Format=="view".
func (cf *Confluence) GetPage(ctx context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	return cf.getPage(ctx, id, "", opts)
}

// GetPageByStatus fetches content from one explicit status namespace. The
// response status is retained for app-layer exact-state reconciliation.
func (cf *Confluence) GetPageByStatus(ctx context.Context, id, status string, opts domain.PullOpts) (*domain.Resource, error) {
	status = strings.TrimSpace(status)
	if status != "current" && status != "trashed" {
		return nil, fmt.Errorf("%w: Confluence page status must be current or trashed", domain.ErrUsage)
	}
	return cf.getPage(ctx, id, status, opts)
}

func (cf *Confluence) getPage(ctx context.Context, id, status string, opts domain.PullOpts) (*domain.Resource, error) {
	expand := "body.storage,version,space,ancestors,metadata.labels"
	if opts.Format == "view" {
		expand = "body.view,version,space,ancestors,metadata.labels"
	}
	if opts.IncludeRestrictions {
		expand += ",restrictions.read.restrictions.user,restrictions.read.restrictions.group"
	}
	requestPath := "/rest/api/content/" + url.PathEscape(id) + "?expand=" + expand
	if status != "" {
		requestPath = "/rest/api/content/" + url.PathEscape(id) + "?status=" + url.QueryEscape(status) + "&expand=" + expand
	}
	var ct content
	if err := cf.c.GetJSON(ctx, requestPath, &ct); err != nil {
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
	cf.rememberResource(r)
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
	m := &domain.PageMeta{ID: ct.ID, Type: ct.Type, Title: ct.Title, Space: ct.Space.Key, Version: ct.Version.Number, Updated: ct.Version.When}
	if ct.Ancestors != nil {
		m.AncestorIDs = make([]string, 0, len(*ct.Ancestors))
		for _, a := range *ct.Ancestors {
			m.Ancestors = append(m.Ancestors, a.Title)
			m.AncestorIDs = append(m.AncestorIDs, a.ID)
		}
	}
	for _, l := range ct.Metadata.Labels.Results {
		m.Labels = append(m.Labels, l.Name)
	}
	m.Restrictions = ct.restrictionState()
	m.URL = confluenceWebURL(cf.base, ct.Links.WebUI)
	if identity, err := identityFromMeta(m, id, domain.WriteScopeRequirements{Space: true, Ancestors: true}); err == nil && cf.identity != nil {
		cf.identity.put(identity)
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

// CurrentConfluenceUser returns the authenticated user's stable backend
// identity without requesting or retaining email. Data Center userKey is
// preferred; username is the documented compatibility fallback.
func (cf *Confluence) CurrentConfluenceUser(ctx context.Context) (domain.ConfluenceUserIdentity, error) {
	var u struct {
		UserKey     string `json:"userKey"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/user/current", &u); err != nil {
		return domain.ConfluenceUserIdentity{}, err
	}
	id := u.UserKey
	if id == "" {
		id = u.Username
	}
	identity := domain.ConfluenceUserIdentity{ID: id, DisplayName: u.DisplayName}
	if err := domain.ValidateConfluenceUserIdentity(identity); err != nil {
		return domain.ConfluenceUserIdentity{}, err
	}
	return identity, nil
}

// CurrentUserTimeZone returns only the current user's observed timezone
// preference. It is not treated as evidence of Confluence's CQL parser zone.
func (cf *Confluence) CurrentUserTimeZone(ctx context.Context) (string, error) {
	var u struct {
		TimeZone string `json:"timeZone"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/user/current", &u); err != nil {
		return "", err
	}
	return u.TimeZone, nil
}

// History returns version records, newest first. It is the compatibility
// surface: it delegates to HistoryQualified and intentionally drops the
// qualification, so a listing cut short by a safety cap is indistinguishable
// from an exhausted one. New evidence-facing callers that must not mistake a
// capped listing for a complete one use HistoryQualified instead.
func (cf *Confluence) History(ctx context.Context, id string) ([]domain.Version, error) {
	inventory, err := cf.HistoryQualified(ctx, id)
	if err != nil {
		return nil, err
	}
	return inventory.Versions, nil
}

// HistoryQualified returns a page's version records (newest first) with
// explicit completeness. It follows _links.next until the server stops
// signaling more (complete) and otherwise reports the exact limiter that
// stopped it: the page cap, the item cap, or a page that advertised more while
// making no progress. A terminal page that signals no next cursor is exhaustion
// even when it is empty; a page that advertises more while returning nothing is
// stalled, not exhausted. The item cap is enforced per version, so the returned
// slice never exceeds it silently.
func (cf *Confluence) HistoryQualified(ctx context.Context, id string) (domain.VersionInventory, error) {
	start := 0
	out := []domain.Version{}
	partial := func(reason string) (domain.VersionInventory, error) {
		return domain.VersionInventory{Versions: out, PartialReason: reason}, nil
	}
	for page := 0; page < maxPages; page++ {
		// Reaching a later iteration means the previous page both advertised more
		// and made progress, so a filled collection is provably a prefix.
		if len(out) >= maxItems {
			return partial(domain.HistoryPartialItemLimit)
		}
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
			return domain.VersionInventory{}, err
		}
		for _, v := range resp.Results {
			if len(out) >= maxItems {
				// One response carried more rows than the cap allows; stop exactly at
				// the cap instead of silently exceeding it.
				return partial(domain.HistoryPartialItemLimit)
			}
			out = append(out, domain.Version{Number: v.Number, When: v.When, By: v.By.DisplayName, Message: v.Message})
		}
		if resp.Links.Next == "" {
			return domain.VersionInventory{Versions: out, Complete: true}, nil // server exhausted at or under the caps
		}
		if len(resp.Results) == 0 {
			// The server still advertises more but returned nothing, so paging cannot
			// progress. Reporting exhaustion here would fabricate completeness.
			return partial(domain.HistoryPartialPaginationStalled)
		}
		start += len(resp.Results)
	}
	// The loop only reaches here by exhausting the page cap; every natural exit
	// returns above, so the last page still signaled _links.next.
	return partial(domain.HistoryPartialPageLimit)
}

// UpdatePage pushes a new body under the optimistic version gate. We PUT with
// version.number = expectVersion+1; Confluence rejects (409) unless the remote
// is exactly at expectVersion — that is the drift refusal. force re-reads the
// current version and bumps from there.
func (cf *Confluence) UpdatePage(ctx context.Context, id string, expectVersion int, title string, body []byte, force bool) (int, error) {
	writeContext, _, err := cf.authorizeContent(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate}, "page", id, id)
	if err != nil {
		return 0, err
	}
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
	err = cf.c.SendJSON(domain.WithWriteClearance(writeContext), "PUT", "/rest/api/content/"+url.PathEscape(id), payload, &out)
	if err != nil {
		return 0, err
	}
	return out.Version.Number, nil
}

// CreatePage creates a new page.
func (cf *Confluence) CreatePage(ctx context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	writeContext, err := cf.authorizeCreate(ctx, "page", space, parent)
	if err != nil {
		return nil, err
	}
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
	if err := cf.c.SendJSON(domain.WithWriteClearance(writeContext), "POST", "/rest/api/content", payload, &out); err != nil {
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
	writeContext, err := cf.authorizeMove(ctx, id, newParent)
	if err != nil {
		return 0, err
	}
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
	if err := cf.c.SendJSON(domain.WithWriteClearance(writeContext), "PUT", "/rest/api/content/"+url.PathEscape(id), payload, &out); err != nil {
		return 0, err
	}
	if cf.identity != nil {
		cf.identity.evictSubtree(id)
	}
	return out.Version.Number, nil
}

// DeletePage trashes a page. Per-space permissions may yield ErrForbidden.
func (cf *Confluence) DeletePage(ctx context.Context, id string) error {
	writeContext, target, err := cf.authorizeContent(ctx, domain.WriteVerbSet{domain.WriteVerbDelete}, "page", id, id)
	if err != nil {
		return err
	}
	if cf.authorizer != nil {
		writeContext, err = cf.authorizeHierarchy(writeContext, domain.WriteVerbSet{domain.WriteVerbDelete}, target)
		if err == nil {
			writeContext, err = cf.authorizePageDelete(writeContext, target)
		}
		if err != nil {
			return err
		}
	}
	_, err = cf.c.Do(domain.WithWriteClearance(domain.WithSingleAttempt(writeContext)), "DELETE", "/rest/api/content/"+url.PathEscape(id)+"?status=current", nil, nil)
	return err
}
