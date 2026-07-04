package app

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// JiraLinkSuggestOpts controls read-only link suggestion from a CSV plan.
type JiraLinkSuggestOpts struct {
	CSVPath string
}

// JiraLinkSuggestResult is the deterministic dry-run output.
type JiraLinkSuggestResult struct {
	Path         string              `json:"path,omitempty"`
	PlannedCount int                 `json:"planned_count"`
	Count        int                 `json:"count"`
	Candidates   []JiraLinkCandidate `json:"candidates"`
}

// JiraLinkCandidate is one missing link from the reviewed CSV plan.
type JiraLinkCandidate struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	Type      string `json:"type"`
	Rationale string `json:"rationale"`
	Row       int    `json:"row"`
}

// SuggestLinks reads a CSV plan and returns missing Jira link candidates only.
func (s *JiraService) SuggestLinks(ctx context.Context, opts JiraLinkSuggestOpts) (*JiraLinkSuggestResult, error) {
	path := strings.TrimSpace(opts.CSVPath)
	if path == "" {
		return nil, fmt.Errorf("%w: --csv is required", domain.ErrUsage)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	planned, err := parseLinkPlanCSV(data)
	if err != nil {
		return nil, err
	}
	bySource := map[string][]JiraLinkCandidate{}
	for _, candidate := range planned {
		bySource[candidate.Source] = append(bySource[candidate.Source], candidate)
	}
	var out []JiraLinkCandidate
	for _, source := range sortedMapKeys(bySource) {
		links, err := s.Links(ctx, source)
		if err != nil {
			return nil, err
		}
		existing := existingOutwardLinks(links)
		for _, candidate := range bySource[source] {
			if existing[linkIdentity(candidate.Target, candidate.Type)] {
				continue
			}
			out = append(out, candidate)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Row < out[j].Row
	})
	return &JiraLinkSuggestResult{Path: path, PlannedCount: len(planned), Count: len(out), Candidates: out}, nil
}

func parseLinkPlanCSV(data []byte) ([]JiraLinkCandidate, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: CSV is empty", domain.ErrUsage)
	}
	cols := linkPlanColumns(records[0])
	for _, required := range []string{"source", "target", "type"} {
		if cols[required] < 0 {
			return nil, fmt.Errorf("%w: CSV must include %s column", domain.ErrUsage, required)
		}
	}
	seen := map[string]bool{}
	var out []JiraLinkCandidate
	for i, record := range records[1:] {
		rowNo := i + 2
		candidate := JiraLinkCandidate{
			Source:    csvCell(record, cols["source"]),
			Target:    csvCell(record, cols["target"]),
			Type:      csvCell(record, cols["type"]),
			Rationale: csvCell(record, cols["rationale"]),
			Row:       rowNo,
		}
		if candidate.Source == "" && candidate.Target == "" && candidate.Type == "" {
			continue
		}
		if candidate.Source == "" || candidate.Target == "" || candidate.Type == "" {
			return nil, fmt.Errorf("%w: CSV row %d must include source, target, and type", domain.ErrUsage, rowNo)
		}
		if candidate.Rationale == "" {
			candidate.Rationale = "csv_plan"
		}
		identity := linkIdentity(candidate.Source, candidate.Target, candidate.Type)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		out = append(out, candidate)
	}
	return out, nil
}

func linkPlanColumns(header []string) map[string]int {
	out := map[string]int{"source": -1, "target": -1, "type": -1, "rationale": -1}
	aliases := map[string]string{
		"source":      "source",
		"from":        "source",
		"issue":       "source",
		"key":         "source",
		"target":      "target",
		"to":          "target",
		"linked":      "target",
		"linkedissue": "target",
		"type":        "type",
		"linktype":    "type",
		"relation":    "type",
		"rationale":   "rationale",
		"reason":      "rationale",
		"comment":     "rationale",
	}
	for i, raw := range header {
		if name, ok := aliases[normalizeHeader(raw)]; ok && out[name] < 0 {
			out[name] = i
		}
	}
	return out
}

func normalizeHeader(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer("_", "", "-", "", " ", "")
	return replacer.Replace(s)
}

func csvCell(record []string, idx int) string {
	if idx < 0 || idx >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[idx])
}

func existingOutwardLinks(links []domain.IssueLink) map[string]bool {
	out := map[string]bool{}
	for _, link := range links {
		if link.Direction != "outward" {
			continue
		}
		// Index the canonical type name (what plan rows and Link() use) and the
		// directional phrase (what Type carries for display), so a plan row like
		// `type=Duplicate` matches an existing link whose phrase is "duplicates" —
		// otherwise a re-apply would create a duplicate link.
		if link.TypeName != "" {
			out[linkIdentity(link.Key, link.TypeName)] = true
		}
		out[linkIdentity(link.Key, link.Type)] = true
	}
	return out
}

func linkIdentity(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		clean = append(clean, strings.ToLower(strings.TrimSpace(part)))
	}
	return strings.Join(clean, "\x00")
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
