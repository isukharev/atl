package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const jiraEpicChildrenCap = 1000

// JiraEpicChild is the compact, stable read model stored for one epic child.
// It deliberately carries no arbitrary raw fields: the sidecar is a derived
// offline-render input, not another issue snapshot.
type JiraEpicChild struct {
	Key      string `json:"key"`
	Summary  string `json:"summary,omitempty"`
	Status   string `json:"status,omitempty"`
	Type     string `json:"type,omitempty"`
	Assignee string `json:"assignee,omitempty"`
}

// JiraEpicChildrenSidecar is written next to an epic snapshot when the
// epic_children render section is enabled. Truncated is explicit and
// conservative: when the shared query hits its cap, every epic in that query is
// marked incomplete because omitted children cannot be attributed safely.
type JiraEpicChildrenSidecar struct {
	Epic         string          `json:"epic"`
	EpicField    string          `json:"epic_field"`
	EpicSelector string          `json:"epic_selector,omitempty"`
	Children     []JiraEpicChild `json:"children"`
	Truncated    bool            `json:"truncated,omitempty"`
	TruncatedAt  int             `json:"truncated_at,omitempty"`
}

func epicChildrenPath(dir, keySeg string) string {
	return filepath.Join(dir, keySeg+".epic-children.json")
}

func writeEpicChildrenSidecar(root, path string, sidecar JiraEpicChildrenSidecar) error {
	b, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return err
	}
	return safepath.WriteFileWithin(root, path, append(b, '\n'), 0o644)
}

func loadEpicChildrenSidecar(root, path string) *JiraEpicChildrenSidecar {
	b, err := safepath.ReadFileWithin(root, path)
	if err != nil {
		return nil
	}
	var sidecar JiraEpicChildrenSidecar
	if json.Unmarshal(b, &sidecar) != nil || sidecar.Epic == "" || !isDirectEpicFieldID(sidecar.EpicField) {
		return nil
	}
	if sidecar.Children == nil {
		sidecar.Children = []JiraEpicChild{}
	}
	return &sidecar
}

// resolveEpicField returns the raw API field id used to group children. An
// explicit config value may be either an id or display name. With no config we
// resolve the conventional Jira Software "Epic Link" field once per pull.
func (s *JiraService) resolveEpicField(ctx context.Context, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if isDirectEpicFieldID(configured) {
		return canonicalEpicFieldID(configured), nil
	}
	defs, err := s.tr.Fields(ctx)
	if err != nil {
		return "", err
	}
	for _, def := range defs {
		if configured != "" && (strings.EqualFold(def.ID, configured) || strings.EqualFold(def.Name, configured)) {
			return def.ID, nil
		}
		if configured == "" && strings.EqualFold(def.Name, "Epic Link") {
			return def.ID, nil
		}
	}
	if configured != "" {
		return "", fmt.Errorf("%w: configured Jira epic field %q was not found", domain.ErrUsage, configured)
	}
	return "", fmt.Errorf("%w: no Jira 'Epic Link' field was found; set render.jira.epic_field explicitly", domain.ErrUsage)
}

func isDirectEpicFieldID(field string) bool {
	if strings.EqualFold(field, "parent") {
		return true
	}
	const prefix = "customfield_"
	field = strings.ToLower(strings.TrimSpace(field))
	if !strings.HasPrefix(field, prefix) {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(field, prefix))
	return err == nil && n > 0
}

func canonicalEpicFieldID(field string) string {
	if strings.EqualFold(strings.TrimSpace(field), "parent") {
		return "parent"
	}
	return strings.ToLower(strings.TrimSpace(field))
}

// fetchEpicChildrenPage performs at most one logical paginated query for all
// epics in a main-search page (up to 100). There is no per-child or per-epic
// refetch. Non-epic issues simply have no entry in the result map.
func (s *JiraService) fetchEpicChildrenPage(ctx context.Context, issues []domain.Issue, epicField string) (map[string]JiraEpicChildrenSidecar, bool, error) {
	return s.fetchEpicChildrenPageMode(ctx, issues, epicField, true)
}

// fetchEpicChildrenPageTransient omits child issue type because the transient
// Markdown row does not render or persist it. Mirror sidecars keep the wider
// projection through fetchEpicChildrenPage for offline compatibility.
func (s *JiraService) fetchEpicChildrenPageTransient(ctx context.Context, issues []domain.Issue, epicField string) (map[string]JiraEpicChildrenSidecar, bool, error) {
	return s.fetchEpicChildrenPageMode(ctx, issues, epicField, false)
}

func (s *JiraService) fetchEpicChildrenPageMode(ctx context.Context, issues []domain.Issue, epicField string, includeChildType bool) (map[string]JiraEpicChildrenSidecar, bool, error) {
	byEpic := map[string]JiraEpicChildrenSidecar{}
	var candidateKeys []string
	candidates := map[string]bool{}
	for _, issue := range issues {
		if issue.Key == "" {
			continue
		}
		candidateKeys = append(candidateKeys, issue.Key)
		candidates[issue.Key] = true
		if isEpicIssue(issue) {
			byEpic[issue.Key] = JiraEpicChildrenSidecar{Epic: issue.Key, EpicField: epicField, Children: []JiraEpicChild{}}
		}
	}
	if len(candidateKeys) == 0 {
		return byEpic, false, nil
	}
	sort.Strings(candidateKeys)
	jql := fmt.Sprintf("%s in (%s) ORDER BY key", epicJQLField(epicField), quoteJQLValues(candidateKeys))
	fields := []string{"summary", "status", "assignee", epicField}
	if includeChildType {
		// Preserve the established mirror-sidecar projection order.
		fields = []string{"summary", "status", "issuetype", "assignee", epicField}
	}
	cursor := ""
	total := 0
	truncated := false
	for total < jiraEpicChildrenCap {
		pageLimit := 100
		if jiraEpicChildrenCap-total < pageLimit {
			pageLimit = jiraEpicChildrenCap - total
		}
		children, next, err := s.tr.Search(ctx, jql, fields, pageLimit, cursor)
		if err != nil {
			return nil, false, err
		}
		for _, child := range children {
			parent := fieldString(child.Fields[epicField])
			if !candidates[parent] {
				continue
			}
			sidecar, ok := byEpic[parent]
			if !ok {
				sidecar = JiraEpicChildrenSidecar{Epic: parent, EpicField: epicField, Children: []JiraEpicChild{}}
			}
			sidecar.Children = append(sidecar.Children, JiraEpicChild{
				Key: child.Key, Summary: child.Summary, Status: child.Status,
				Type: child.Type, Assignee: child.Assignee,
			})
			byEpic[parent] = sidecar
		}
		total += len(children)
		if next == "" || len(children) == 0 {
			break
		}
		if total >= jiraEpicChildrenCap {
			truncated = true
			break
		}
		cursor = next
	}
	for key, sidecar := range byEpic {
		sort.Slice(sidecar.Children, func(i, j int) bool { return sidecar.Children[i].Key < sidecar.Children[j].Key })
		if truncated {
			sidecar.Truncated = true
			sidecar.TruncatedAt = jiraEpicChildrenCap
		}
		byEpic[key] = sidecar
	}
	return byEpic, truncated, nil
}

func hasEpicCandidate(issues []domain.Issue) bool {
	for _, issue := range issues {
		if isEpicIssue(issue) {
			return true
		}
	}
	return false
}

func isEpicIssue(issue domain.Issue) bool {
	if issueType, ok := issue.Fields["issuetype"].(map[string]any); ok {
		switch level := issueType["hierarchyLevel"].(type) {
		case float64:
			if level == 1 {
				return true
			}
		case json.Number:
			if n, err := strconv.Atoi(level.String()); err == nil && n == 1 {
				return true
			}
		}
		if strings.EqualFold(asString(issueType["name"]), "epic") {
			return true
		}
	}
	return strings.EqualFold(issue.Type, "epic")
}

func compatibleEpicSidecar(sidecar *JiraEpicChildrenSidecar, issueKey, epicField string) bool {
	if sidecar == nil || sidecar.Epic != issueKey {
		return false
	}
	epicField = strings.TrimSpace(epicField)
	if isDirectEpicFieldID(epicField) {
		return canonicalEpicFieldID(sidecar.EpicField) == canonicalEpicFieldID(epicField)
	}
	if epicField == "" {
		return sidecar.EpicSelector == ""
	}
	return sidecar.EpicSelector != "" && strings.EqualFold(strings.TrimSpace(sidecar.EpicSelector), epicField)
}

func epicJQLField(field string) string {
	const prefix = "customfield_"
	if strings.HasPrefix(field, prefix) {
		if n, err := strconv.Atoi(strings.TrimPrefix(field, prefix)); err == nil && n > 0 {
			return fmt.Sprintf("cf[%d]", n)
		}
	}
	return `"` + strings.ReplaceAll(field, `"`, `\"`) + `"`
}

func quoteJQLValues(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		quoted = append(quoted, `"`+value+`"`)
	}
	return strings.Join(quoted, ",")
}
