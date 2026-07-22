package agenteval

import (
	"fmt"
	"slices"
	"sort"
	"strings"
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
	"jira_issue_field_get": "jira.issue.field", "jira_issue_refs": "jira.issue.refs",
	"jira_epic_digest": "jira.epic.digest", "jira_board_view": "jira.board.view",
	"confluence_page_resolve": "confluence.page.resolve", "confluence_page_outline": "confluence.page.outline",
	"confluence_page_section": "confluence.page.section", "confluence_search": "confluence.search",
}

var allowedCapabilityFamilies = map[string]struct{}{
	"atl.config": {}, "atl.capabilities": {}, "jira.fields": {}, "jira.issue.fields": {},
	"jira.issue.field": {}, "jira.issue.field.preview": {}, "jira.issue.field.set": {}, "jira.issue.refs": {}, "jira.issue.worklog.list": {}, "jira.issue.worklog.add": {}, "jira.issue.search": {}, "jira.issue.batch-read": {}, "jira.epic.digest": {}, "jira.board.list": {}, "jira.board.view": {},
	"jira.issue.history": {},
	"jira.field-options": {}, "jira.link-types": {}, "jira.me": {},
	"jira.sprint.current": {}, "jira.sprint.get": {}, "jira.sprint.issues": {}, "jira.sprint.list": {},
	"jira.transitions": {}, "jira.user.get": {}, "jira.user.search": {},
	"jira.issue.attachment.get": {}, "jira.issue.attachment.list": {}, "jira.issue.check": {}, "jira.issue.children": {},
	"jira.issue.comment.list": {}, "jira.issue.get": {}, "jira.issue.images": {}, "jira.issue.link.list": {},
	"jira.issue.tree": {}, "jira.issue.view": {}, "jira.issue.watchers.list": {},
	"jira.planning.report": {},
	"jira.export":          {}, "jira.export.diff": {}, "jira.structure.get": {}, "jira.structure.forest": {}, "jira.structure.folders": {},
	"jira.structure.rows": {}, "jira.structure.values": {}, "jira.structure.pull-issues": {}, "jira.structure.export": {}, "jira.structure.view": {},
	"confluence.diff": {}, "confluence.search": {}, "confluence.page.resolve": {}, "confluence.page.outline": {}, "confluence.page.section": {},
	"confluence.page.meta": {}, "confluence.page.history": {}, "confluence.page.view": {}, "confluence.attachment.list": {},
	"confluence.attachment.get": {}, "confluence.comment.list": {}, "confluence.me": {},
	"confluence.page.get": {}, "confluence.page.labels.list": {}, "confluence.page.list": {}, "confluence.space.tree": {},
	"confluence.table.extract": {},
	"confluence.table.summary": {},
	"confluence.plan.create":   {}, "confluence.plan.preview": {}, "confluence.plan.apply": {},
}

// neutralDataCapability maps route-specific capability families onto the
// backend data each route can expose. Neutral-common runs may use different
// mechanics (for example an ordered CLI export versus a typed search tool),
// but they must grant the model the same semantic data surface.
var neutralDataCapability = map[string]string{
	"jira.fields":              "jira.fields",
	"jira.issue.fields":        "jira.issue.fields",
	"jira.issue.field":         "jira.issue.field",
	"jira.issue.refs":          "jira.issue.refs",
	"jira.issue.search":        "jira.issue.list",
	"jira.issue.batch-read":    "jira.issue.list",
	"jira.planning.report":     "jira.issue.list",
	"jira.epic.digest":         "jira.epic.digest",
	"jira.board.view":          "jira.board.view",
	"confluence.search":        "confluence.search",
	"confluence.page.resolve":  "confluence.page.resolve",
	"confluence.page.outline":  "confluence.page.outline",
	"confluence.page.section":  "confluence.page.section",
	"confluence.table.extract": "confluence.table.extract",
	"confluence.table.summary": "confluence.table.summary",
}

func validateRunDataCapabilities(spec RunSpec) error {
	if spec.EffectiveCategory() != BenchmarkCategoryNeutralCommon {
		if len(spec.DataCapabilities) != 0 {
			return fmt.Errorf("data_capabilities are valid only for neutral-common runs")
		}
		return nil
	}
	if len(spec.DataCapabilities) == 0 || len(spec.DataCapabilities) > 32 {
		return fmt.Errorf("neutral-common runs require 1..32 data_capabilities")
	}
	for index, capability := range spec.DataCapabilities {
		if !identifierRE.MatchString(capability) {
			return fmt.Errorf("invalid neutral data capability")
		}
		if index > 0 && spec.DataCapabilities[index-1] >= capability {
			return fmt.Errorf("data_capabilities must be sorted and unique")
		}
	}
	if spec.EffectiveSurface() == SurfaceExternalMCP {
		// The private profile binds opaque tool names to reviewed public
		// capability families at run time.
		return nil
	}
	derived, err := deriveRunDataCapabilities(spec)
	if err != nil {
		return err
	}
	if !equalStrings(derived, spec.DataCapabilities) {
		return fmt.Errorf("data_capabilities do not match the allowed interface routes")
	}
	return nil
}

func deriveRunDataCapabilities(spec RunSpec) ([]string, error) {
	families := map[string]struct{}{}
	addFamily := func(routeFamily string) error {
		if routeFamily == "atl.config" || routeFamily == "atl.capabilities" {
			return nil
		}
		dataFamily, ok := neutralDataCapability[routeFamily]
		if !ok {
			return fmt.Errorf("neutral-common run exposes an unclassified data route")
		}
		families[dataFamily] = struct{}{}
		return nil
	}
	for _, command := range spec.AllowedATLCommands {
		args := strings.Fields(strings.TrimPrefix(command, "atl "))
		family, ok := CapabilityFamilyForCLI(args)
		if !ok {
			return nil, fmt.Errorf("neutral-common run exposes an unknown atl route")
		}
		if err := addFamily(family); err != nil {
			return nil, err
		}
	}
	for _, rule := range spec.AllowedCLICommands {
		args := append([]string(nil), rule.Command...)
		for _, flag := range rule.Flags {
			if flag.Required {
				args = append(args, flag.Name)
			}
		}
		family, ok := CapabilityFamilyForCLI(args)
		if !ok {
			return nil, fmt.Errorf("neutral-common run exposes an unknown cli route")
		}
		if err := addFamily(family); err != nil {
			return nil, err
		}
	}
	for _, tool := range spec.AllowedMCPTools {
		family, ok := CapabilityFamilyForMCP(tool)
		if !ok {
			return nil, fmt.Errorf("neutral-common run exposes an unknown typed MCP route")
		}
		if err := addFamily(family); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(families))
	for family := range families {
		result = append(result, family)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, fmt.Errorf("neutral-common run exposes no data route")
	}
	return result, nil
}

func equalStrings(left, right []string) bool {
	return slices.Equal(left, right)
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
	// An identity-selected export is atl's bounded transient batch-read path.
	// Keep it distinct from query/file exports so benchmark trajectories can
	// compare point-loop and batch-read behavior without retaining selectors.
	if hasCommandPrefix(args, "jira", "export") && !hasCommandPrefix(args, "jira", "export", "diff") {
		if hasCLIFlag(args[2:], "--keys") || hasCLIFlag(args[2:], "--ids") {
			return "jira.issue.batch-read", true
		}
	}
	patterns := []struct {
		path   []string
		family string
	}{
		{[]string{"config", "show"}, "atl.config"}, {[]string{"capabilities"}, "atl.capabilities"},
		{[]string{"jira", "issue", "field", "preview"}, "jira.issue.field.preview"}, {[]string{"jira", "issue", "field", "set"}, "jira.issue.field.set"},
		{[]string{"jira", "issue", "worklog", "list"}, "jira.issue.worklog.list"},
		{[]string{"jira", "issue", "worklog", "add"}, "jira.issue.worklog.add"},
		{[]string{"jira", "issue", "history"}, "jira.issue.history"},
		{[]string{"jira", "issue", "attachment", "get"}, "jira.issue.attachment.get"}, {[]string{"jira", "issue", "attachment", "list"}, "jira.issue.attachment.list"},
		{[]string{"jira", "issue", "comment", "list"}, "jira.issue.comment.list"}, {[]string{"jira", "issue", "link", "list"}, "jira.issue.link.list"},
		{[]string{"jira", "issue", "watchers", "list"}, "jira.issue.watchers.list"},
		{[]string{"jira", "issue", "check"}, "jira.issue.check"}, {[]string{"jira", "issue", "children"}, "jira.issue.children"},
		{[]string{"jira", "issue", "images"}, "jira.issue.images"}, {[]string{"jira", "issue", "tree"}, "jira.issue.tree"},
		{[]string{"jira", "issue", "view"}, "jira.issue.view"}, {[]string{"jira", "issue", "get"}, "jira.issue.get"},
		{[]string{"jira", "issue", "field", "get"}, "jira.issue.field"}, {[]string{"jira", "issue", "fields"}, "jira.issue.fields"}, {[]string{"jira", "issue", "refs"}, "jira.issue.refs"}, {[]string{"jira", "epic", "digest"}, "jira.epic.digest"},
		{[]string{"jira", "issue", "search"}, "jira.issue.search"}, {[]string{"jira", "board", "list"}, "jira.board.list"}, {[]string{"jira", "board", "view"}, "jira.board.view"}, {[]string{"jira", "fields"}, "jira.fields"},
		{[]string{"jira", "planning", "report"}, "jira.planning.report"}, {[]string{"jira", "quality-report"}, "jira.planning.report"},
		{[]string{"jira", "field-options"}, "jira.field-options"}, {[]string{"jira", "link-types"}, "jira.link-types"}, {[]string{"jira", "me"}, "jira.me"},
		{[]string{"jira", "sprint", "current"}, "jira.sprint.current"}, {[]string{"jira", "sprint", "get"}, "jira.sprint.get"},
		{[]string{"jira", "sprint", "issues"}, "jira.sprint.issues"}, {[]string{"jira", "sprint", "list"}, "jira.sprint.list"},
		{[]string{"jira", "transitions"}, "jira.transitions"}, {[]string{"jira", "user", "get"}, "jira.user.get"}, {[]string{"jira", "user", "search"}, "jira.user.search"},
		{[]string{"jira", "export", "diff"}, "jira.export.diff"}, {[]string{"jira", "export"}, "jira.export"},
		{[]string{"jira", "structure", "get"}, "jira.structure.get"}, {[]string{"jira", "structure", "forest"}, "jira.structure.forest"},
		{[]string{"jira", "structure", "folders"}, "jira.structure.folders"}, {[]string{"jira", "structure", "rows"}, "jira.structure.rows"},
		{[]string{"jira", "structure", "values"}, "jira.structure.values"}, {[]string{"jira", "structure", "pull-issues"}, "jira.structure.pull-issues"},
		{[]string{"jira", "structure", "export"}, "jira.structure.export"}, {[]string{"jira", "structure", "view"}, "jira.structure.view"},
		{[]string{"conf", "diff"}, "confluence.diff"}, {[]string{"conf", "search"}, "confluence.search"},
		{[]string{"conf", "table", "extract"}, "confluence.table.extract"},
		{[]string{"conf", "table", "summary"}, "confluence.table.summary"},
		{[]string{"conf", "plan", "create"}, "confluence.plan.create"}, {[]string{"conf", "plan", "preview"}, "confluence.plan.preview"},
		{[]string{"conf", "plan", "apply"}, "confluence.plan.apply"},
		{[]string{"conf", "page", "resolve"}, "confluence.page.resolve"}, {[]string{"conf", "page", "outline"}, "confluence.page.outline"},
		{[]string{"conf", "page", "section"}, "confluence.page.section"},
		{[]string{"conf", "page", "meta"}, "confluence.page.meta"}, {[]string{"conf", "page", "history"}, "confluence.page.history"},
		{[]string{"conf", "page", "view"}, "confluence.page.view"}, {[]string{"conf", "attachment", "list"}, "confluence.attachment.list"},
		{[]string{"conf", "attachment", "get"}, "confluence.attachment.get"}, {[]string{"conf", "comment", "list"}, "confluence.comment.list"},
		{[]string{"conf", "page", "labels", "list"}, "confluence.page.labels.list"},
		{[]string{"conf", "page", "get"}, "confluence.page.get"}, {[]string{"conf", "page", "list"}, "confluence.page.list"},
		{[]string{"conf", "space", "tree"}, "confluence.space.tree"}, {[]string{"conf", "me"}, "confluence.me"},
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

func hasCommandPrefix(args []string, path ...string) bool {
	if len(args) < len(path) {
		return false
	}
	for index := range path {
		if args[index] != path[index] {
			return false
		}
	}
	return true
}

func hasCLIFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || len(arg) > len(name) && arg[:len(name)+1] == name+"=" {
			return true
		}
	}
	return false
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
