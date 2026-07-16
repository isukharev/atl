package agenteval

import (
	"fmt"
	"sort"
)

type CapabilityFamilyMetric struct {
	Family      string `json:"family"`
	Invocations int    `json:"invocations"`
	Successes   int    `json:"successes"`
	Failures    int    `json:"failures"`
	OutputBytes int64  `json:"output_bytes"`
}

var mcpCapabilityFamilies = map[string]string{
	"jira_fields": "jira.fields", "jira_issue_search": "jira.issue.search",
	"jira_issue_field_get": "jira.issue.field", "jira_epic_digest": "jira.epic.digest", "jira_board_view": "jira.board.view",
	"confluence_page_resolve": "confluence.page.resolve", "confluence_page_outline": "confluence.page.outline",
	"confluence_page_section": "confluence.page.section",
}

var allowedCapabilityFamilies = map[string]struct{}{
	"atl.config": {}, "atl.capabilities": {}, "jira.fields": {}, "jira.issue.fields": {},
	"jira.issue.field": {}, "jira.issue.search": {}, "jira.epic.digest": {}, "jira.board.view": {},
	"confluence.search": {}, "confluence.page.resolve": {}, "confluence.page.outline": {}, "confluence.page.section": {},
}

func CapabilityFamilyForMCP(tool string) (string, bool) {
	family, ok := mcpCapabilityFamilies[tool]
	return family, ok
}

func CapabilityFamilyForCLI(args []string) (string, bool) {
	for len(args) > 0 {
		switch args[0] {
		case "--read-only", "--verbose":
			args = args[1:]
		case "-o", "--output", "--config-dir", "--jira-url", "--confluence-url", "--mirror-root":
			if len(args) < 2 {
				return "", false
			}
			args = args[2:]
		default:
			goto matched
		}
	}
matched:
	patterns := []struct {
		path   []string
		family string
	}{
		{[]string{"config", "show"}, "atl.config"}, {[]string{"capabilities"}, "atl.capabilities"},
		{[]string{"jira", "issue", "field", "get"}, "jira.issue.field"}, {[]string{"jira", "issue", "fields"}, "jira.issue.fields"}, {[]string{"jira", "epic", "digest"}, "jira.epic.digest"},
		{[]string{"jira", "issue", "search"}, "jira.issue.search"}, {[]string{"jira", "board", "view"}, "jira.board.view"}, {[]string{"jira", "fields"}, "jira.fields"},
		{[]string{"conf", "search"}, "confluence.search"},
		{[]string{"conf", "page", "resolve"}, "confluence.page.resolve"}, {[]string{"conf", "page", "outline"}, "confluence.page.outline"},
		{[]string{"conf", "page", "section"}, "confluence.page.section"},
	}
	for _, pattern := range patterns {
		if len(args) < len(pattern.path) {
			continue
		}
		ok := true
		for i := range pattern.path {
			if args[i] != pattern.path[i] {
				ok = false
				break
			}
		}
		if ok {
			return pattern.family, true
		}
	}
	return "", false
}

func normalizeCapabilityFamilies(values []CapabilityFamilyMetric) ([]CapabilityFamilyMetric, error) {
	if len(values) > 64 {
		return nil, fmt.Errorf("capability families exceed 64 entries")
	}
	byFamily := map[string]CapabilityFamilyMetric{}
	for _, value := range values {
		_, known := allowedCapabilityFamilies[value.Family]
		if !known || value.Invocations < 1 || value.Successes < 0 || value.Failures < 0 || value.OutputBytes < 0 || value.Successes+value.Failures != value.Invocations || value.Invocations > maxObservedMethodCount {
			return nil, fmt.Errorf("invalid capability family metric")
		}
		if _, exists := byFamily[value.Family]; exists {
			return nil, fmt.Errorf("duplicate capability family %q", value.Family)
		}
		byFamily[value.Family] = value
	}
	keys := make([]string, 0, len(byFamily))
	for key := range byFamily {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]CapabilityFamilyMetric, 0, len(keys))
	for _, key := range keys {
		out = append(out, byFamily[key])
	}
	return out, nil
}

func mergeCapabilityFamily(values map[string]CapabilityFamilyMetric, family string, failed bool, outputBytes int64) {
	value := values[family]
	value.Family = family
	value.Invocations++
	value.OutputBytes += outputBytes
	if failed {
		value.Failures++
	} else {
		value.Successes++
	}
	values[family] = value
}

func capabilityFamilySlice(values map[string]CapabilityFamilyMetric) []CapabilityFamilyMetric {
	out := make([]CapabilityFamilyMetric, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	out, _ = normalizeCapabilityFamilies(out)
	return out
}
