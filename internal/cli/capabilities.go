package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

const capabilityCatalogSchemaVersion = 1

type capabilityDefinition struct {
	ID           string
	TaskClass    string
	Service      string
	Role         string
	Priority     int
	Summary      string
	Command      string
	Evidence     string
	Completeness string
	Skill        string
	Reference    string
}

type capability struct {
	ID           string   `json:"id"`
	TaskClass    string   `json:"task_class"`
	Service      string   `json:"service"`
	Role         string   `json:"role"`
	Priority     int      `json:"priority"`
	Summary      string   `json:"summary"`
	Command      string   `json:"command"`
	Access       string   `json:"access"`
	OutputModes  []string `json:"output_modes"`
	Evidence     string   `json:"evidence"`
	Completeness string   `json:"completeness"`
	Skill        string   `json:"skill"`
	Reference    string   `json:"reference"`
}

type capabilitySelection struct {
	Task    string `json:"task,omitempty"`
	Service string `json:"service,omitempty"`
	Access  string `json:"access,omitempty"`
	ID      string `json:"id,omitempty"`
	Count   int    `json:"count"`
}

type capabilityCatalog struct {
	SchemaVersion int                 `json:"schema_version"`
	Routing       capabilityRouting   `json:"routing"`
	Selection     capabilitySelection `json:"selection"`
	Capabilities  []capability        `json:"capabilities"`
}

type capabilityRouting struct {
	Match         string `json:"match"`
	ReferenceLoad string `json:"reference_load"`
	Stop          string `json:"stop"`
}

// capabilityDefinitions is intentionally small and curated. It routes exact
// agent task classes to the shortest reviewed command sequence; it is not a
// fuzzy natural-language classifier or a second command registry. Access and
// output facts are derived from the Cobra command tree below, so CI catches
// drift between this routing layer and the executable contract.
var capabilityDefinitions = []capabilityDefinition{
	{ID: "jira.issue.fields", TaskClass: "jira/evidence", Service: "jira", Role: "discover", Priority: 10, Summary: "Discover non-empty issue fields with names and compact values", Command: "jira issue fields", Evidence: "qualified", Completeness: "explicit", Skill: "jira", Reference: "reference/evidence-workflow.md"},
	{ID: "jira.epic.digest", TaskClass: "jira/evidence", Service: "jira", Role: "primary", Priority: 20, Summary: "Collect bounded multi-source evidence for one epic and period", Command: "jira epic digest", Evidence: "qualified", Completeness: "per-source", Skill: "jira", Reference: "reference/evidence-workflow.md"},
	{ID: "jira.issue.field.get", TaskClass: "jira/evidence", Service: "jira", Role: "expand", Priority: 30, Summary: "Expand one exact clipped field with a hard byte bound", Command: "jira issue field get", Evidence: "qualified", Completeness: "explicit", Skill: "jira", Reference: "reference/evidence-workflow.md"},
	{ID: "jira.issue.refs", TaskClass: "jira/evidence", Service: "jira", Role: "expand", Priority: 40, Summary: "Extract provenance-qualified artifact references", Command: "jira issue refs", Evidence: "qualified", Completeness: "per-source", Skill: "jira", Reference: "reference/evidence-workflow.md"},
	{ID: "jira.issue.history", TaskClass: "jira/evidence", Service: "jira", Role: "expand", Priority: 50, Summary: "Read field-filtered changelog evidence within a period", Command: "jira issue history", Evidence: "qualified", Completeness: "explicit", Skill: "jira", Reference: "reference/evidence-workflow.md"},

	{ID: "jira.board.list", TaskClass: "jira/portfolio", Service: "jira", Role: "discover", Priority: 10, Summary: "Discover Agile boards and stable identifiers", Command: "jira board list", Evidence: "snapshot", Completeness: "pagination", Skill: "jira", Reference: "reference/portfolio-evidence.md"},
	{ID: "jira.board.view", TaskClass: "jira/portfolio", Service: "jira", Role: "primary", Priority: 20, Summary: "Read normalized board configuration, issues, and backlog", Command: "jira board view", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/portfolio-evidence.md"},
	{ID: "jira.structure.folders", TaskClass: "jira/portfolio", Service: "jira", Role: "discover", Priority: 30, Summary: "Discover Structure folders before selecting a subtree", Command: "jira structure folders", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/portfolio-evidence.md"},
	{ID: "jira.structure.view", TaskClass: "jira/portfolio", Service: "jira", Role: "primary", Priority: 40, Summary: "Read a normalized Structure hierarchy with selected issue fields", Command: "jira structure view", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/portfolio-evidence.md"},
	{ID: "jira.portfolio.epic.digest", TaskClass: "jira/portfolio", Service: "jira", Role: "expand", Priority: 50, Summary: "Qualify only the evidence sources missing from the portfolio snapshot", Command: "jira epic digest", Evidence: "qualified", Completeness: "per-source", Skill: "jira", Reference: "reference/portfolio-evidence.md"},
	{ID: "jira.portfolio.confluence.section", TaskClass: "jira/portfolio", Service: "confluence", Role: "expand", Priority: 60, Summary: "Return one bounded linked evidence section instead of a full-page view", Command: "conf page section", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/commands.md"},

	{ID: "jira.board-portfolio.fields", TaskClass: "jira/board-portfolio", Service: "jira", Role: "discover", Priority: 10, Summary: "Resolve the narrow custom field used by the board evidence", Command: "jira fields", Evidence: "identity", Completeness: "explicit", Skill: "jira", Reference: "reference/portfolio-evidence.md"},
	{ID: "jira.board-portfolio.view", TaskClass: "jira/board-portfolio", Service: "jira", Role: "primary", Priority: 20, Summary: "Read one normalized board snapshot for portfolio membership and child state", Command: "jira board view", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/portfolio-evidence.md"},
	{ID: "jira.board-portfolio.epic.digest", TaskClass: "jira/board-portfolio", Service: "jira", Role: "expand", Priority: 30, Summary: "Qualify only the per-epic field and history evidence absent from the board", Command: "jira epic digest", Evidence: "qualified", Completeness: "per-source", Skill: "jira", Reference: "reference/portfolio-evidence.md"},

	{ID: "jira.batch.issue.export", TaskClass: "jira/batch-analysis", Service: "jira", Role: "primary", Priority: 10, Summary: "Read an ordered explicit issue selector set without durable artifacts", Command: "jira export", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/batch-read.md"},

	{ID: "jira.structure.rows", TaskClass: "jira/structure-planning", Service: "jira", Role: "discover", Priority: 10, Summary: "Read a selected Structure subtree without resolving issue fields", Command: "jira structure rows", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/structure-batch.md"},
	{ID: "jira.structure.values", TaskClass: "jira/structure-planning", Service: "jira", Role: "expand", Priority: 20, Summary: "Read an explicit Structure row and attribute value matrix", Command: "jira structure values", Evidence: "snapshot", Completeness: "per-row", Skill: "jira", Reference: "reference/structure-batch.md"},
	{ID: "jira.structure.issue.export", TaskClass: "jira/structure-planning", Service: "jira", Role: "expand", Priority: 30, Summary: "Read an ordered explicit issue batch without durable artifacts", Command: "jira export", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/structure-batch.md"},

	{ID: "jira.issue.fields.edit", TaskClass: "jira/edit", Service: "jira", Role: "discover", Priority: 10, Summary: "Resolve editable custom-field names and current values", Command: "jira issue fields", Evidence: "qualified", Completeness: "explicit", Skill: "jira", Reference: "reference/fields.md"},
	{ID: "jira.issue.field.preview", TaskClass: "jira/edit", Service: "jira", Role: "preview", Priority: 20, Summary: "Build a read-only file-backed custom-field proposal", Command: "jira issue field preview", Evidence: "version-gated", Completeness: "explicit", Skill: "jira", Reference: "reference/fields.md"},
	{ID: "jira.issue.field.set", TaskClass: "jira/edit", Service: "jira", Role: "write", Priority: 30, Summary: "Apply one reviewed custom-field proposal", Command: "jira issue field set", Evidence: "version-gated", Completeness: "reconciled", Skill: "jira", Reference: "reference/fields.md"},
	{ID: "jira.issue.worklog.list", TaskClass: "jira/edit", Service: "jira", Role: "discover", Priority: 40, Summary: "Read the complete worklog baseline before a reviewed add", Command: "jira issue worklog list", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/editing.md"},
	{ID: "jira.issue.worklog.add", TaskClass: "jira/edit", Service: "jira", Role: "write", Priority: 50, Summary: "Preview then apply one baseline-bound worklog without replaying an ambiguous write", Command: "jira issue worklog add", Evidence: "hash-bound", Completeness: "reconciled", Skill: "jira", Reference: "reference/editing.md"},
	{ID: "jira.issue.plan.apply", TaskClass: "jira/edit", Service: "jira", Role: "write", Priority: 60, Summary: "Apply a reviewed guarded multi-issue plan", Command: "jira issue plan apply", Evidence: "version-gated", Completeness: "per-row", Skill: "jira", Reference: "reference/commands.md"},

	{ID: "confluence.page.resolve", TaskClass: "confluence/evidence", Service: "confluence", Role: "discover", Priority: 10, Summary: "Resolve page IDs, full URLs, or short links", Command: "conf page resolve", Evidence: "identity", Completeness: "exact", Skill: "confluence", Reference: "reference/commands.md"},
	{ID: "confluence.page.outline", TaskClass: "confluence/evidence", Service: "confluence", Role: "discover", Priority: 20, Summary: "Return a heading inventory without exposing the full rendered page", Command: "conf page outline", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/commands.md"},
	{ID: "confluence.page.section", TaskClass: "confluence/evidence", Service: "confluence", Role: "primary", Priority: 30, Summary: "Read one bounded rendered section by heading", Command: "conf page section", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/commands.md"},
	{ID: "confluence.page.view", TaskClass: "confluence/evidence", Service: "confluence", Role: "expand", Priority: 40, Summary: "Read a transient full Markdown page view", Command: "conf page view", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/commands.md"},
	{ID: "confluence.table.summary", TaskClass: "confluence/table-analytics", Service: "confluence", Role: "discover", Priority: 10, Summary: "Inventory table shapes, spans, links, and styles without exposing cell content", Command: "conf table summary", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/tables-attachments.md"},
	{ID: "confluence.table.extract", TaskClass: "confluence/table-analytics", Service: "confluence", Role: "primary", Priority: 20, Summary: "Extract one structured table with spans, links, and spreadsheet-safe values", Command: "conf table extract", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/tables-attachments.md"},

	{ID: "confluence.pull", TaskClass: "confluence/edit", Service: "confluence", Role: "stage", Priority: 10, Summary: "Mirror native CSF and its versioned Markdown view", Command: "conf pull", Evidence: "version-gated", Completeness: "explicit", Skill: "confluence", Reference: "reference/push.md"},
	{ID: "confluence.diff", TaskClass: "confluence/edit", Service: "confluence", Role: "review", Priority: 20, Summary: "Inspect offline semantic and byte changes", Command: "conf diff", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/push.md"},
	{ID: "confluence.plan.create", TaskClass: "confluence/edit", Service: "confluence", Role: "plan", Priority: 30, Summary: "Create a durable review-bound batch update plan", Command: "conf plan create", Evidence: "hash-bound", Completeness: "explicit", Skill: "confluence", Reference: "reference/push.md"},
	{ID: "confluence.plan.preview", TaskClass: "confluence/edit", Service: "confluence", Role: "review", Priority: 40, Summary: "Preflight a plan locally and remotely without writes", Command: "conf plan preview", Evidence: "hash-bound", Completeness: "per-page", Skill: "confluence", Reference: "reference/push.md"},
	{ID: "confluence.plan.apply", TaskClass: "confluence/edit", Service: "confluence", Role: "write", Priority: 50, Summary: "Apply an explicitly confirmed reviewed plan", Command: "conf plan apply", Evidence: "version-gated", Completeness: "per-page", Skill: "confluence", Reference: "reference/push.md"},

	{ID: "knowledge.jira.search", TaskClass: "knowledge/search", Service: "jira", Role: "discover", Priority: 10, Summary: "Search Jira for current topic candidates", Command: "jira issue search", Evidence: "snapshot", Completeness: "explicit", Skill: "search-knowledge", Reference: "SKILL.md"},
	{ID: "knowledge.confluence.search", TaskClass: "knowledge/search", Service: "confluence", Role: "discover", Priority: 20, Summary: "Search Confluence for current topic candidates", Command: "conf search", Evidence: "snapshot", Completeness: "explicit", Skill: "confluence", Reference: "reference/commands.md"},
	{ID: "knowledge.jira.field", TaskClass: "knowledge/search", Service: "jira", Role: "expand", Priority: 30, Summary: "Expand one exact selected Jira field", Command: "jira issue field get", Evidence: "qualified", Completeness: "explicit", Skill: "jira", Reference: "reference/evidence-workflow.md"},
	{ID: "knowledge.confluence.outline", TaskClass: "knowledge/search", Service: "confluence", Role: "expand", Priority: 40, Summary: "Inspect selected-page headings without exposing its full body", Command: "conf page outline", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/commands.md"},
	{ID: "knowledge.confluence.section", TaskClass: "knowledge/search", Service: "confluence", Role: "expand", Priority: 50, Summary: "Read one bounded selected-page section", Command: "conf page section", Evidence: "derived", Completeness: "explicit", Skill: "confluence", Reference: "reference/commands.md"},
}

func newCapabilitiesCmd() *cobra.Command {
	var task, service, access, id string
	c := &cobra.Command{
		Use:   "capabilities",
		Short: "Query the versioned offline agent capability catalog",
		Long: "Query exact task-to-command routes without loading config, credentials, or network state.\n" +
			"The catalog is deterministic and derives access/output facts from the registered CLI tree.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			catalog, err := buildCapabilityCatalog(cmd.Root(), capabilitySelection{
				Task: strings.TrimSpace(task), Service: strings.TrimSpace(service),
				Access: strings.TrimSpace(access), ID: strings.TrimSpace(id),
			})
			if err != nil {
				return err
			}
			return emitID(cmd, catalog, func() string { return capabilityCatalogMarkdown(catalog) }, func() []string {
				ids := make([]string, len(catalog.Capabilities))
				for i := range catalog.Capabilities {
					ids[i] = catalog.Capabilities[i].ID
				}
				return ids
			})
		},
	}
	c.Flags().StringVar(&task, "task", "", "exact task class (jira/evidence, jira/portfolio, jira/board-portfolio, jira/batch-analysis, jira/structure-planning, jira/edit, confluence/evidence, confluence/table-analytics, confluence/edit, knowledge/search)")
	c.Flags().StringVar(&service, "service", "", "exact service: jira|confluence")
	c.Flags().StringVar(&access, "access", "", "exact access class: read-only|mutating")
	c.Flags().StringVar(&id, "id", "", "exact capability id")
	_ = c.RegisterFlagCompletionFunc("task", fixedComp("jira/evidence", "jira/portfolio", "jira/board-portfolio", "jira/batch-analysis", "jira/structure-planning", "jira/edit", "confluence/evidence", "confluence/table-analytics", "confluence/edit", "knowledge/search"))
	_ = c.RegisterFlagCompletionFunc("service", fixedComp("jira", "confluence"))
	_ = c.RegisterFlagCompletionFunc("access", fixedComp("read-only", "mutating"))
	return c
}

func buildCapabilityCatalog(root *cobra.Command, selection capabilitySelection) (capabilityCatalog, error) {
	if selection.Service != "" && selection.Service != "jira" && selection.Service != "confluence" {
		return capabilityCatalog{}, usageErr("invalid capability service %q (want jira|confluence)", selection.Service)
	}
	if selection.Access != "" && selection.Access != "read-only" && selection.Access != "mutating" {
		return capabilityCatalog{}, usageErr("invalid capability access %q (want read-only|mutating)", selection.Access)
	}

	items := make([]capability, 0, len(capabilityDefinitions))
	for _, definition := range capabilityDefinitions {
		if selection.Task != "" && definition.TaskClass != selection.Task {
			continue
		}
		if selection.Service != "" && definition.Service != selection.Service {
			continue
		}
		if selection.ID != "" && definition.ID != selection.ID {
			continue
		}
		command, remaining, err := root.Find(strings.Fields(definition.Command))
		if err != nil || len(remaining) != 0 || command == nil || (command.Run == nil && command.RunE == nil) {
			return capabilityCatalog{}, fmt.Errorf("%w: capability %q references unregistered command %q", domain.ErrCheckFailed, definition.ID, definition.Command)
		}
		commandAccess := command.Annotations[accessAnnotation]
		if commandAccess != "read-only" && commandAccess != "mutating" {
			return capabilityCatalog{}, fmt.Errorf("%w: capability %q command %q has invalid access metadata", domain.ErrCheckFailed, definition.ID, definition.Command)
		}
		if selection.Access != "" && commandAccess != selection.Access {
			continue
		}
		modes := []string{"json"}
		if command.Annotations[textOutputAnnotation] == "supported" {
			modes = append(modes, "text")
		}
		if command.Annotations[idOutputAnnotation] == "supported" {
			modes = append(modes, "id")
		}
		items = append(items, capability{
			ID: definition.ID, TaskClass: definition.TaskClass, Service: definition.Service,
			Role: definition.Role, Priority: definition.Priority, Summary: definition.Summary,
			Command: definition.Command, Access: commandAccess, OutputModes: modes,
			Evidence: definition.Evidence, Completeness: definition.Completeness,
			Skill: definition.Skill, Reference: definition.Reference,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].TaskClass != items[j].TaskClass {
			return items[i].TaskClass < items[j].TaskClass
		}
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].ID < items[j].ID
	})
	if len(items) == 0 && (selection.Task != "" || selection.ID != "") {
		return capabilityCatalog{}, fmt.Errorf("%w: no capability matches the exact selection", domain.ErrNotFound)
	}
	selection.Count = len(items)
	return capabilityCatalog{
		SchemaVersion: capabilityCatalogSchemaVersion,
		Routing: capabilityRouting{
			Match:         "exact",
			ReferenceLoad: "invoke capability.skill, then open capability.reference relative to that skill; do not search the filesystem",
			Stop:          "stop expanding the route when sufficient complete evidence is available",
		},
		Selection: selection, Capabilities: items,
	}, nil
}

func capabilityCatalogMarkdown(catalog capabilityCatalog) string {
	var b strings.Builder
	b.WriteString("| Capability | Role | Access | Command | Evidence | Reference |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, item := range catalog.Capabilities {
		fmt.Fprintf(&b, "| `%s` | %s | %s | `atl %s` | %s/%s | `%s/%s` |\n",
			item.ID, item.Role, item.Access, item.Command, item.Evidence, item.Completeness, item.Skill, item.Reference)
	}
	return strings.TrimRight(b.String(), "\n")
}
