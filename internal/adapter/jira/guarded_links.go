package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraGuardedLinkTypeMaxItems = 1024
	jiraGuardedLinkMaxItems     = 4096
	jiraGuardedLinkStringBytes  = 1024
	jiraGuardedLinkRefBytes     = 64
)

var _ domain.JiraGuardedLinkPort = (*Jira)(nil)

type guardedLinkNoAttemptError struct{ cause error }

func (e *guardedLinkNoAttemptError) Error() string {
	return "guarded Jira link write was denied before dispatch"
}
func (e *guardedLinkNoAttemptError) Unwrap() error                  { return e.cause }
func (e *guardedLinkNoAttemptError) DiagnosticWriteAttempted() bool { return false }

type strictLinkTypeDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

func (j *Jira) ReadStrictLinkTypes(ctx context.Context) (domain.JiraStrictLinkCatalog, error) {
	data, err := j.c.Do(ctx, http.MethodGet, "/rest/api/2/issueLinkType", nil, nil)
	if err != nil {
		return domain.JiraStrictLinkCatalog{}, err
	}
	var envelope struct {
		Types json.RawMessage `json:"issueLinkTypes"`
	}
	if err := decodeOneJSON(data, &envelope); err != nil || !jsonArrayPresent(envelope.Types) {
		return domain.JiraStrictLinkCatalog{}, guardedLinkDecodeError("link-type catalog")
	}
	var rows []strictLinkTypeDTO
	if err := json.Unmarshal(envelope.Types, &rows); err != nil || len(rows) > jiraGuardedLinkTypeMaxItems {
		return domain.JiraStrictLinkCatalog{}, guardedLinkDecodeError("link-type catalog")
	}
	out := make([]domain.JiraLinkTypeMetadata, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if !guardedLinkID(row.ID) || !guardedLinkText(row.Name) || !guardedLinkText(row.Inward) || !guardedLinkText(row.Outward) {
			return domain.JiraStrictLinkCatalog{}, guardedLinkDecodeError("link-type catalog")
		}
		if _, duplicate := seen[row.ID]; duplicate {
			return domain.JiraStrictLinkCatalog{}, guardedLinkDecodeError("link-type catalog")
		}
		seen[row.ID] = struct{}{}
		out = append(out, domain.JiraLinkTypeMetadata{ID: row.ID, Name: row.Name, Inward: row.Inward, Outward: row.Outward})
	}
	sort.Slice(out, func(i, k int) bool { return guardedNumericLess(out[i].ID, out[k].ID) })
	return domain.JiraStrictLinkCatalog{Types: out, Complete: true}, nil
}

type strictEndpointDTO struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Project json.RawMessage `json:"project"`
		Links   json.RawMessage `json:"issuelinks"`
		Updated json.RawMessage `json:"updated"`
	} `json:"fields"`
}

type strictLinkRowDTO struct {
	ID             string            `json:"id"`
	Type           strictLinkTypeDTO `json:"type"`
	Inward         json.RawMessage   `json:"inwardIssue,omitempty"`
	Outward        json.RawMessage   `json:"outwardIssue,omitempty"`
	InwardPresent  bool              `json:"-"`
	OutwardPresent bool              `json:"-"`
}

func (row *strictLinkRowDTO) UnmarshalJSON(data []byte) error {
	type wire struct {
		ID      string            `json:"id"`
		Type    strictLinkTypeDTO `json:"type"`
		Inward  json.RawMessage   `json:"inwardIssue"`
		Outward json.RawMessage   `json:"outwardIssue"`
	}
	var decoded wire
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	_, inwardPresent := members["inwardIssue"]
	_, outwardPresent := members["outwardIssue"]
	*row = strictLinkRowDTO{ID: decoded.ID, Type: decoded.Type, Inward: decoded.Inward, Outward: decoded.Outward, InwardPresent: inwardPresent, OutwardPresent: outwardPresent}
	return nil
}

type strictLinkPeerDTO struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

func (j *Jira) ReadStrictLinkEndpoint(ctx context.Context, reference string) (domain.JiraStrictLinkEndpoint, error) {
	if len(reference) == 0 || len(reference) > jiraGuardedLinkRefBytes || (!domain.ValidJiraIssueKey(reference) && !guardedLinkID(reference)) {
		return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint reference")
	}
	data, err := j.c.Do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(reference)+"?fields=project,issuelinks,updated", nil, nil)
	if err != nil {
		return domain.JiraStrictLinkEndpoint{}, err
	}
	var response strictEndpointDTO
	if err := decodeOneJSON(data, &response); err != nil || !guardedLinkID(response.ID) || !guardedLinkKey(response.Key) || !jsonArrayPresent(response.Fields.Links) {
		return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint snapshot")
	}
	var project struct {
		Key string `json:"key"`
	}
	if len(response.Fields.Project) == 0 || bytes.Equal(bytes.TrimSpace(response.Fields.Project), []byte("null")) || json.Unmarshal(response.Fields.Project, &project) != nil || !guardedLinkProject(project.Key) || !strings.HasPrefix(response.Key, project.Key+"-") {
		return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint snapshot")
	}
	var rows []strictLinkRowDTO
	if json.Unmarshal(response.Fields.Links, &rows) != nil || len(rows) > jiraGuardedLinkMaxItems {
		return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint inventory")
	}
	links := make([]domain.JiraStrictIssueLink, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if !guardedLinkID(row.ID) || !guardedLinkTypeDTO(row.Type) {
			return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint inventory")
		}
		if _, duplicate := seen[row.ID]; duplicate {
			return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint inventory")
		}
		seen[row.ID] = struct{}{}
		if row.InwardPresent == row.OutwardPresent {
			return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint inventory")
		}
		peerRaw, role := row.Inward, "outward"
		if row.OutwardPresent {
			peerRaw, role = row.Outward, "inward"
		}
		var peer strictLinkPeerDTO
		if !jsonObjectPresent(peerRaw) || json.Unmarshal(peerRaw, &peer) != nil || !guardedLinkID(peer.ID) || !guardedLinkKey(peer.Key) {
			return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint inventory")
		}
		links = append(links, domain.JiraStrictIssueLink{
			ID: row.ID, Type: domain.JiraLinkTypeMetadata{ID: row.Type.ID, Name: row.Type.Name, Inward: row.Type.Inward, Outward: row.Type.Outward},
			Role: role, OtherID: peer.ID, OtherKey: peer.Key,
		})
	}
	sort.Slice(links, func(i, k int) bool {
		if links[i].ID != links[k].ID {
			return guardedNumericLess(links[i].ID, links[k].ID)
		}
		return links[i].Role < links[k].Role
	})
	updatedPresent := len(response.Fields.Updated) != 0
	updated := ""
	if !updatedPresent || json.Unmarshal(response.Fields.Updated, &updated) != nil || !domain.ValidJiraGuardedCommentInstant(updated) {
		return domain.JiraStrictLinkEndpoint{}, guardedLinkDecodeError("endpoint snapshot")
	}
	return domain.JiraStrictLinkEndpoint{ID: response.ID, Key: response.Key, Project: project.Key, Links: links, Complete: true, Updated: updated, UpdatedPresent: updatedPresent}, nil
}

func (j *Jira) AddGuardedLink(ctx context.Context, write domain.JiraGuardedLinkWrite) error {
	cleared, err := j.authorizeGuardedLink(ctx, domain.WriteVerbUpdate, write)
	if err != nil {
		return &guardedLinkNoAttemptError{cause: err}
	}
	payload := struct {
		Type struct {
			ID string `json:"id"`
		} `json:"type"`
		Inward struct {
			ID string `json:"id"`
		} `json:"inwardIssue"`
		Outward struct {
			ID string `json:"id"`
		} `json:"outwardIssue"`
	}{}
	payload.Type.ID, payload.Inward.ID, payload.Outward.ID = write.TypeID, write.Inward.ID, write.Outward.ID
	return j.c.SendJSON(domain.WithWriteClearance(cleared), http.MethodPost, "/rest/api/2/issueLink", payload, nil)
}

func (j *Jira) DeleteGuardedLink(ctx context.Context, write domain.JiraGuardedLinkWrite) error {
	cleared, err := j.authorizeGuardedLink(ctx, domain.WriteVerbDelete, write)
	if err != nil {
		return &guardedLinkNoAttemptError{cause: err}
	}
	if !guardedLinkID(write.LinkID) {
		return &guardedLinkNoAttemptError{cause: domain.ErrCheckFailed}
	}
	return j.c.SendJSON(domain.WithWriteClearance(cleared), http.MethodDelete, "/rest/api/2/issueLink/"+url.PathEscape(write.LinkID), nil, nil)
}

func (j *Jira) authorizeGuardedLink(ctx context.Context, verb domain.WriteVerb, write domain.JiraGuardedLinkWrite) (context.Context, error) {
	if !guardedLinkID(write.TypeID) || !guardedLinkEndpointIdentity(write.Outward) || !guardedLinkEndpointIdentity(write.Inward) || write.Outward.ID == write.Inward.ID {
		return ctx, domain.ErrCheckFailed
	}
	if j.authorizer == nil {
		return ctx, nil
	}
	targets := []domain.WriteTarget{
		{Service: "jira", Kind: "link", Key: write.Outward.Key, Project: write.Outward.Project},
		{Service: "jira", Kind: "link", Key: write.Inward.Key, Project: write.Inward.Project},
	}
	return j.authorizer.Authorize(ctx, domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{verb}, Targets: targets})
}

func decodeOneJSON(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("trailing value")
		}
		return err
	}
	return nil
}

func guardedLinkDecodeError(owner string) error {
	return fmt.Errorf("%w: Jira returned a malformed or unbounded %s", domain.ErrCheckFailed, owner)
}
func jsonArrayPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}
func jsonObjectPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}
func guardedLinkID(value string) bool {
	return len(value) <= jiraGuardedLinkRefBytes && domain.ValidConfluenceContentID(value)
}
func guardedLinkKey(value string) bool {
	return len(value) <= jiraGuardedLinkRefBytes && domain.ValidJiraIssueKey(value)
}
func guardedLinkProject(value string) bool {
	return len(value) <= jiraGuardedLinkRefBytes && domain.ValidJiraIssueKey(value+"-1")
}
func guardedLinkText(value string) bool {
	return value != "" && len(value) <= jiraGuardedLinkStringBytes && utf8.ValidString(value)
}
func guardedLinkTypeDTO(value strictLinkTypeDTO) bool {
	return guardedLinkID(value.ID) && guardedLinkText(value.Name) && guardedLinkText(value.Inward) && guardedLinkText(value.Outward)
}
func guardedLinkEndpointIdentity(value domain.JiraStrictLinkEndpoint) bool {
	return guardedLinkID(value.ID) && guardedLinkKey(value.Key) && guardedLinkProject(value.Project) && strings.HasPrefix(value.Key, value.Project+"-")
}
func guardedNumericLess(left, right string) bool {
	a, _ := strconv.ParseUint(left, 10, 64)
	b, _ := strconv.ParseUint(right, 10, 64)
	return a < b
}
