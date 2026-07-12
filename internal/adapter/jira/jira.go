// Package jira implements domain.Tracker against a Jira Server/Data Center REST
// v2 API with bearer-PAT auth. Issue bodies are native Jira wiki markup; the
// adapter does not convert them.
package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
	"github.com/isukharev/atl/internal/jiramap"
)

// Jira is the Tracker adapter.
type Jira struct {
	c    *httpx.Client
	base string
}

// New builds a Jira adapter for base URL with a PAT.
func New(base, token, version string) *Jira {
	c := httpx.New(base, token, version)
	// Jira DC has no optimistic version gate: a 409 is a generic conflict
	// (locked issue, closed sprint, workflow veto), never a version conflict —
	// exit 5's re-pull/--force remediation does not apply here.
	c.SetNoVersionGate()
	return &Jira{c: c, base: strings.TrimRight(base, "/")}
}

var _ domain.Tracker = (*Jira)(nil)
var _ domain.CompleteChangelogReader = (*Jira)(nil)
var _ domain.Verifier = (*Jira)(nil)

const defaultFields = "summary,description,status,issuetype,project,assignee,reporter,labels,issuelinks,comment,attachment"

// --- DTOs ---

type issueDTO struct {
	ID     string         `json:"id"`
	Key    string         `json:"key"`
	Fields map[string]any `json:"fields"`
}

func (j *Jira) mapIssue(d issueDTO) *domain.Issue {
	return MapIssueFields(d.ID, d.Key, d.Fields)
}

// MapIssueFields is the adapter's compatibility wrapper around the shared,
// transport-neutral snapshot mapper.
func MapIssueFields(id, key string, fields map[string]any) *domain.Issue {
	return jiramap.Issue(id, key, fields)
}

// GetIssue fetches one issue. If fields is empty a sensible default set is used.
func (j *Jira) GetIssue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	fq := defaultFields
	if len(fields) > 0 {
		fq = strings.Join(fields, ",")
	}
	var d issueDTO
	if err := j.c.GetJSONUseNumber(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"?fields="+url.QueryEscape(fq), &d); err != nil {
		return nil, err
	}
	return j.mapIssue(d), nil
}

// parseCursor parses a pagination cursor (a startAt offset). Empty means the
// first page; a non-numeric or negative value is a usage error rather than a
// silent restart from offset 0.
func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid cursor %q (expected a non-negative offset)", domain.ErrUsage, cursor)
	}
	return n, nil
}

// Search runs JQL. cursor is the startAt offset; returns the next offset or "".
func (j *Jira) Search(ctx context.Context, jql string, fields []string, limit int, cursor string) ([]domain.Issue, string, error) {
	return j.search(ctx, jql, fields, limit, cursor, false)
}

// SearchLenient is reserved for generated identity joins. Ordinary Search
// keeps Jira's strict semantic validation for user-authored JQL.
func (j *Jira) SearchLenient(ctx context.Context, jql string, fields []string, limit int, cursor string) ([]domain.Issue, string, error) {
	return j.search(ctx, jql, fields, limit, cursor, true)
}

func (j *Jira) search(ctx context.Context, jql string, fields []string, limit int, cursor string, lenient bool) ([]domain.Issue, string, error) {
	startAt, err := parseCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	fq := "summary,status,issuetype,project,assignee,labels"
	if len(fields) > 0 {
		fq = strings.Join(fields, ",")
	}
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(limit))
	q.Set("fields", fq)
	if lenient {
		q.Set("validateQuery", "false")
	}
	var resp struct {
		Issues     []issueDTO `json:"issues"`
		StartAt    int        `json:"startAt"`
		MaxResults int        `json:"maxResults"`
		Total      int        `json:"total"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/search?"+q.Encode(), &resp); err != nil {
		return nil, "", err
	}
	out := make([]domain.Issue, 0, len(resp.Issues))
	for _, d := range resp.Issues {
		out = append(out, *j.mapIssue(d))
	}
	next := ""
	if startAt+len(resp.Issues) < resp.Total && len(resp.Issues) > 0 {
		next = strconv.Itoa(startAt + len(resp.Issues))
	}
	return out, next, nil
}

// Create creates an issue. Each extra field value that parses as valid JSON is
// sent as the decoded JSON value (so callers can pass objects, arrays or
// numbers, e.g. priority={"name":"High"}); otherwise it is sent as a string.
func (j *Jira) Create(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]string) (*domain.Issue, error) {
	fl := map[string]any{
		"project":   map[string]string{"key": project},
		"issuetype": map[string]string{"name": issueType},
		"summary":   summary,
	}
	if len(body) > 0 {
		fl["description"] = string(body)
	}
	for k, v := range fields {
		fl[k] = coerceField(v)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := j.c.SendJSON(ctx, "POST", "/rest/api/2/issue", map[string]any{"fields": fl}, &out); err != nil {
		return nil, err
	}
	return &domain.Issue{Key: out.Key, Summary: summary, Project: project, Type: issueType, Body: string(body)}, nil
}

// Update edits summary/description/extra fields. Extra field values are coerced
// the same way as in Create (JSON-decoded when valid, else sent as a string).
//
// Update is last-writer-wins: it issues a bare PUT with no optimistic version
// gate. Jira Server/DC has no per-field compare-and-set and exposes no per-issue
// version counter, so concurrent edits silently overwrite each other.
func (j *Jira) Update(ctx context.Context, key, summary string, body []byte, fields map[string]string) error {
	fl := map[string]any{}
	if summary != "" {
		fl["summary"] = summary
	}
	if body != nil {
		fl["description"] = string(body)
	}
	for k, v := range fields {
		fl[k] = coerceField(v)
	}
	if len(fl) == 0 {
		return fmt.Errorf("%w: nothing to update", domain.ErrUsage)
	}
	return j.c.SendJSON(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(key), map[string]any{"fields": fl}, nil)
}

// SetFields writes explicitly typed values as-is. Unlike Update's compatibility
// map[string]string path, it performs no JSON-shape guessing.
func (j *Jira) SetFields(ctx context.Context, key string, fields map[string]any) error {
	if len(fields) == 0 {
		return fmt.Errorf("%w: no fields to update", domain.ErrUsage)
	}
	return j.c.SendJSON(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(key), map[string]any{"fields": fields}, nil)
}

// Transition moves an issue to a target status by name, optionally commenting
// and setting fields on the transition (coerced like Create/Update fields).
func (j *Jira) Transition(ctx context.Context, key, to, comment string, fields map[string]string) error {
	trs, err := j.Transitions(ctx, key)
	if err != nil {
		return err
	}
	var id string
	for _, t := range trs {
		if strings.EqualFold(t.Name, to) || strings.EqualFold(t.To, to) {
			id = t.ID
			break
		}
	}
	if id == "" {
		names := make([]string, len(trs))
		for i, t := range trs {
			names[i] = t.Name
		}
		return fmt.Errorf("%w: no transition to %q; available: %s", domain.ErrUsage, to, strings.Join(names, ", "))
	}
	payload := map[string]any{"transition": map[string]string{"id": id}}
	if len(fields) > 0 {
		fl := map[string]any{}
		for k, v := range fields {
			fl[k] = coerceField(v)
		}
		payload["fields"] = fl
	}
	if comment != "" {
		payload["update"] = map[string]any{"comment": []any{map[string]any{"add": map[string]string{"body": comment}}}}
	}
	return j.c.SendJSON(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", payload, nil)
}

// DeleteIssue permanently deletes an issue. deleteSubtasks must be true to
// delete an issue that still has subtasks (else Jira returns 400).
func (j *Jira) DeleteIssue(ctx context.Context, key string, deleteSubtasks bool) error {
	q := url.Values{}
	q.Set("deleteSubtasks", strconv.FormatBool(deleteSubtasks))
	return j.c.SendJSON(ctx, "DELETE", "/rest/api/2/issue/"+url.PathEscape(key)+"?"+q.Encode(), nil, nil)
}

// UpdateLabels adds/removes labels via the field-update verb so it doesn't
// clobber labels set by others (unlike a full PUT of the labels array).
func (j *Jira) UpdateLabels(ctx context.Context, key string, add, remove []string) error {
	var ops []any
	for _, l := range add {
		ops = append(ops, map[string]string{"add": l})
	}
	for _, l := range remove {
		ops = append(ops, map[string]string{"remove": l})
	}
	if len(ops) == 0 {
		return fmt.Errorf("%w: nothing to change (pass --add and/or --remove)", domain.ErrUsage)
	}
	payload := map[string]any{"update": map[string]any{"labels": ops}}
	return j.c.SendJSON(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(key), payload, nil)
}

// Assign sets the issue assignee via the dedicated endpoint. Jira DC's field
// update path expects an object for assignee, which makes it an easy trap;
// this uses PUT /issue/{key}/assignee with {"name": <username>} instead. An
// empty username sends {"name": null}, which unassigns the issue.
func (j *Jira) Assign(ctx context.Context, key, username string) error {
	payload := map[string]any{"name": nil}
	if username != "" {
		payload["name"] = username
	}
	return j.c.SendJSON(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(key)+"/assignee", payload, nil)
}

type userDTO struct {
	Name         string `json:"name"`
	Key          string `json:"key"`
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	Active       bool   `json:"active"`
}

func mapUser(d userDTO) domain.User {
	return domain.User{
		Name: d.Name, Key: d.Key, AccountID: d.AccountID,
		DisplayName: d.DisplayName, Email: d.EmailAddress, Active: d.Active,
	}
}

// CurrentUser returns the authenticated user.
func (j *Jira) CurrentUser(ctx context.Context) (*domain.User, error) {
	var d userDTO
	if err := j.c.GetJSON(ctx, "/rest/api/2/myself", &d); err != nil {
		return nil, err
	}
	u := mapUser(d)
	return &u, nil
}

// SearchUsers finds users. DC's endpoint matches on the `username` query
// parameter (Cloud uses `query`); this targets Data Center.
func (j *Jira) SearchUsers(ctx context.Context, query string, limit int) ([]domain.User, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	q := url.Values{}
	q.Set("username", query)
	q.Set("maxResults", strconv.Itoa(limit))
	var arr []userDTO
	if err := j.c.GetJSON(ctx, "/rest/api/2/user/search?"+q.Encode(), &arr); err != nil {
		return nil, err
	}
	out := make([]domain.User, 0, len(arr))
	for _, d := range arr {
		out = append(out, mapUser(d))
	}
	return out, nil
}

// GetUser fetches one user by DC username.
func (j *Jira) GetUser(ctx context.Context, username string) (*domain.User, error) {
	q := url.Values{}
	q.Set("username", username)
	var d userDTO
	if err := j.c.GetJSON(ctx, "/rest/api/2/user?"+q.Encode(), &d); err != nil {
		return nil, err
	}
	u := mapUser(d)
	return &u, nil
}

// AddComment posts a wiki-markup comment.
func (j *Jira) AddComment(ctx context.Context, key string, body []byte) (*domain.Comment, error) {
	var out struct {
		ID      string `json:"id"`
		Created string `json:"created"`
		Author  struct {
			DisplayName string `json:"displayName"`
		} `json:"author"`
	}
	if err := j.c.SendJSON(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/comment",
		map[string]string{"body": string(body)}, &out); err != nil {
		return nil, err
	}
	return &domain.Comment{ID: out.ID, Author: out.Author.DisplayName, Created: out.Created, Body: string(body)}, nil
}

// commentPageGuard bounds the internal comment paging loop so a backend that
// keeps signaling more pages cannot spin forever (mirrors the Confluence
// adapter's maxPages guard).
const commentPageGuard = 100

// ListComments returns an issue's comments via the dedicated comment endpoint so
// the caller need not refetch the whole issue body. The port has no cursor, so
// the adapter pages internally until the listing is exhausted.
func (j *Jira) ListComments(ctx context.Context, key string) ([]domain.Comment, error) {
	startAt := 0
	expectedTotal := -1
	out := []domain.Comment{}
	for page := 0; page < commentPageGuard; page++ {
		var resp struct {
			StartAt  int `json:"startAt"`
			Total    int `json:"total"`
			Comments []struct {
				ID      string         `json:"id"`
				Author  map[string]any `json:"author"`
				Created string         `json:"created"`
				Body    string         `json:"body"`
			} `json:"comments"`
		}
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", "100")
		if err := j.c.GetJSON(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"/comment?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		if resp.StartAt != startAt {
			return nil, fmt.Errorf("%w: Jira comment listing for %s returned offset %d while %d was requested",
				domain.ErrCheckFailed, key, resp.StartAt, startAt)
		}
		if expectedTotal < 0 {
			expectedTotal = resp.Total
		} else if resp.Total != expectedTotal {
			return nil, fmt.Errorf("%w: Jira comment listing for %s changed total from %d to %d while paging",
				domain.ErrCheckFailed, key, expectedTotal, resp.Total)
		}
		for _, c := range resp.Comments {
			out = append(out, domain.Comment{ID: c.ID, Author: nestedDisplay(c.Author), Created: c.Created, Body: c.Body})
		}
		next := resp.StartAt + len(resp.Comments)
		if next > resp.Total {
			return nil, fmt.Errorf("%w: Jira comment listing for %s returned inconsistent pagination (%d comments through offset %d, total %d)",
				domain.ErrCheckFailed, key, len(resp.Comments), next, resp.Total)
		}
		if len(resp.Comments) == 0 && next < resp.Total {
			return nil, fmt.Errorf("%w: Jira comment listing for %s made no progress at offset %d with %d comments remaining",
				domain.ErrCheckFailed, key, next, resp.Total-next)
		}
		startAt = next
		if startAt >= resp.Total {
			return out, nil
		}
		if page == commentPageGuard-1 {
			return nil, fmt.Errorf("%w: Jira comment listing for %s remains incomplete after %d pages (%d of %d comments fetched)",
				domain.ErrCheckFailed, key, commentPageGuard, startAt, resp.Total)
		}
	}
	panic("unreachable")
}

// DeleteComment removes a comment by id.
func (j *Jira) DeleteComment(ctx context.Context, key, commentID string) error {
	return j.c.SendJSON(ctx, "DELETE",
		"/rest/api/2/issue/"+url.PathEscape(key)+"/comment/"+url.PathEscape(commentID), nil, nil)
}

// Link creates a typed link from→to.
func (j *Jira) Link(ctx context.Context, from, to, linkType string) error {
	payload := map[string]any{
		"type":         map[string]string{"name": linkType},
		"inwardIssue":  map[string]string{"key": to},
		"outwardIssue": map[string]string{"key": from},
	}
	return j.c.SendJSON(ctx, "POST", "/rest/api/2/issueLink", payload, nil)
}

// DeleteLink removes an issue link by its backend id.
func (j *Jira) DeleteLink(ctx context.Context, linkID string) error {
	return j.c.SendJSON(ctx, "DELETE", "/rest/api/2/issueLink/"+url.PathEscape(linkID), nil, nil)
}

type jiraChangelogItem struct {
	Field      string `json:"field"`
	FieldID    string `json:"fieldId"`
	FromString string `json:"fromString"`
	ToString   string `json:"toString"`
}

type jiraChangelogHistory struct {
	ID      string              `json:"id"`
	Author  map[string]any      `json:"author"`
	Created string              `json:"created"`
	Items   []jiraChangelogItem `json:"items"`
}

type jiraChangelogPage struct {
	StartAt    int                    `json:"startAt"`
	MaxResults int                    `json:"maxResults"`
	Total      *int                   `json:"total"`
	Values     []jiraChangelogHistory `json:"values"`
	Histories  []jiraChangelogHistory `json:"histories"`
}

// Changelog retains the original Tracker contract for callers that only need
// entries. New analysis workflows use CompleteChangelog for provenance.
func (j *Jira) Changelog(ctx context.Context, key string) ([]domain.ChangelogEntry, error) {
	snapshot, err := j.CompleteChangelog(ctx, key)
	if err != nil {
		return nil, err
	}
	return snapshot.Entries, nil
}

// CompleteChangelog prefers the paginated Data Center endpoint. Older servers
// fall back to the issue expansion, which is labeled incomplete unless its
// paging metadata proves every advertised entry is present.
func (j *Jira) CompleteChangelog(ctx context.Context, key string) (*domain.ChangelogSnapshot, error) {
	const pageSize = 100
	startAt := 0
	advertisedTotal := -1
	entries := []domain.ChangelogEntry{}
	for pageNumber := 0; pageNumber < 1000; pageNumber++ {
		var page jiraChangelogPage
		path := fmt.Sprintf("/rest/api/2/issue/%s/changelog?startAt=%d&maxResults=%d", url.PathEscape(key), startAt, pageSize)
		if err := j.c.GetJSON(ctx, path, &page); err != nil {
			if startAt == 0 && unsupportedChangelogEndpoint(err) {
				return j.embeddedChangelog(ctx, key)
			}
			return nil, err
		}
		raw := page.Values
		if len(raw) == 0 {
			raw = page.Histories
		}
		if startAt == 0 && page.Total == nil && len(raw) == 0 {
			return j.embeddedChangelog(ctx, key)
		}
		if page.Total != nil {
			advertisedTotal = *page.Total
		}
		if page.StartAt != startAt {
			total := len(entries)
			if page.Total != nil {
				total = *page.Total
			}
			return &domain.ChangelogSnapshot{Entries: entries, Total: total, Source: "paginated", PartialReason: "Jira changelog pagination returned a non-contiguous page"}, nil
		}
		entries = append(entries, mapChangelogHistories(raw)...)
		next := page.StartAt + len(raw)
		if page.Total != nil && next == *page.Total {
			return &domain.ChangelogSnapshot{Entries: entries, Total: *page.Total, Complete: true, Source: "paginated"}, nil
		}
		if page.Total != nil && next > *page.Total {
			return &domain.ChangelogSnapshot{Entries: entries, Total: *page.Total, Source: "paginated", PartialReason: "Jira changelog returned more entries than advertised"}, nil
		}
		if len(raw) == 0 || next <= startAt {
			total := len(entries)
			if page.Total != nil {
				total = *page.Total
			}
			return &domain.ChangelogSnapshot{Entries: entries, Total: total, Source: "paginated", PartialReason: "Jira changelog pagination made no forward progress"}, nil
		}
		startAt = next
	}
	total := len(entries)
	if advertisedTotal >= 0 {
		total = advertisedTotal
	}
	return &domain.ChangelogSnapshot{Entries: entries, Total: total, Source: "paginated", PartialReason: "Jira changelog exceeded the 1000-page safety cap"}, nil
}

func unsupportedChangelogEndpoint(err error) bool {
	if errors.Is(err, domain.ErrNotFound) {
		return true
	}
	var apiErr *httpx.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 405
}

func (j *Jira) embeddedChangelog(ctx context.Context, key string) (*domain.ChangelogSnapshot, error) {
	var d struct {
		Changelog jiraChangelogPage `json:"changelog"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"?expand=changelog&fields=summary", &d); err != nil {
		return nil, err
	}
	entries := mapChangelogHistories(d.Changelog.Histories)
	total := len(entries)
	complete := false
	if d.Changelog.Total != nil {
		total = *d.Changelog.Total
		complete = d.Changelog.StartAt == 0 && len(entries) == total
	}
	reason := "embedded changelog did not report paging metadata"
	if d.Changelog.Total != nil && total > len(entries) {
		reason = "embedded changelog contains only part of the advertised history"
	}
	if complete {
		reason = ""
	}
	return &domain.ChangelogSnapshot{Entries: entries, Total: total, Complete: complete, Source: "embedded", PartialReason: reason}, nil
}

func mapChangelogHistories(histories []jiraChangelogHistory) []domain.ChangelogEntry {
	out := make([]domain.ChangelogEntry, 0, len(histories))
	for _, h := range histories {
		e := domain.ChangelogEntry{ID: h.ID, Author: nestedDisplay(h.Author), Created: h.Created}
		for _, it := range h.Items {
			e.Items = append(e.Items, domain.ChangelogItem{Field: it.Field, FieldID: it.FieldID, From: it.FromString, To: it.ToString})
		}
		out = append(out, e)
	}
	return out
}

// LinkEpic sets the Epic Link field (DC classic) on an issue.
func (j *Jira) LinkEpic(ctx context.Context, issue, epic string) error {
	fields, err := j.Fields(ctx)
	if err != nil {
		return err
	}
	var epicField string
	for _, f := range fields {
		if strings.EqualFold(f.Name, "Epic Link") {
			epicField = f.ID
			break
		}
	}
	if epicField == "" {
		return fmt.Errorf("%w: no 'Epic Link' field on this Jira (team-managed projects use the parent field)", domain.ErrUsage)
	}
	return j.c.SendJSON(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(issue),
		map[string]any{"fields": map[string]any{epicField: epic}}, nil)
}

// Whoami confirms the PAT by fetching the current user and returns their
// display name. Used by `atl auth login` to validate credentials before they
// are persisted.
func (j *Jira) Whoami(ctx context.Context) (string, error) {
	var u struct {
		DisplayName string `json:"displayName"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/myself", &u); err != nil {
		return "", err
	}
	return u.DisplayName, nil
}

// --- small helpers for untyped field access ---

// coerceField decodes an extra --field value. Only a structured value — a JSON
// object or array — is decoded (that is the case needing a non-string type, e.g.
// priority={"name":"High"} or labels=["a","b"]). A bare scalar is kept verbatim
// as a string, so a text/label/version field whose value merely looks like JSON
// (123, true, null) is NOT silently retyped into a number/bool/null and rejected
// or mis-stored by Jira.
func coerceField(v string) any {
	if t := strings.TrimSpace(v); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var decoded any
		if err := json.Unmarshal([]byte(v), &decoded); err == nil {
			return decoded
		}
	}
	return v
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func nestedName(v any) string {
	if m, ok := v.(map[string]any); ok {
		return str(m["name"])
	}
	return ""
}

func nestedKey(v any) string {
	if m, ok := v.(map[string]any); ok {
		return str(m["key"])
	}
	return ""
}

func nestedDisplay(v any) string {
	if m, ok := v.(map[string]any); ok {
		if d := str(m["displayName"]); d != "" {
			return d
		}
		return str(m["name"])
	}
	return ""
}

func typeField(v any, field string) any {
	if m, ok := v.(map[string]any); ok {
		if s := str(m[field]); s != "" {
			return s
		}
		return str(m["name"])
	}
	return ""
}
