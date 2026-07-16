package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraDigestCompactNarrativeBytes   = 3 << 10
	jiraDigestCompactDescriptionBytes = 1536
	jiraDigestCompactBodyBytes        = 768
	jiraDigestCompactValueBytes       = 128
	jiraDigestCompactURLBytes         = 384
	jiraDigestCompactComments         = 3
	jiraDigestCompactHistory          = 5
	jiraDigestCompactHistoryItems     = 3
	jiraDigestCompactLinks            = 10
	jiraDigestCompactRefs             = 10
	jiraDigestCompactConfluence       = 2
	jiraDigestCompactStatuses         = 32
)

type JiraDigestProjection struct {
	Name    string   `json:"name"`
	Omitted []string `json:"omitted"`
	Clipped []string `json:"clipped"`
}

type JiraDigestCompactComment struct {
	ID      string `json:"id"`
	Created string `json:"created"`
	Body    string `json:"body,omitempty"`
}

type JiraDigestCommentSummary struct {
	Count  int                        `json:"count"`
	Recent []JiraDigestCompactComment `json:"recent"`
}

type JiraDigestLinkSummary struct {
	Count    int                `json:"count"`
	ByType   map[string]int     `json:"by_type"`
	Blockers []domain.IssueLink `json:"blockers"`
}

type JiraDigestCompactHistoryEntry struct {
	ID      string                 `json:"id"`
	Created string                 `json:"created"`
	Items   []domain.ChangelogItem `json:"items"`
}

type JiraDigestHistorySummary struct {
	Count  int                             `json:"count"`
	Recent []JiraDigestCompactHistoryEntry `json:"recent"`
}

type JiraDigestRefSummary struct {
	Count  int            `json:"count"`
	ByKind map[string]int `json:"by_kind"`
	Items  []PlanningRef  `json:"items"`
}

// ProjectJiraEpicDigest applies a deterministic presentation projection after
// all evidence and completeness decisions have been made. The full projection
// returns the original contract unchanged; compact never upgrades source
// completeness and records every omitted or clipped path.
func ProjectJiraEpicDigest(in *JiraEpicDigestResult, projection string) (*JiraEpicDigestResult, error) {
	projection = strings.ToLower(strings.TrimSpace(projection))
	if projection == "" || projection == "full" {
		return in, nil
	}
	if projection != "compact" {
		return nil, fmt.Errorf("%w: --projection must be full or compact", domain.ErrUsage)
	}
	if in == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out JiraEpicDigestResult
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	p := &JiraDigestProjection{Name: "compact", Omitted: []string{}, Clipped: []string{}}
	out.Projection = p
	out.Epic.Key = compactDigestString(out.Epic.Key, jiraDigestCompactValueBytes, "epic.key", p)
	out.Epic.Summary = compactDigestString(out.Epic.Summary, jiraDigestCompactValueBytes, "epic.summary", p)
	out.Epic.Status = compactDigestString(out.Epic.Status, jiraDigestCompactValueBytes, "epic.status", p)
	out.Epic.Resolution = compactDigestString(out.Epic.Resolution, jiraDigestCompactValueBytes, "epic.resolution", p)
	out.Epic.Type = compactDigestString(out.Epic.Type, jiraDigestCompactValueBytes, "epic.type", p)
	out.Epic.Updated = compactDigestString(out.Epic.Updated, jiraDigestCompactValueBytes, "epic.updated", p)
	out.Epic.Description = compactDigestString(out.Epic.Description, jiraDigestCompactDescriptionBytes, "epic.description", p)
	compactDigestField(out.StatusField, "status_field", p)
	compactDigestField(out.DoDField, "dod_field", p)
	compactDigestChildren(out.Children, p)
	compactDigestComments(&out, p)
	compactDigestLinks(&out, p)
	compactDigestHistory(&out, p)
	compactDigestRefs(&out, p)
	compactDigestConfluence(&out, p)
	p.Omitted = sortedUniqueStrings(p.Omitted)
	p.Clipped = sortedUniqueStrings(p.Clipped)
	return &out, nil
}

func compactDigestField(field *JiraDigestFieldEvidence, path string, p *JiraDigestProjection) {
	if field == nil {
		return
	}
	field.ID = compactDigestString(field.ID, jiraDigestCompactValueBytes, path+".id", p)
	field.Name = compactDigestString(field.Name, jiraDigestCompactValueBytes, path+".name", p)
	field.Value = compactDigestString(field.Value, jiraDigestCompactNarrativeBytes, path+".value", p)
	if field.LastChange != nil {
		field.LastChange.FieldID = compactDigestString(field.LastChange.FieldID, jiraDigestCompactValueBytes, path+".last_change.field_id", p)
		field.LastChange.Field = compactDigestString(field.LastChange.Field, jiraDigestCompactValueBytes, path+".last_change.field", p)
		field.LastChange.Created = compactDigestString(field.LastChange.Created, jiraDigestCompactValueBytes, path+".last_change.created", p)
		field.LastChange.HistoryID = compactDigestString(field.LastChange.HistoryID, jiraDigestCompactValueBytes, path+".last_change.history_id", p)
		field.LastChange.From = compactDigestString(field.LastChange.From, jiraDigestCompactValueBytes, path+".last_change.from", p)
		field.LastChange.To = compactDigestString(field.LastChange.To, jiraDigestCompactValueBytes, path+".last_change.to", p)
	}
}

func compactDigestChildren(children *JiraDigestChildren, p *JiraDigestProjection) {
	if children == nil {
		return
	}
	if children.List != nil {
		children.List = nil
		p.Omitted = append(p.Omitted, "children.list")
	}
	keys := make([]string, 0, len(children.ByStatus))
	for key := range children.ByStatus {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bounded := make(map[string]int, min(len(keys), jiraDigestCompactStatuses))
	for i, key := range keys {
		if i >= jiraDigestCompactStatuses {
			p.Omitted = append(p.Omitted, "children.by_status[remaining]")
			break
		}
		bounded[compactDigestString(key, jiraDigestCompactValueBytes, "children.by_status.name", p)] += children.ByStatus[key]
	}
	children.ByStatus = bounded
	children.LatestUpdated = compactDigestString(children.LatestUpdated, jiraDigestCompactValueBytes, "children.latest_updated", p)
}

func compactDigestComments(out *JiraEpicDigestResult, p *JiraDigestProjection) {
	if _, ok := out.Sources["comments"]; !ok {
		return
	}
	summary := &JiraDigestCommentSummary{Count: out.Sources["comments"].Count, Recent: []JiraDigestCompactComment{}}
	start := max(0, len(out.Comments)-jiraDigestCompactComments)
	for i := start; i < len(out.Comments); i++ {
		comment := out.Comments[i]
		summary.Recent = append(summary.Recent, JiraDigestCompactComment{
			ID:      compactDigestString(comment.ID, jiraDigestCompactValueBytes, "comment_summary.recent.id", p),
			Created: compactDigestString(comment.Created, jiraDigestCompactValueBytes, "comment_summary.recent.created", p),
			Body:    compactDigestString(comment.Body, jiraDigestCompactBodyBytes, "comment_summary.recent.body", p),
		})
	}
	if len(out.Comments) > 0 {
		p.Omitted = append(p.Omitted, "comments")
	}
	out.Comments = nil
	out.CommentSummary = summary
}

func compactDigestLinks(out *JiraEpicDigestResult, p *JiraDigestProjection) {
	if _, ok := out.Sources["links"]; !ok {
		return
	}
	summary := &JiraDigestLinkSummary{Count: out.Sources["links"].Count, ByType: map[string]int{}, Blockers: []domain.IssueLink{}}
	allTypes := map[string]int{}
	for _, link := range out.Links {
		name := link.TypeName
		if name == "" {
			name = link.Type
		}
		allTypes[name]++
	}
	typeNames := make([]string, 0, len(allTypes))
	for name := range allTypes {
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)
	for i, name := range typeNames {
		if i >= jiraDigestCompactStatuses {
			p.Omitted = append(p.Omitted, "link_summary.by_type[remaining]")
			break
		}
		summary.ByType[compactDigestString(name, jiraDigestCompactValueBytes, "link_summary.by_type", p)] += allTypes[name]
	}
	for i, link := range out.Blockers {
		if i >= jiraDigestCompactLinks {
			p.Omitted = append(p.Omitted, "link_summary.blockers[remaining]")
			break
		}
		summary.Blockers = append(summary.Blockers, compactDigestLink(link, "link_summary.blockers", p))
	}
	if len(out.Links) > 0 {
		p.Omitted = append(p.Omitted, "links")
	}
	if len(out.Blockers) > 0 {
		p.Omitted = append(p.Omitted, "blockers")
	}
	out.Links, out.Blockers, out.LinkSummary = nil, nil, summary
}

func compactDigestLink(link domain.IssueLink, path string, p *JiraDigestProjection) domain.IssueLink {
	link.ID = compactDigestString(link.ID, jiraDigestCompactValueBytes, path+".id", p)
	link.Type = compactDigestString(link.Type, jiraDigestCompactValueBytes, path+".type", p)
	link.TypeName = compactDigestString(link.TypeName, jiraDigestCompactValueBytes, path+".type_name", p)
	link.Direction = compactDigestString(link.Direction, jiraDigestCompactValueBytes, path+".direction", p)
	link.Key = compactDigestString(link.Key, jiraDigestCompactValueBytes, path+".key", p)
	return link
}

func compactDigestHistory(out *JiraEpicDigestResult, p *JiraDigestProjection) {
	if _, ok := out.Sources["history"]; !ok {
		return
	}
	summary := &JiraDigestHistorySummary{Count: out.Sources["history"].Count, Recent: []JiraDigestCompactHistoryEntry{}}
	start := max(0, len(out.History)-jiraDigestCompactHistory)
	for i := start; i < len(out.History); i++ {
		entry := out.History[i]
		compact := JiraDigestCompactHistoryEntry{ID: compactDigestString(entry.ID, jiraDigestCompactValueBytes, "history_summary.recent.id", p), Created: compactDigestString(entry.Created, jiraDigestCompactValueBytes, "history_summary.recent.created", p), Items: []domain.ChangelogItem{}}
		for j, item := range entry.Items {
			if j >= jiraDigestCompactHistoryItems {
				p.Omitted = append(p.Omitted, "history_summary.recent.items[remaining]")
				break
			}
			item.Field = compactDigestString(item.Field, jiraDigestCompactValueBytes, "history_summary.recent.items.field", p)
			item.FieldID = compactDigestString(item.FieldID, jiraDigestCompactValueBytes, "history_summary.recent.items.field_id", p)
			item.From = compactDigestString(item.From, jiraDigestCompactValueBytes, "history_summary.recent.items.from", p)
			item.To = compactDigestString(item.To, jiraDigestCompactValueBytes, "history_summary.recent.items.to", p)
			compact.Items = append(compact.Items, item)
		}
		summary.Recent = append(summary.Recent, compact)
	}
	if len(out.History) > 0 {
		p.Omitted = append(p.Omitted, "history")
	}
	out.History, out.HistorySummary = nil, summary
}

func compactDigestRefs(out *JiraEpicDigestResult, p *JiraDigestProjection) {
	if _, ok := out.Sources["refs"]; !ok {
		return
	}
	summary := &JiraDigestRefSummary{Count: out.Sources["refs"].Count, ByKind: map[string]int{}, Items: []PlanningRef{}}
	for i, ref := range out.Refs {
		summary.ByKind[compactDigestString(ref.Kind, jiraDigestCompactValueBytes, "ref_summary.by_kind", p)]++
		if i >= jiraDigestCompactRefs {
			continue
		}
		ref.Kind = compactDigestString(ref.Kind, jiraDigestCompactValueBytes, "ref_summary.items.kind", p)
		ref.URL = compactDigestString(ref.URL, jiraDigestCompactURLBytes, "ref_summary.items.url", p)
		summary.Items = append(summary.Items, ref)
	}
	if len(out.Refs) > jiraDigestCompactRefs {
		p.Omitted = append(p.Omitted, "ref_summary.items[remaining]")
	}
	if len(out.Refs) > 0 {
		p.Omitted = append(p.Omitted, "refs")
	}
	out.Refs, out.RefSummary = nil, summary
}

func compactDigestConfluence(out *JiraEpicDigestResult, p *JiraDigestProjection) {
	if len(out.Confluence) == 0 {
		return
	}
	if len(out.Confluence) > jiraDigestCompactConfluence {
		out.Confluence = out.Confluence[:jiraDigestCompactConfluence]
		p.Omitted = append(p.Omitted, "confluence[remaining]")
	}
	for i := range out.Confluence {
		out.Confluence[i].URL = compactDigestString(out.Confluence[i].URL, jiraDigestCompactURLBytes, "confluence.url", p)
		section := out.Confluence[i].Section
		if section == nil {
			continue
		}
		section.ID = compactDigestString(section.ID, jiraDigestCompactValueBytes, "confluence.section.id", p)
		section.PageTitle = compactDigestString(section.PageTitle, jiraDigestCompactValueBytes, "confluence.section.page_title", p)
		section.Space = compactDigestString(section.Space, jiraDigestCompactValueBytes, "confluence.section.space", p)
		section.Heading = compactDigestString(section.Heading, jiraDigestCompactValueBytes, "confluence.section.heading", p)
		section.Markdown = compactDigestString(section.Markdown, jiraDigestCompactDescriptionBytes, "confluence.section.markdown", p)
		if len(section.Path) > 10 {
			section.Path = section.Path[:10]
			p.Omitted = append(p.Omitted, "confluence.section.path[remaining]")
		}
		for j := range section.Path {
			section.Path[j] = compactDigestString(section.Path[j], jiraDigestCompactValueBytes, "confluence.section.path", p)
		}
	}
}

func compactDigestString(value string, limit int, path string, p *JiraDigestProjection) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	p.Clipped = append(p.Clipped, path)
	return value[:end]
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
